package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const qqEndpoint = "https://lyrics-api.boidu.dev/qq/getLyrics"

// fetchPortatoLyricsCandidates queries the Better Lyrics QQ endpoint for
// word-synced QRC lyrics, walking the same specificity tiers as the TTML source.
// Results come back as enhanced LRC so Navidrome sees word timing.
func fetchPortatoLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	headers := p.betterLyricsHeaders()

	health := lookupHealthy
	retryBroader := true
	var lookupErrors []error
	var found *lyricsCandidate
	for i, requestURL := range uniqueRequestURLs(
		mustRequestURL(buildBetterLyricsProviderRequestURLWithMetadata(qqEndpoint, track, true, true)),
		mustRequestURL(buildBetterLyricsProviderRequestURLWithMetadata(qqEndpoint, track, false, true)),
		mustRequestURL(buildBetterLyricsProviderRequestURLWithMetadata(qqEndpoint, track, false, false)),
	) {
		if i > 0 && !retryBroader {
			break
		}
		text, retry, uncertain, err := p.fetchQQQRC(requestURL, headers, deadline)
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

		lines, parseErr := parseKFamilyLyrics(wordSyncQRC, text)
		if parseErr != nil {
			lookupErrors = append(lookupErrors, fmt.Errorf("better lyrics QQ API returned unparseable QRC"))
			health = health.merge(lookupDegraded)
			continue
		}
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}, "Better Lyrics Portato", "elrc")
		if !candidate.present() {
			lookupErrors = append(lookupErrors, fmt.Errorf("better lyrics QQ API returned no usable timed lines"))
			health = health.merge(lookupDegraded)
			continue
		}
		found = &candidate
		break
	}
	if found != nil {
		return []lyricsCandidate{*found}, health, nil
	}
	if len(lookupErrors) > 0 {
		return nil, health, errors.Join(lookupErrors...)
	}
	return nil, health, nil
}

func (p *lyricsProvider) fetchQQQRC(requestURL string, headers map[string]string, deadline time.Time) (string, bool, bool, error) {
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
		return "", false, false, fmt.Errorf("request better lyrics QQ API: %w", err)
	}
	if response == nil {
		return "", false, false, fmt.Errorf("better lyrics QQ API returned no response")
	}

	switch response.StatusCode {
	case 200:
		var payload providerAPIResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return "", false, false, fmt.Errorf("decode better lyrics QQ API response: %w", err)
		}
		if strings.TrimSpace(payload.Lyrics) == "" {
			return "", true, false, nil
		}
		return payload.Lyrics, false, false, nil
	case 401:
		_, keyed := headers["X-API-Key"]
		return "", true, !keyed, nil
	case 404:
		return "", true, false, nil
	case 429:
		retryAfter := p.rememberRateLimit(response, requestURL)
		return "", !globalRateLimit(response), false, rateLimitResponseError("better lyrics QQ API", response, retryAfter)
	default:
		return "", false, false, providerResponseError("better lyrics QQ API", response)
	}
}
