package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
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

// fetchPetitLyricsCandidates resolves lyrics from PetitLyrics: word WSY first,
// then plain text. The line-level LSY binary decode is intentionally not
// implemented this release, so its tier falls straight through to plain.
func fetchPetitLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	tiers := []string{petitLyricsTypeWord, petitLyricsTypeLine, petitLyricsTypePlain}
	var lookupErrors []error
	for _, tier := range tiers {
		response, health, err := p.fetchPetitLyricsTier(track, tier, deadline)
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
		if !candidate.present() {
			continue
		}
		return []lyricsCandidate{candidate}, health, nil
	}
	if err := errors.Join(lookupErrors...); err != nil {
		return nil, lookupDegraded, err
	}
	return nil, lookupHealthy, nil
}

// petitLyricsCandidate converts one tier payload into a candidate. A zero
// candidate without an error is a clean miss (empty tier) so the tier loop can
// fall through; decode failures return the error so malformed provider data
// degrades instead of caching as coverage.
func petitLyricsCandidate(tier string, payload petitlyricsPayload) (lyricsCandidate, error) {
	data := strings.TrimSpace(payload.LyricsData)
	if data == "" {
		return lyricsCandidate{}, nil
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return lyricsCandidate{}, fmt.Errorf("decode PetitLyrics %s payload: %w", tier, err)
	}
	content := string(raw)

	switch tier {
	case petitLyricsTypeWord:
		lines, parseErr := parsePetitWSY(raw)
		if parseErr != nil || len(lines) == 0 {
			if parseErr != nil {
				return lyricsCandidate{}, fmt.Errorf("parse PetitLyrics WSY payload: %w", parseErr)
			}
			return lyricsCandidate{}, nil
		}
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}, "PetitLyrics", "elrc")
		if candidate.present() {
			return candidate, nil
		}
		return lyricsCandidate{}, nil
	case petitLyricsTypeLine:
		// LSY binary payloads carry timings only and are skipped this release;
		// plain rows (rare) fall through to the next tier.
		if strings.HasPrefix(content, "MHDR") {
			return lyricsCandidate{}, nil
		}
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: content}, "PetitLyrics", "lrc")
		if candidate.present() && candidate.quality > timingUnsynced {
			return candidate, nil
		}
		return lyricsCandidate{}, nil
	default:
		text := strings.TrimSpace(content)
		if text == "" {
			return lyricsCandidate{}, nil
		}
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: text}, "PetitLyrics", "plain")
		if candidate.present() {
			return candidate, nil
		}
		return lyricsCandidate{}, nil
	}
}

type petitlyricsPayload struct {
	LyricsData string `xml:"lyricsData"`
}

// petitlyricsStatusOK is the only status that carries song data; every other
// non-empty status is a provider failure (see fetchPetitLyricsTier).
const petitlyricsStatusOK = "00000000"

// fetchPetitLyricsTier performs one form POST of the GetPetitLyricsData API.
func (p *lyricsProvider) fetchPetitLyricsTier(track lyrics.TrackInfo, tier string, deadline time.Time) (petitlyricsPayload, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return petitlyricsPayload{}, lookupHealthy, nil
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
	response, err := p.postForm("PetitLyrics", petitlyricsEndpoint, form, petitLyricsHeaders(), deadline)
	if err != nil {
		return petitlyricsPayload{}, lookupDegraded, err
	}

	switch response.StatusCode {
	case 200:
		// fall to the XML parsing below
	case 404:
		return petitlyricsPayload{}, lookupHealthy, nil
	case 429:
		return petitlyricsPayload{}, lookupDegraded, p.rateLimitFailure("petitlyrics", "PetitLyrics", response)
	default:
		return petitlyricsPayload{}, lookupDegraded, providerResponseError("PetitLyrics", response)
	}

	var envelope petitlyricsEnvelope
	if err := xml.Unmarshal(response.Body, &envelope); err != nil {
		return petitlyricsPayload{}, lookupDegraded, fmt.Errorf("decode PetitLyrics response: %w", err)
	}
	if status := strings.TrimSpace(envelope.Status); status != "" && status != petitlyricsStatusOK {
		// Missing songs answer the success status with zero rows (verified
		// against the live API), so any other status is a provider failure:
		// degrade so the sweep retries instead of caching coverage.
		return petitlyricsPayload{}, lookupDegraded, fmt.Errorf("PetitLyrics status %s", status)
	}
	// Each row carries its own metadata: the first row is not automatically
	// the requested song, and a row of a different tier would be decoded with
	// the wrong parser. Accept only the row that answers this lookup.
	titleTokens := normalizedLyricTokens(track.Title)
	for _, song := range envelope.Songs.Songs {
		if rowTier := strings.TrimSpace(song.LyricsType); rowTier != "" && rowTier != tier {
			continue
		}
		if rowTitle := strings.TrimSpace(song.Title); rowTitle != "" && !lyricTokensMatch(normalizedLyricTokens(rowTitle), titleTokens) {
			continue
		}
		if rowArtist := strings.TrimSpace(song.Artist); rowArtist != "" && !artistTokensMatch(rowArtist, artist) {
			continue
		}
		return petitlyricsPayload{LyricsData: song.LyricsData}, lookupHealthy, nil
	}
	return petitlyricsPayload{}, lookupHealthy, nil
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

// appleLyricallyArtistMatches accepts any collaborating artist entry whose
// complete name matches the requested artist.
func appleLyricallyArtistMatches(artistName, wanted string) bool {
	return artistTokensMatch(artistName, wanted)
}

func petitLyricsHeaders() map[string]string {
	return map[string]string{
		"Accept":     "text/xml, application/xml",
		"User-Agent": pluginUserAgent(),
	}
}

const appleLyricallyEndpoint = "https://itunes.apple.com/search"
const lyricallyMirrorEndpoint = "https://lyrics.paxsenix.org/apple-music/lyrics"

// fetchLyricallyLyricsCandidates resolves Apple-sourced lyrics without any
// Apple credential: iTunes search picks the duration-tightest track, then the
// Lyrically mirror returns word-timed enhanced LRC, line TTML, or plain text.
func fetchLyricallyLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return nil, lookupHealthy, nil
	}

	searchURL := appleLyricallyEndpoint + "?" + url.Values{
		"term":   {title + " " + artist},
		"media":  {"music"},
		"entity": {"song"},
		"limit":  {"5"},
	}.Encode()
	response, err := p.sendRequest("Apple search", searchURL, defaultHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		// Apple throttles too; persist the cooldown so repeated lookups stop
		// contacting the throttled endpoint.
		return nil, lookupDegraded, p.rateLimitFailure("apple-lyrically", "Apple search", response)
	default:
		return nil, lookupDegraded, providerResponseError("Apple search", response)
	}

	var search struct {
		Results []struct {
			TrackID         int64  `json:"trackId"`
			TrackName       string `json:"trackName"`
			ArtistName      string `json:"artistName"`
			TrackTimeMillis int64  `json:"trackTimeMillis"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response.Body, &search); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Apple search response: %w", err)
	}
	target := int64(math.Round(float64(track.Duration) * 1000.0))
	titleTokens := normalizedLyricTokens(track.Title)
	var trackID int64
	bestDelta := int64(math.MaxInt64)
	found := false
	for _, result := range search.Results {
		if result.TrackID <= 0 {
			continue
		}
		// Duration alone does not identify a recording: covers and live
		// versions share runtimes, so the returned identity gates first and
		// the duration ranks the survivors.
		if !lyricTokensMatch(normalizedLyricTokens(result.TrackName), titleTokens) {
			continue
		}
		if !appleLyricallyArtistMatches(result.ArtistName, trackArtist(track)) {
			continue
		}
		if track.Duration > 0 {
			// The duration gate applies only when the request carries one.
			delta := int64(math.Abs(float64(result.TrackTimeMillis - target)))
			if delta > 2000 || delta >= bestDelta {
				continue
			}
			trackID, bestDelta, found = result.TrackID, delta, true
			continue
		}
		if !found {
			trackID, bestDelta, found = result.TrackID, 0, true
		}
	}
	if !found {
		return nil, lookupHealthy, nil
	}

	mirrorURL := lyricallyMirrorEndpoint + "?id=" + strconv.FormatInt(trackID, 10)
	response, err = p.sendRequest("Lyrically lyrics", mirrorURL, defaultHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		return nil, lookupDegraded, p.rateLimitFailure("apple-lyrically", "Lyrically lyrics", response)
	default:
		return nil, lookupDegraded, providerResponseError("Lyrically lyrics", response)
	}

	var mirror struct {
		ELRC        string `json:"elrc"`
		TTMLContent string `json:"ttmlContent"`
		Plain       string `json:"plain"`
	}
	if err := json.Unmarshal(response.Body, &mirror); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Lyrically lyrics response: %w", err)
	}

	if elrc := strings.TrimSpace(mirror.ELRC); elrc != "" {
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: elrc}, "Apple Music (Lyrically)", "elrc")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	if ttml := strings.TrimSpace(mirror.TTMLContent); ttml != "" {
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: ttml}, "Apple Music (Lyrically)", "ttml")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	if plain := strings.TrimSpace(mirror.Plain); plain != "" {
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: plain}, "Apple Music (Lyrically)", "plain")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	return nil, lookupHealthy, nil
}
