package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const (
	lyricifyResolveEndpoint = "https://api.credits.fm/v1/resolve/track"
	lyricifyMobileLyricsURL = "https://api.lyricify.app/lyrics/get/mobile/new"
)

// fetchLyricifyLyricsCandidates resolves lyrics via Lyricify's mobile endpoint:
// titles/artists first resolve to an ISRC through credits.fm, the fetch then uses
// a throwaway random username (no account exists server-side).
func fetchLyricifyLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return nil, lookupHealthy, nil
	}

	payload, err := json.Marshal(struct {
		Name   string `json:"name"`
		Artist string `json:"artist"`
	}{title, artist})
	if err != nil {
		return nil, lookupDegraded, err
	}
	response, err := p.sendRequestHelper("Lyricify resolve", "POST", lyricifyResolveEndpoint, payload, lyricifyHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("lyricify"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Lyricify resolve", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Lyricify resolve", response)
	}

	var resolve struct {
		ISRC string `json:"isrc"`
	}
	if err := json.Unmarshal(response.Body, &resolve); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Lyricify resolve response: %w", err)
	}
	isrc := strings.TrimSpace(resolve.ISRC)
	if isrc == "" {
		return nil, lookupHealthy, nil
	}

	// The mobile endpoint wants the ISRC base64-encoded in the query string.
	encoded := base64URLEncode(isrc)
	query := url.Values{
		"username": {randomLyricifyUsername()},
		"isrc":     {encoded},
	}
	response, err = p.sendRequest("Lyricify lyrics", lyricifyMobileLyricsURL+"?"+query.Encode(), lyricifyAcceptGzipHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 403, 404:
		// A missing ISRC comes back as 403, not 404; both are clean misses here.
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("lyricify"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Lyricify lyrics", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Lyricify lyrics", response)
	}

	var payloadEnvelope struct {
		Text           string `json:"text"`
		Offset         int64  `json:"offset"`
		IsInstrumental bool   `json:"isInstrumental"`
	}
	if err := json.Unmarshal(response.Body, &payloadEnvelope); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Lyricify lyrics response: %w", err)
	}
	if payloadEnvelope.IsInstrumental {
		return nil, lookupHealthy, nil
	}
	text := strings.TrimSpace(payloadEnvelope.Text)
	if text == "" {
		return nil, lookupHealthy, nil
	}

	lines, parseErr := parseLyricifyLYL(text)
	if parseErr != nil {
		// The endpoint returned payload that is not parseable LYL: a provider
		// failure must stay retryable instead of becoming cached coverage
		// (Codex).
		return nil, lookupDegraded, fmt.Errorf("decode Lyricify lyrics: %w", parseErr)
	}
	if len(lines) == 0 {
		return nil, lookupHealthy, nil
	}
	lines = applyLyricsOffset(lines, payloadEnvelope.Offset)
	candidate := newLyricsCandidate(lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}, "Lyricify", "elrc")
	if !candidate.present() {
		return nil, lookupHealthy, nil
	}
	return []lyricsCandidate{candidate}, lookupHealthy, nil
}

// applyLyricsOffset shifts parsed lines by the file's millisecond offset.
func applyLyricsOffset(lines []timedLine, offsetMs int64) []timedLine {
	if offsetMs == 0 {
		return lines
	}
	shifted := make([]timedLine, 0, len(lines))
	for _, line := range lines {
		line.StartMs += offsetMs
		line.EndMs += offsetMs
		for k := range line.Words {
			line.Words[k].StartMs += offsetMs
			line.Words[k].EndMs += offsetMs
		}
		shifted = append(shifted, line)
	}
	return shifted
}

func base64URLEncode(value string) string {
	return base64.RawStdEncoding.EncodeToString([]byte(value))
}

// randomLyricifyUsername imitates the app's throwaway username (5-12 lowercase).
func randomLyricifyUsername() string {
	raw := make([]byte, 9)
	_, _ = rand.Read(raw)
	for k := range raw {
		raw[k] = 'a' + raw[k]%26
	}
	return string(raw)
}

func lyricifyHeaders() map[string]string {
	return map[string]string{
		"Accept":          "application/json",
		"Content-Type":    "application/json",
		"Accept-Encoding": "identity",
		"User-Agent":      desktopChromeUserAgent,
	}
}

func lyricifyAcceptGzipHeaders() map[string]string {
	return map[string]string{
		"Accept": "application/json",
		// Deliberately no Accept-Encoding here: the host client disables
		// transparent decompression when the request sets it, and nothing in the
		// module unpacks bodies, so a gzip answer would fail every decode.
		"User-Agent": desktopChromeUserAgent,
	}
}

const desktopChromeUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
