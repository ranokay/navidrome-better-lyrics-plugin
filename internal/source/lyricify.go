package source

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const (
	lyricifyResolveEndpoint = "https://api.credits.fm/v1/resolve/track"
	lyricifyMobileLyricsURL = "https://api.lyricify.app/lyrics/get/mobile/new"
)

// fetchLyricify resolves lyrics via Lyricify's mobile endpoint:
// titles/artists first resolve to an ISRC through credits.fm, the fetch then uses
// a throwaway random username (no account exists server-side).
func fetchLyricify(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	title := strings.TrimSpace(track.Title)
	artist := lyric.TrackArtist(track)
	if title == "" || artist == "" {
		return nil, Healthy, nil
	}

	payload, err := json.Marshal(struct {
		Name   string `json:"name"`
		Artist string `json:"artist"`
	}{title, artist})
	if err != nil {
		return nil, Degraded, err
	}
	response, err := rt.Do("Lyricify resolve", "POST", lyricifyResolveEndpoint, payload, lyricifyHeaders(), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, Healthy, nil
	case 429:
		return nil, Degraded, rateLimitFailure(rt, "lyricify", "Lyricify resolve", response)
	default:
		return nil, Degraded, providerResponseError("Lyricify resolve", response)
	}

	var resolve struct {
		ISRC string `json:"isrc"`
	}
	if err := json.Unmarshal(response.Body, &resolve); err != nil {
		return nil, Degraded, fmt.Errorf("decode Lyricify resolve response: %w", err)
	}
	isrc := strings.TrimSpace(resolve.ISRC)
	if isrc == "" {
		return nil, Healthy, nil
	}

	// The mobile endpoint wants the ISRC base64-encoded in the query string.
	encoded := base64URLEncode(isrc)
	query := url.Values{
		"username": {randomLyricifyUsername()},
		"isrc":     {encoded},
	}
	response, err = get(rt, "Lyricify lyrics", lyricifyMobileLyricsURL+"?"+query.Encode(), lyricifyAcceptGzipHeaders(), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 403, 404:
		// A missing ISRC comes back as 403, not 404; both are clean misses here.
		return nil, Healthy, nil
	case 429:
		return nil, Degraded, rateLimitFailure(rt, "lyricify", "Lyricify lyrics", response)
	default:
		return nil, Degraded, providerResponseError("Lyricify lyrics", response)
	}

	var payloadEnvelope struct {
		Text           string `json:"text"`
		Offset         int64  `json:"offset"`
		IsInstrumental bool   `json:"isInstrumental"`
	}
	if err := json.Unmarshal(response.Body, &payloadEnvelope); err != nil {
		return nil, Degraded, fmt.Errorf("decode Lyricify lyrics response: %w", err)
	}
	if payloadEnvelope.IsInstrumental {
		return nil, Healthy, nil
	}
	text := strings.TrimSpace(payloadEnvelope.Text)
	if text == "" {
		return nil, Healthy, nil
	}

	lines, parseErr := lyric.ParseLyricifyLYL(text)
	if parseErr != nil {
		// The endpoint returned payload that is not parseable LYL: a provider
		// failure must stay retryable instead of becoming cached coverage.
		return nil, Degraded, fmt.Errorf("decode Lyricify lyrics: %w", parseErr)
	}
	if len(lines) == 0 {
		return nil, Healthy, nil
	}
	lines = applyLyricsOffset(lines, payloadEnvelope.Offset)
	candidate := lyric.NewCandidate(lyrics.LyricsText{Text: lyric.EnhancedLRC(lines)}, "Lyricify", "elrc")
	if !candidate.Present() {
		return nil, Healthy, nil
	}
	return []lyric.Candidate{candidate}, Healthy, nil
}

// applyLyricsOffset shifts parsed lines by the file's millisecond offset.
func applyLyricsOffset(lines []lyric.TimedLine, offsetMs int64) []lyric.TimedLine {
	if offsetMs == 0 {
		return lines
	}
	shifted := make([]lyric.TimedLine, 0, len(lines))
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
