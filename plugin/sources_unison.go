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

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const unisonEndpoint = "https://unison.boidu.dev/lyrics"

type unisonAPIResponse struct {
	Success bool               `json:"success"`
	Data    *unisonLyricsEntry `json:"data"`
}

type unisonLyricsEntry struct {
	Lyrics   string `json:"lyrics"`
	Format   string `json:"format"`
	Language string `json:"language"`
}

// fetchUnisonLyrics walks the community database through its explicitly
// documented specificity tiers.
func fetchUnisonCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	headers := defaultHeaders()

	health := lookupHealthy
	retryBroader := true
	var lookupErrors []error
	var found *lyricsCandidate
	for i, requestURL := range uniqueRequestURLs(
		mustRequestURL(unisonRequestURLWithFullMetadata(track)),
		mustRequestURL(unisonRequestURL(track)),
		mustRequestURL(unisonRequestURLWithoutOptionalMetadata(track)),
	) {
		if i > 0 && !retryBroader {
			break
		}
		unisonLyrics, unisonFormat, retry, err := p.fetchUnisonLyrics(requestURL, headers, deadline)
		retryBroader = retry
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			health = health.merge(lookupDegraded)
			continue
		}
		if unisonLyrics.Text == "" {
			continue
		}

		candidate := newLyricsCandidate(unisonLyrics, "Unison", unisonFormat)
		if !candidate.present() {
			lookupErrors = append(lookupErrors, fmt.Errorf("unison API returned malformed %s lyrics", strings.ToUpper(unisonFormat)))
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

func (p *lyricsProvider) fetchUnisonLyrics(requestURL string, headers map[string]string, deadline time.Time) (lyrics.LyricsText, string, bool, error) {
	if cooldownErr := p.activeUnisonCooldown(); cooldownErr != nil {
		return lyrics.LyricsText{}, "", false, cooldownErr
	}
	response, err := p.sendRequest("Unison API", requestURL, headers, deadline)
	if err != nil {
		return lyrics.LyricsText{}, "", false, err
	}

	switch response.StatusCode {
	case 200:
		var payload unisonAPIResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return lyrics.LyricsText{}, "", false, fmt.Errorf("decode Unison API response: %w", err)
		}
		if !payload.Success || payload.Data == nil {
			return lyrics.LyricsText{}, "", false, fmt.Errorf("decode Unison API response: missing successful lyrics data")
		}
		if strings.TrimSpace(payload.Data.Lyrics) == "" {
			return lyrics.LyricsText{}, "", true, nil
		}

		format := strings.ToLower(strings.TrimSpace(payload.Data.Format))
		switch format {
		case "ttml", "lrc", "plain":
			return lyrics.LyricsText{
				Lang: strings.TrimSpace(payload.Data.Language),
				Text: payload.Data.Lyrics,
			}, format, false, nil
		default:
			return lyrics.LyricsText{}, "", false, fmt.Errorf("unsupported Unison lyrics format %q", payload.Data.Format)
		}
	case 404:
		return lyrics.LyricsText{}, "", true, nil
	case 429:
		retryAfter := p.rememberUnisonRateLimit(response)
		return lyrics.LyricsText{}, "", false, providerRateLimitResponseError("Unison API", response, retryAfter)
	default:
		return lyrics.LyricsText{}, "", false, providerResponseError("Unison API", response)
	}
}

// unisonRequestURL preserves the pre-existing duration-only query shape. The
// provider first tries unisonRequestURLWithFullMetadata, then this broader form.
func unisonRequestURL(track lyrics.TrackInfo) (string, bool) {
	return buildUnisonRequestURL(track, false, true)
}

func unisonRequestURLWithFullMetadata(track lyrics.TrackInfo) (string, bool) {
	return buildUnisonRequestURL(track, true, true)
}

func unisonRequestURLWithoutOptionalMetadata(track lyrics.TrackInfo) (string, bool) {
	return buildUnisonRequestURL(track, false, false)
}

func buildUnisonRequestURL(track lyrics.TrackInfo, includeAlbum, includeDuration bool) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}

	query := url.Values{
		"artist": {artist},
		"song":   {title},
	}
	if includeAlbum {
		if album := strings.TrimSpace(track.Album); album != "" {
			query.Set("album", album)
		}
	}
	if includeDuration {
		if duration := int64(math.Round(float64(track.Duration))); duration > 0 {
			query.Set("duration", strconv.FormatInt(duration, 10))
		}
	}
	return unisonEndpoint + "?" + query.Encode(), true
}
