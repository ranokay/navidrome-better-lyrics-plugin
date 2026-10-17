package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const binimumEndpoint = "https://lyrics-api.binimum.org/"

// binimumSearchResults models the live search payload; the redirects (binimum →
// lrc.red) are followed by the host HTTP client, so lyricsUrl is enough here.
type binimumSearchResults struct {
	Results []binimumResult `json:"results"`
}

type binimumResult struct {
	TrackName  string  `json:"track_name"`
	ArtistName string  `json:"artist_name"`
	AlbumName  string  `json:"album_name"`
	Duration   float64 `json:"duration"`
	ISRC       string  `json:"isrc"`
	LyricsURL  string  `json:"lyricsUrl"`
	TimingType string  `json:"timing_type"`
}

// fetchBinimumLyricsCandidates resolves lyrics through BiniLyrics: a metadata
// search scoped by duration (≤2 s drift) followed by an Apple-shaped TTML fetch
// that Navidrome parses unchanged.
func fetchBinimumLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	searchURL, ok := binimumSearchURL(track)
	if !ok {
		return nil, lookupHealthy, nil
	}
	response, err := p.sendRequest("BiniLyrics search", searchURL, defaultHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}

	switch response.StatusCode {
	case 200:
		// fall through to the result selection below
	case 401, 404:
		// missing results are a clean miss
		return nil, lookupHealthy, nil
	case 429:
		return nil, lookupDegraded, p.rateLimitFailure("binimum", "BiniLyrics search", response)
	default:
		return nil, lookupDegraded, providerResponseError("BiniLyrics search", response)
	}

	var results binimumSearchResults
	if err := json.Unmarshal(response.Body, &results); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode BiniLyrics search response: %w", err)
	}

	best, found := pickBinimumResult(results.Results, track)
	if !found {
		return nil, lookupHealthy, nil
	}

	lyricsResponse, err := p.sendRequest("BiniLyrics lyrics", best.LyricsURL, defaultHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch lyricsResponse.StatusCode {
	case 200:
		// pass through below
	case 401, 404:
		return nil, lookupHealthy, nil
	case 429:
		return nil, lookupDegraded, p.rateLimitFailure("binimum", "BiniLyrics lyrics", lyricsResponse)
	default:
		return nil, lookupDegraded, providerResponseError("BiniLyrics lyrics", lyricsResponse)
	}

	if strings.TrimSpace(string(lyricsResponse.Body)) == "" {
		return nil, lookupHealthy, nil
	}
	text := string(lyricsResponse.Body)
	candidate := newLyricsCandidate(lyrics.LyricsText{Text: text}, "BiniLyrics", "ttml")
	if !candidate.present() {
		if emptyLyricsDocument(text, "ttml") {
			return nil, lookupHealthy, nil
		}
		return nil, lookupDegraded, fmt.Errorf("BiniLyrics lyrics returned malformed TTML")
	}
	return []lyricsCandidate{candidate}, lookupHealthy, nil
}

func pickBinimumResult(results []binimumResult, track lyrics.TrackInfo) (binimumResult, bool) {
	target := int64(math.Round(float64(track.Duration)))
	title := normalizedLyricTokens(track.Title)
	var best binimumResult
	bestDelta := int64(math.MaxInt64)
	found := false
	for _, candidateResult := range results {
		if !lyricTokensMatch(normalizedLyricTokens(candidateResult.TrackName), title) ||
			!artistTokensMatch(candidateResult.ArtistName, trackArtist(track)) {
			continue
		}
		// Provider-supplied URLs are untrusted: only the known lyrics hosts
		// may be fetched through the server's HTTP client (SSRF).
		if !binimumLyricsURLAllowed(candidateResult.LyricsURL) {
			continue
		}
		if track.Duration > 0 {
			// The duration gate applies only when the request carries one:
			// durationless library entries still match on identity.
			delta := int64(math.Abs(float64(int64(math.Round(candidateResult.Duration)) - target)))
			if delta > 2 {
				continue
			}
			if delta < bestDelta {
				best, bestDelta, found = candidateResult, delta, true
			}
			continue
		}
		if !found {
			best, bestDelta, found = candidateResult, 0, true
		}
	}
	return best, found
}

// binimumLyricsURLAllowed gates provider-supplied lyrics URLs to the two
// hosts the service actually serves files from, requiring HTTPS so a hostile
// or compromised search response cannot redirect the server-side fetch at
// arbitrary endpoints.
func binimumLyricsURLAllowed(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "lrc.red", "lyrics-api.binimum.org":
		return true
	}
	return false
}

func binimumSearchURL(track lyrics.TrackInfo) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}
	query := url.Values{
		"track":    {title},
		"artist":   {artist},
		"duration": {strconv.FormatInt(int64(math.Round(float64(track.Duration))), 10)},
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		query.Set("album", album)
	}
	return binimumEndpoint + "?" + query.Encode(), true
}
