package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const neteaseEndpoint = "https://music.163.com"

// fetchNetEaseLyricsCandidates resolves lyrics from NetEase Cloud Music's plain
// (unauthenticated) JSON API: form-POST search, then a lyric fetch. The word-synced
// YRC payload is preferred, then line LRC, then nothing (translations are skipped).
func fetchNetEaseLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	form := url.Values{
		"s":      {strings.TrimSpace(track.Title) + " " + trackArtist(track)},
		"type":   {"1"},
		"offset": {"0"},
		"limit":  {"10"},
	}
	searchURL := neteaseEndpoint + "/api/cloudsearch/pc"
	response, err := p.postForm("NetEase search", searchURL, form, neteaseHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the envelope decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("netease"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("NetEase search", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("NetEase search", response)
	}

	var search neteaseSearchEnvelope
	if err := json.Unmarshal(response.Body, &search); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode NetEase search response: %w", err)
	}
	// Classify the application-level status the HTTP layer already answered
	// 200 for: an envelope failure disguised as HTTP 200 must not become a
	// healthy miss that a sweep marks covered (Codex).
	if health, err := neteaseEnvelopeStatusFailure(p, search.Code); err != nil {
		return nil, health, err
	}
	song, found := pickNetEaseSong(search.Result.Songs, track)
	if !found {
		return nil, lookupHealthy, nil
	}

	lyricQuery := url.Values{
		"id": {strconv.FormatInt(song.ID, 10)},
		"lv": {"-1"}, "kv": {"-1"}, "tv": {"-1"}, "rv": {"-1"}, "yv": {"-1"},
	}
	lyricURL := neteaseEndpoint + "/api/song/lyric/v1?" + lyricQuery.Encode()
	lyricResponse, err := p.sendRequest("NetEase lyrics", lyricURL, neteaseHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch lyricResponse.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(lyricResponse.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("netease"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("NetEase lyrics", lyricResponse, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("NetEase lyrics", lyricResponse)
	}

	var lyricEnvelope neteaseLyricEnvelope
	if err := json.Unmarshal(lyricResponse.Body, &lyricEnvelope); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode NetEase lyric response: %w", err)
	}

	if yrc := strings.TrimSpace(lyricEnvelope.YRC.Lyric); yrc != "" {
		lines, parseErr := parseKFamilyLyrics(wordSyncYRC, stripNetEaseCreditLines(yrc))
		if parseErr == nil && len(lines) > 0 {
			candidate := newLyricsCandidate(lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}, "NetEase", "elrc")
			if candidate.present() {
				return []lyricsCandidate{candidate}, lookupHealthy, nil
			}
		}
	}
	if lrc := strings.TrimSpace(lyricEnvelope.LRC.Lyric); lrc != "" {
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: stripNetEaseCreditLines(lrc)}, "NetEase", "lrc")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	return nil, lookupHealthy, nil
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
// statuses degrade the lookup instead of reporting a healthy miss (Codex).
func neteaseEnvelopeStatusFailure(p *lyricsProvider, code int) (lookupHealth, error) {
	switch code {
	case 0, 200, 404:
		return lookupHealthy, nil
	case 429:
		p.rememberCooldown(sourceGlobalCooldownKey("netease"), defaultRateLimitCooldown)
		return lookupDegraded, &providerHTTPError{source: "NetEase cloudsearch envelope", status: 429, globalRateLimit: true}
	default:
		return lookupDegraded, &providerHTTPError{source: "NetEase cloudsearch envelope", status: int32(code)}
	}
}

func pickNetEaseSong(songs []neteaseSong, track lyrics.TrackInfo) (neteaseSong, bool) {
	target := int64(float64(track.Duration)*1000.0 + 0.5)
	title, artist := normalizedLyricTokens(track.Title), normalizedLyricTokens(trackArtist(track))
	for _, song := range songs {
		if song.ID <= 0 {
			continue
		}
		// Require a normalized metadata match before the duration gate: the
		// fuzzy cloud search can return unrelated rows with similar durations,
		// and their lyrics would then outrank correct candidates (Codex).
		if !lyricTokensMatch(normalizedLyricTokens(song.Name), title) {
			continue
		}
		if !neteaseArtistsMatch(song.Artists, artist) {
			continue
		}
		if delta := song.Duration - target; delta < 2000 && delta > -2000 {
			return song, true
		}
	}
	return neteaseSong{}, false
}

// neteaseArtistsMatch reports whether any returned artist name matches the
// requested artist tokens: the cloudsearch payload can carry collaborating
// artists, so one matching row suffices.
func neteaseArtistsMatch(song []neteaseArtist, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, candidateArtist := range song {
		if lyricTokensMatch(normalizedLyricTokens(candidateArtist.Name), wanted) {
			return true
		}
	}
	return false
}

type neteaseLyricEnvelope struct {
	LRC neteaseLyricEntry `json:"lrc"`
	YRC neteaseLyricEntry `json:"yrc"`
}

type neteaseLyricEntry struct {
	Lyric string `json:"lyric"`
}

func neteaseHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}
}
