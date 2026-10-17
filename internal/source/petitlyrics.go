package source

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const petitlyricsEndpoint = "https://p0.petitlyrics.com/api/GetPetitLyricsData.php"

const (
	petitlyricsClientAppID = "p1110417"
	petitlyricsTerminal    = "10"
)

// petitlyricsLyricsTypes mirrors the API's terminal tiers: 3 = word timings,
// 2 = line timings, 1 = plain text.
const (
	petitLyricsTypeWord  = "3"
	petitLyricsTypeLine  = "2"
	petitLyricsTypePlain = "1"
)

// fetchPetitLyrics resolves lyrics from PetitLyrics: word WSY first,
// then plain text. The line-level LSY binary decode is intentionally not
// implemented this release, so its tier falls straight through to plain.
func fetchPetitLyrics(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	tiers := []string{petitLyricsTypeWord, petitLyricsTypeLine, petitLyricsTypePlain}
	var lookupErrors []error
	for _, tier := range tiers {
		response, health, err := fetchPetitLyricsTier(rt, track, tier, deadline)
		if err != nil {
			return nil, health, err
		}
		candidate, tierErr := petitLyricsCandidate(tier, response)
		if tierErr != nil {
			// A nonempty but malformed payload is a provider failure, not a
			// genuine empty tier: keep the error and still try later tiers,
			// degrading only if nothing usable comes out.
			lookupErrors = append(lookupErrors, tierErr)
			continue
		}
		if !candidate.Present() {
			continue
		}
		return []lyric.Candidate{candidate}, health, nil
	}
	if err := errors.Join(lookupErrors...); err != nil {
		return nil, Degraded, err
	}
	return nil, Healthy, nil
}

// petitLyricsCandidate converts one tier payload into a candidate. A zero
// candidate without an error is a clean miss (empty tier) so the tier loop can
// fall through; decode failures return the error so malformed provider data
// degrades instead of caching as coverage.
func petitLyricsCandidate(tier string, payload petitlyricsPayload) (lyric.Candidate, error) {
	data := strings.TrimSpace(payload.LyricsData)
	if data == "" {
		return lyric.Candidate{}, nil
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return lyric.Candidate{}, fmt.Errorf("decode PetitLyrics %s payload: %w", tier, err)
	}
	content := string(raw)

	switch tier {
	case petitLyricsTypeWord:
		lines, parseErr := lyric.ParsePetitWSY(raw)
		if parseErr != nil || len(lines) == 0 {
			if parseErr != nil {
				return lyric.Candidate{}, fmt.Errorf("parse PetitLyrics WSY payload: %w", parseErr)
			}
			return lyric.Candidate{}, nil
		}
		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: lyric.EnhancedLRC(lines)}, "PetitLyrics", "elrc")
		if candidate.Present() {
			return candidate, nil
		}
		return lyric.Candidate{}, nil
	case petitLyricsTypeLine:
		// LSY binary payloads carry timings only and are skipped this release;
		// plain rows (rare) fall through to the next tier.
		if strings.HasPrefix(content, "MHDR") {
			return lyric.Candidate{}, nil
		}
		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: content}, "PetitLyrics", "lrc")
		if candidate.Present() && candidate.Quality > lyric.TimingUnsynced {
			return candidate, nil
		}
		return lyric.Candidate{}, nil
	default:
		text := strings.TrimSpace(content)
		if text == "" {
			return lyric.Candidate{}, nil
		}
		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: text}, "PetitLyrics", "plain")
		if candidate.Present() {
			return candidate, nil
		}
		return lyric.Candidate{}, nil
	}
}

type petitlyricsPayload struct {
	LyricsData string `xml:"lyricsData"`
}

// petitlyricsStatusOK is the only status that carries song data; every other
// non-empty status is a provider failure (see fetchPetitLyricsTier).
const petitlyricsStatusOK = "00000000"

// fetchPetitLyricsTier performs one form POST of the GetPetitLyricsData API.
func fetchPetitLyricsTier(rt Runtime, track lyrics.TrackInfo, tier string, deadline time.Time) (petitlyricsPayload, Health, error) {
	title := strings.TrimSpace(track.Title)
	artist := lyric.TrackArtist(track)
	if title == "" || artist == "" {
		return petitlyricsPayload{}, Healthy, nil
	}
	form := url.Values{
		"clientAppId":  {petitlyricsClientAppID},
		"terminalType": {petitlyricsTerminal},
		"lyricsType":   {tier},
		"key_title":    {title},
		"key_artist":   {artist},
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		form.Set("key_album", album)
	}
	response, err := postForm(rt, "PetitLyrics", petitlyricsEndpoint, form, petitLyricsHeaders(rt), deadline)
	if err != nil {
		return petitlyricsPayload{}, Degraded, err
	}

	switch response.StatusCode {
	case 200:
		// fall to the XML parsing below
	case 404:
		return petitlyricsPayload{}, Healthy, nil
	case 429:
		return petitlyricsPayload{}, Degraded, rateLimitFailure(rt, "petitlyrics", "PetitLyrics", response)
	default:
		return petitlyricsPayload{}, Degraded, providerResponseError("PetitLyrics", response)
	}

	var envelope petitlyricsEnvelope
	if err := xml.Unmarshal(response.Body, &envelope); err != nil {
		return petitlyricsPayload{}, Degraded, fmt.Errorf("decode PetitLyrics response: %w", err)
	}
	if status := strings.TrimSpace(envelope.Status); status != "" && status != petitlyricsStatusOK {
		// Missing songs answer the success status with zero rows (verified
		// against the live API), so any other status is a provider failure:
		// degrade so the sweep retries instead of caching coverage.
		return petitlyricsPayload{}, Degraded, fmt.Errorf("PetitLyrics status %s", status)
	}
	// Each row carries its own metadata: the first row is not automatically
	// the requested song, and a row of a different tier would be decoded with
	// the wrong parser. Accept only the row that answers this lookup.
	titleTokens := lyric.NormalizedTokens(track.Title)
	for _, song := range envelope.Songs.Songs {
		if rowTier := strings.TrimSpace(song.LyricsType); rowTier != "" && rowTier != tier {
			continue
		}
		if rowTitle := strings.TrimSpace(song.Title); rowTitle != "" && !lyric.TokensMatch(lyric.NormalizedTokens(rowTitle), titleTokens) {
			continue
		}
		if rowArtist := strings.TrimSpace(song.Artist); rowArtist != "" && !lyric.ArtistMatch(rowArtist, artist) {
			continue
		}
		return petitlyricsPayload{LyricsData: song.LyricsData}, Healthy, nil
	}
	return petitlyricsPayload{}, Healthy, nil
}

type petitlyricsEnvelope struct {
	Status string          `xml:"status"`
	Songs  petitlyricsSong `xml:"songs"`
}

type petitlyricsSong struct {
	Songs []petitlyricsSongEntry `xml:"song"`
}

type petitlyricsSongEntry struct {
	LyricsType string `xml:"lyricsType"`
	LyricsData string `xml:"lyricsData"`
	Title      string `xml:"title"`
	Artist     string `xml:"artist"`
}

func petitLyricsHeaders(rt Runtime) map[string]string {
	return map[string]string{
		"Accept":     "text/xml, application/xml",
		"User-Agent": rt.DefaultHeaders()["User-Agent"],
	}
}
