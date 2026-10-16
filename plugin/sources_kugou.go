package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const kugouEndpoint = "https://lyrics-api.boidu.dev/kugou/getLyrics"

type providerAPIResponse struct {
	Lyrics string `json:"lyrics"`
}

// fetchKugouLyricsCandidates queries the Better Lyrics Kugou endpoint for a
// line-synchronized LRC fallback.
func fetchKugouLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	kugouURL, ok := kugouRequestURL(track)
	if !ok {
		return nil, lookupHealthy, nil
	}
	text, err := p.fetchKugouLyrics(kugouURL, p.betterLyricsHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	if strings.TrimSpace(text.Text) == "" {
		return nil, lookupHealthy, nil
	}
	return []lyricsCandidate{newLyricsCandidate(text, "Better Lyrics Legato", "lrc")}, lookupHealthy, nil
}

func (p *lyricsProvider) fetchKugouLyrics(requestURL string, headers map[string]string, deadline time.Time) (lyrics.LyricsText, error) {
	if cooldownErr := p.activeBetterLyricsCooldown(requestURL); cooldownErr != nil {
		return lyrics.LyricsText{}, cooldownErr
	}
	response, err := p.sendRequest("Better Lyrics Kugou API", requestURL, headers, deadline)
	if err != nil {
		return lyrics.LyricsText{}, err
	}

	switch response.StatusCode {
	case 200:
		var payload providerAPIResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return lyrics.LyricsText{}, fmt.Errorf("decode Better Lyrics Kugou API response: %w", err)
		}
		if strings.TrimSpace(payload.Lyrics) == "" {
			return lyrics.LyricsText{}, nil
		}
		return lyrics.LyricsText{Text: payload.Lyrics}, nil
	case 401, 404:
		return lyrics.LyricsText{}, nil
	case 429:
		retryAfter := p.rememberRateLimit(response, requestURL)
		return lyrics.LyricsText{}, rateLimitResponseError("Better Lyrics Kugou API", response, retryAfter)
	default:
		return lyrics.LyricsText{}, providerResponseError("Better Lyrics Kugou API", response)
	}
}

func kugouRequestURL(track lyrics.TrackInfo) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}
	query := url.Values{
		"a": {artist},
		"s": {title},
	}
	return kugouEndpoint + "?" + query.Encode(), true
}

// betterLyricsSharedHeaders is the common header set for endpoints served by the
// Better Lyrics API service.
func betterLyricsSharedHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}
}
