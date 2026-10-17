package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const qqEndpoint = "https://lyrics-api.boidu.dev/qq/getLyrics"

// portatoQRCContent extracts the QRC body from the QQ XML envelope: the live
// payload wraps the bracket rows in a LyricContent attribute, so parsing the
// raw document would leave XML entities escaped and lose rows whose line
// breaks were entity-encoded. Raw (non-XML) QRC responses pass through
// unchanged; a malformed QrcInfos shell fails loudly so the lookup degrades
// instead of parsing XML junk.
func portatoQRCContent(document string) (string, error) {
	if !strings.Contains(document, "QrcInfos") {
		return document, nil
	}
	var shell struct {
		LyricInfos []struct {
			Lyrics []struct {
				LyricContent string `xml:"LyricContent,attr"`
			} `xml:",any"`
		} `xml:",any"`
	}
	if err := xml.Unmarshal([]byte(document), &shell); err != nil {
		return "", fmt.Errorf("decode QQ QRC envelope: %w", err)
	}
	for _, info := range shell.LyricInfos {
		for _, lyric := range info.Lyrics {
			if content := strings.TrimSpace(lyric.LyricContent); content != "" {
				return content, nil
			}
		}
	}
	return "", errors.New("QQ QRC envelope carries no LyricContent")
}

// fetchPortatoLyricsCandidates queries the Better Lyrics QQ endpoint for
// word-synced QRC lyrics, walking the same specificity tiers as the TTML source.
// Results come back as enhanced LRC so Navidrome sees word timing.
func fetchPortatoLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, lookupHealth, error) {
	headers := p.betterLyricsHeaders()

	health := lookupHealthy
	retryBroader := true
	var lookupErrors []error
	var found *lyric.Candidate
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

		qrc, extractErr := portatoQRCContent(text)
		if extractErr != nil {
			lookupErrors = append(lookupErrors, fmt.Errorf("better lyrics QQ API returned a bad QRC envelope"))
			health = health.merge(lookupDegraded)
			continue
		}
		lines, parseErr := lyric.ParseKFamily(lyric.QRC, qrc)
		if parseErr != nil {
			lookupErrors = append(lookupErrors, fmt.Errorf("better lyrics QQ API returned unparseable QRC"))
			health = health.merge(lookupDegraded)
			continue
		}
		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: lyric.EnhancedLRC(lines)}, "Better Lyrics Portato", "elrc")
		if !candidate.Present() {
			lookupErrors = append(lookupErrors, fmt.Errorf("better lyrics QQ API returned no usable timed lines"))
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
