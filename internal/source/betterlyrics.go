package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const apiEndpoint = "https://lyrics-api.boidu.dev/getLyrics"

type apiResponse struct {
	TTML string `json:"ttml"`
}

// fetchBetterLyrics walks the Better Lyrics TTML endpoint through its
// documented specificity tiers: full metadata, then no album, then title+artist.
func fetchBetterLyrics(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	headers := betterLyricsHeaders(rt)

	health := Healthy
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
		text, retry, uncertain, err := fetchBetterLyricsTTML(rt, requestURL, headers, deadline)
		retryBroader = retry
		if uncertain {
			health = health.Merge(Uncertain)
		}
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			health = health.Merge(Degraded)
			continue
		}
		if text == "" {
			continue
		}

		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: text}, "Better Lyrics", "ttml")
		if !candidate.Present() {
			lookupErrors = append(lookupErrors, fmt.Errorf("better lyrics API returned malformed TTML"))
			health = health.Merge(Degraded)
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

func betterLyricsHeaders(rt Runtime) map[string]string {
	headers := rt.DefaultHeaders()
	if apiKey := rt.BetterLyricsAPIKey(); apiKey != "" {
		headers["X-API-Key"] = apiKey
	}
	return headers
}

func fetchBetterLyricsTTML(rt Runtime, requestURL string, headers map[string]string, deadline time.Time) (string, bool, bool, error) {
	if cooldownErr := activeBetterLyricsCooldown(rt, requestURL); cooldownErr != nil {
		return "", !IsGlobalRateLimitError(cooldownErr), false, cooldownErr
	}
	response, err := rt.Do("Better Lyrics API", "GET", requestURL, nil, headers, deadline)
	if err != nil {
		return "", false, false, err
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
		retryAfter := rememberRateLimit(rt, response, requestURL)
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
