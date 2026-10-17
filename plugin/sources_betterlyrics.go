package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const apiEndpoint = "https://lyrics-api.boidu.dev/getLyrics"

type apiResponse struct {
	TTML string `json:"ttml"`
}

// fetchBetterLyricsLyrics walks the Better Lyrics TTML endpoint through its
// documented specificity tiers: full metadata, then no album, then title+artist.
func fetchBetterLyricsLyrics(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, lookupHealth, error) {
	headers := p.betterLyricsHeaders()

	health := lookupHealthy
	retryBroader := true
	var lookupErrors []error
	var found *lyric.Candidate
	for i, requestURL := range uniqueRequestURLs(
		mustRequestURL(lyricsRequestURL(track)),
		mustRequestURL(lyricsRequestURLWithoutAlbum(track)),
		mustRequestURL(lyricsRequestURLWithoutOptionalMetadata(track)),
	) {
		if i > 0 && !retryBroader {
			break
		}
		text, retry, uncertain, err := p.fetchBetterLyricsTTML(requestURL, headers, deadline)
		retryBroader = retry
		if uncertain {
			health = health.merge(lookupUncertain)
		}
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			health = health.merge(lookupDegraded)
			continue
		}
		if text == "" {
			continue
		}

		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: text}, "Better Lyrics", "ttml")
		if !candidate.Present() {
			lookupErrors = append(lookupErrors, fmt.Errorf("better lyrics API returned malformed TTML"))
			health = health.merge(lookupDegraded)
			continue
		}
		found = &candidate
		break
	}
	if found != nil {
		return []lyric.Candidate{*found}, health, nil
	}
	if len(lookupErrors) > 0 {
		return nil, health, errors.Join(lookupErrors...)
	}
	return nil, health, nil
}

func (p *lyricsProvider) betterLyricsHeaders() map[string]string {
	headers := defaultHeaders()
	if apiKey := readProviderConfig(p.configLookup).BetterLyricsAPIKey; apiKey != "" {
		headers["X-API-Key"] = apiKey
	}
	return headers
}

func (p *lyricsProvider) fetchBetterLyricsTTML(requestURL string, headers map[string]string, deadline time.Time) (string, bool, bool, error) {
	if cooldownErr := p.activeBetterLyricsCooldown(requestURL); cooldownErr != nil {
		return "", !isGlobalRateLimitError(cooldownErr), false, cooldownErr
	}
	timeout, err := p.requestTimeoutMilliseconds(deadline)
	if err != nil {
		return "", false, false, err
	}
	response, err := p.send(host.HTTPRequest{
		Method:    "GET",
		URL:       requestURL,
		Headers:   headers,
		TimeoutMs: timeout,
	})
	if err != nil {
		return "", false, false, fmt.Errorf("request Better Lyrics API: %w", err)
	}
	if response == nil {
		return "", false, false, fmt.Errorf("better lyrics API returned no response")
	}

	switch response.StatusCode {
	case 200:
		var payload apiResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return "", false, false, fmt.Errorf("decode Better Lyrics API response: %w", err)
		}
		if strings.TrimSpace(payload.TTML) == "" {
			// An empty TTML payload is a clean miss for this query, so broader
			// metadata tiers may still find a usable cached entry.
			return "", true, false, nil
		}
		return payload.TTML, false, false, nil
	case 401:
		// Without an API key a 401 means the exact query is not available to an
		// unauthenticated cache-only lookup, which is uncertain rather than proof
		// that lyrics do not exist. A configured key changes the semantic to a
		// clean miss.
		_, keyed := headers["X-API-Key"]
		return "", true, !keyed, nil
	case 404:
		return "", true, false, nil
	case 429:
		retryAfter := p.rememberRateLimit(response, requestURL)
		return "", !globalRateLimit(response), false, rateLimitResponseError("better lyrics API", response, retryAfter)
	default:
		return "", false, false, apiResponseError(response)
	}
}

func lyricsRequestURL(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsProviderRequestURLWithMetadata(apiEndpoint, track, true, true)
}

func lyricsRequestURLWithoutAlbum(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsProviderRequestURLWithMetadata(apiEndpoint, track, false, true)
}

func lyricsRequestURLWithoutOptionalMetadata(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsProviderRequestURLWithMetadata(apiEndpoint, track, false, false)
}

func buildBetterLyricsProviderRequestURLWithMetadata(endpoint string, track lyrics.TrackInfo, includeAlbum, includeDuration bool) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := lyric.TrackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}

	query := url.Values{
		"a": {artist},
		"s": {title},
	}
	if includeAlbum {
		if album := strings.TrimSpace(track.Album); album != "" {
			query.Set("al", album)
		}
	}
	if includeDuration {
		if duration := int64(math.Round(float64(track.Duration))); duration > 0 {
			query.Set("d", strconv.FormatInt(duration, 10))
		}
	}

	return endpoint + "?" + query.Encode(), true
}

// uniqueRequestURLs collapses identical URLs so that requests to a tiered endpoint
// whose optional metadata is absent (or is a zero duration) stay at one request.
func uniqueRequestURLs(urls ...string) []string {
	seen := make(map[string]struct{}, len(urls))
	result := make([]string, 0, len(urls))
	for _, requestURL := range urls {
		if requestURL == "" {
			continue
		}
		if _, exists := seen[requestURL]; exists {
			continue
		}
		seen[requestURL] = struct{}{}
		result = append(result, requestURL)
	}
	return result
}

func mustRequestURL(requestURL string, ok bool) string {
	if !ok {
		return ""
	}
	return requestURL
}
