package source

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const neteaseEndpoint = "https://music.163.com"

// fetchNetEase resolves lyrics from NetEase Cloud Music's plain
// (unauthenticated) JSON API: form-POST search, then a lyric fetch. The word-synced
// YRC payload is preferred, then line LRC, then nothing (translations are skipped).
func fetchNetEase(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	form := url.Values{
		"s":      {strings.TrimSpace(track.Title) + " " + lyric.TrackArtist(track)},
		"type":   {"1"},
		"offset": {"0"},
		"limit":  {"10"},
	}
	searchURL := neteaseEndpoint + "/api/cloudsearch/pc"
	response, err := postForm(rt, "NetEase search", searchURL, form, rt.DefaultHeaders(), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the envelope decode
	case 404:
		return nil, Healthy, nil
	case 429:
		return nil, Degraded, rateLimitFailure(rt, "netease", "NetEase search", response)
	default:
		return nil, Degraded, providerResponseError("NetEase search", response)
	}

	var search neteaseSearchEnvelope
	if err := json.Unmarshal(response.Body, &search); err != nil {
		return nil, Degraded, fmt.Errorf("decode NetEase search response: %w", err)
	}
	// Classify the application-level status the HTTP layer already answered
	// 200 for: an envelope failure disguised as HTTP 200 must not become a
	// healthy miss that a sweep marks covered.
	if health, err := neteaseEnvelopeStatusFailure(rt, search.Code); err != nil {
		return nil, health, err
	}
	song, found := pickNetEaseSong(search.Result.Songs, track)
	if !found {
		return nil, Healthy, nil
	}

	lyricQuery := url.Values{
		"id": {strconv.FormatInt(song.ID, 10)},
		"lv": {"-1"}, "kv": {"-1"}, "tv": {"-1"}, "rv": {"-1"}, "yv": {"-1"},
	}
	lyricURL := neteaseEndpoint + "/api/song/lyric/v1?" + lyricQuery.Encode()
	lyricResponse, err := get(rt, "NetEase lyrics", lyricURL, rt.DefaultHeaders(), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	switch lyricResponse.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, Healthy, nil
	case 429:
		return nil, Degraded, rateLimitFailure(rt, "netease", "NetEase lyrics", lyricResponse)
	default:
		return nil, Degraded, providerResponseError("NetEase lyrics", lyricResponse)
	}

	var lyricEnvelope neteaseLyricEnvelope
	if err := json.Unmarshal(lyricResponse.Body, &lyricEnvelope); err != nil {
		return nil, Degraded, fmt.Errorf("decode NetEase lyric response: %w", err)
	}
	// The live lyric envelope also carries an application-level code; a
	// transient failure must not fall through as cacheable negative coverage.
	if health, err := neteaseEnvelopeStatusFailure(rt, lyricEnvelope.Code); err != nil {
		return nil, health, err
	}

	var yrcParseErr error
	if yrc := strings.TrimSpace(lyricEnvelope.YRC.Lyric); yrc != "" {
		lines, parseErr := lyric.ParseKFamily(lyric.YRC, stripNetEaseCreditLines(yrc))
		if parseErr == nil && len(lines) > 0 {
			candidate := lyric.NewCandidate(lyrics.LyricsText{Text: lyric.EnhancedLRC(lines)}, "NetEase", "elrc")
			if candidate.Present() {
				return []lyric.Candidate{candidate}, Healthy, nil
			}
		}
		yrcParseErr = parseErr
	}
	if lrc := strings.TrimSpace(lyricEnvelope.LRC.Lyric); lrc != "" {
		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: stripNetEaseCreditLines(lrc)}, "NetEase", "lrc")
		if candidate.Present() {
			return []lyric.Candidate{candidate}, Healthy, nil
		}
	}
	if yrcParseErr != nil {
		// Nonempty YRC that failed to parse with no usable LRC fallback is a
		// provider failure, not cacheable negative coverage.
		return nil, Degraded, fmt.Errorf("parse NetEase YRC payload: %w", yrcParseErr)
	}
	return nil, Healthy, nil
}

// stripNetEaseCreditLines drops the rich-JSON credit rows NetEase mixes into an
// otherwise vanilla LRC payload ({"t":…,"c":[…]}).
func stripNetEaseCreditLines(text string) string {
	var kept []string
	for _, row := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(row), "{") {
			continue
		}
		kept = append(kept, row)
	}
	return strings.Join(kept, "\n")
}

type neteaseSearchEnvelope struct {
	Code   int `json:"code"`
	Result struct {
		Songs []neteaseSong `json:"songs"`
	} `json:"result"`
}

type neteaseSong struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Artists  []neteaseArtist `json:"ar"` // live cloudsearch wire: `ar`
	Duration int64           `json:"dt"` // ms (live cloudsearch wire name)
}

type neteaseArtist struct {
	Name string `json:"name"`
}

// neteaseEnvelopeStatusFailure classifies the cloudsearch JSON code the HTTP
// layer already answered 200 for: 0/200 pass, no-match 404 keeps the quiet
// miss contract, 429 activates the provider cooldown, other non-success
// statuses degrade the lookup instead of reporting a healthy miss.
func neteaseEnvelopeStatusFailure(rt Runtime, code int) (Health, error) {
	switch code {
	case 0, 200, 404:
		return Healthy, nil
	case 429:
		rt.RememberCooldown(GlobalCooldownKey("netease"), defaultRateLimitCooldown)
		return Degraded, &providerHTTPError{source: "NetEase cloudsearch envelope", status: 429, globalRateLimit: true}
	default:
		return Degraded, &providerHTTPError{source: "NetEase cloudsearch envelope", status: int32(code)}
	}
}

func pickNetEaseSong(songs []neteaseSong, track lyrics.TrackInfo) (neteaseSong, bool) {
	target := int64(float64(track.Duration)*1000.0 + 0.5)
	title := lyric.NormalizedTokens(track.Title)
	for _, song := range songs {
		if song.ID <= 0 {
			continue
		}
		// Require a normalized metadata match before the duration gate: the
		// fuzzy cloud search can return unrelated rows with similar durations,
		// and their lyrics would then outrank correct candidates.
		if !lyric.TokensMatch(lyric.NormalizedTokens(song.Name), title) {
			continue
		}
		if !neteaseArtistsMatch(song.Artists, lyric.TrackArtist(track)) {
			continue
		}
		if track.Duration > 0 {
			// The duration gate applies only when the request carries one;
			// durationless entries still match on identity.
			delta := song.Duration - target
			if delta >= 2000 || delta <= -2000 {
				continue
			}
		}
		return song, true
	}
	return neteaseSong{}, false
}

// neteaseArtistsMatch reports whether any returned artist name matches the
// requested artist completely: the cloudsearch payload can carry collaborating
// artists, so one matching row suffices.
func neteaseArtistsMatch(song []neteaseArtist, wanted string) bool {
	if strings.TrimSpace(wanted) == "" {
		return true
	}
	for _, candidateArtist := range song {
		if lyric.ArtistMatch(candidateArtist.Name, wanted) {
			return true
		}
	}
	return false
}

type neteaseLyricEnvelope struct {
	Code int               `json:"code"`
	LRC  neteaseLyricEntry `json:"lrc"`
	YRC  neteaseLyricEntry `json:"yrc"`
}

type neteaseLyricEntry struct {
	Lyric string `json:"lyric"`
}
