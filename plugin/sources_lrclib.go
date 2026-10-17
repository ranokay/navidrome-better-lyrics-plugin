package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const lrclibEndpoint = "https://lrclib.net"

// fetchLRCLibLyricsCandidates resolves lyrics from the LRCLib community API: the
// exact `get` endpoint first, then the `search` endpoint scoped to a duration drift
// of at most two seconds. `lyricsfile` (word timing), `syncedLyrics` (line), and
// `plainLyrics` map to enhanced LRC, line LRC, and plain text respectively.
func fetchLRCLibLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	getURL, ok := lrclibGetURL(track)
	if !ok {
		return nil, lookupHealthy, nil
	}

	response, health, err := p.fetchLRCLibEndpoint("LRCLib get", getURL, deadline)
	if err != nil {
		return nil, health, err
	}
	if len(response) > 0 {
		// The exact endpoint uses fuzzy metadata too: its reply is not
		// guaranteed to describe the requested recording, so the returned
		// identity must match before the record can win (Devin: an unrelated
		// same-shape record would otherwise become the cached winner).
		matched, gateErr := lrclibRecordMatches(response, track)
		if gateErr != nil {
			// A malformed 200 must stay a retryable provider failure, not
			// melt into a cacheable miss (Greptile).
			return nil, lookupDegraded, gateErr
		}
		if matched {
			candidate, candErr := lrclibCandidate(p, response)
			if candErr != nil && !errors.Is(candErr, errNoLRCLibPayload) {
				return nil, lookupDegraded, candErr
			}
			if candErr == nil && candidate.present() {
				return []lyricsCandidate{candidate}, health, nil
			}
		}
	}
	// An unusable exact match (instrumental, empty) is a miss rather than a
	// failure: the duration-scoped search still gets its chance below.
	// The exact lookup misses or the payload cannot be used; broaden by title and
	// artist, keeping the best duration match.
	searchQuery := url.Values{"track_name": {strings.TrimSpace(track.Title)}, "artist_name": {trackArtist(track)}}
	searchURL := lrclibEndpoint + "/api/search?" + searchQuery.Encode()
	searchBody, health, err := p.fetchLRCLibEndpoint("LRCLib search", searchURL, deadline)
	if err != nil {
		return nil, health, err
	}
	if len(searchBody) == 0 {
		// The search endpoint answered a miss (404) with no body: decoding nil
		// would degrade a clean miss (spotted while wiring the metadata gate).
		return nil, health, nil
	}

	var results []lrclibRecord
	if err := json.Unmarshal(searchBody, &results); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode LRCLib search response: %w", err)
	}
	target := int64(math.Round(float64(track.Duration)))
	var best lrclibRecord
	bestDelta := int64(math.MaxInt64)
	found := false
	for _, result := range results {
		// Fuzzy search rows only qualify when their identity matches; the
		// duration gate then ranks the qualifying candidates (Devin).
		if !lrclibRecordMetadataMatches(result, track) {
			continue
		}
		delta := int64(math.Abs(float64(int64(math.Round(result.Duration)) - target)))
		if delta > 2 {
			continue
		}
		if delta < bestDelta {
			best, bestDelta, found = result, delta, true
		}
	}
	if !found {
		return nil, lookupHealthy, nil
	}

	bestJSON, err := json.Marshal(best)
	if err != nil {
		return nil, lookupDegraded, fmt.Errorf("re-encode LRCLib match: %w", err)
	}
	matchCandidate, candErr := lrclibCandidate(p, bestJSON)
	if candErr != nil && !errors.Is(candErr, errNoLRCLibPayload) {
		return nil, lookupDegraded, candErr
	}
	if candErr != nil || !matchCandidate.present() {
		// The duration-matched record is unusable (instrumental, empty): a clean
		// miss, so later sources still get their turn.
		return nil, lookupHealthy, nil
	}
	return []lyricsCandidate{matchCandidate}, lookupHealthy, nil
}

// fetchLRCLibEndpoint performs one LRCLib HTTP call with the shared status and
// throttling handling.
func (p *lyricsProvider) fetchLRCLibEndpoint(source, requestURL string, deadline time.Time) ([]byte, lookupHealth, error) {
	response, err := p.sendRequest(source, requestURL, lrclibHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}

	switch response.StatusCode {
	case 200:
		return response.Body, lookupHealthy, nil
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("lrclib"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError(source, response, retryAfter)
	case 503:
		// LRCLib sheds load with a 503 + Retry-After; treat like a rate limit.
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("lrclib"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError(source, response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError(source, response)
	}
}

// lrclibCandidate turns an LRCLib record (JSON) into the best ranked candidate:
// lyricsfile words, then synced LRC, then plain text.
func lrclibCandidate(p *lyricsProvider, record []byte) (lyricsCandidate, error) {
	var payload struct {
		Instrumental bool   `json:"instrumental"`
		HasWordSync  bool   `json:"hasWordSync"`
		PlainLyrics  string `json:"plainLyrics"`
		SyncedLyrics string `json:"syncedLyrics"`
		LyricsFile   string `json:"lyricsfile"`
	}
	if err := json.Unmarshal(record, &payload); err != nil {
		return lyricsCandidate{}, fmt.Errorf("decode LRCLib record: %w", err)
	}
	if payload.Instrumental {
		return lyricsCandidate{}, errNoLRCLibPayload
	}

	if payload.HasWordSync && strings.TrimSpace(payload.LyricsFile) != "" {
		lines, err := parseLRCLibLyricsFile([]byte(payload.LyricsFile))
		if err == nil && len(lines) > 0 {
			text := lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}
			if candidate := newLyricsCandidate(text, "LRCLib", "elrc"); candidate.present() {
				return candidate, nil
			}
		}
	}
	if strings.TrimSpace(payload.SyncedLyrics) != "" {
		text := lyrics.LyricsText{Text: payload.SyncedLyrics}
		if candidate := newLyricsCandidate(text, "LRCLib", "lrc"); candidate.present() {
			return candidate, nil
		}
	}
	if strings.TrimSpace(payload.PlainLyrics) != "" {
		text := lyrics.LyricsText{Text: payload.PlainLyrics}
		if candidate := newLyricsCandidate(text, "LRCLib", "plain"); candidate.present() {
			return candidate, nil
		}
	}
	return lyricsCandidate{}, errNoLRCLibPayload
}

// lrclibGetURL builds the exact-match lookup; the album is optional but improves
// the cache key upstream.
func lrclibGetURL(track lyrics.TrackInfo) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}
	query := url.Values{
		"track_name":  {title},
		"artist_name": {artist},
		"duration":    {fmt.Sprintf("%d", int64(math.Round(float64(track.Duration))))},
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		query.Set("album_name", album)
	}
	return lrclibEndpoint + "/api/get?" + query.Encode(), true
}

// lrclibRecord keeps the fields used for scoring and re-serialization.
type lrclibRecord struct {
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	HasWordSync  bool    `json:"hasWordSync"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
	LyricsFile   string  `json:"lyricsfile"`
}

// lrclibRecordMatches gates a raw /api/get payload on the returned identity
// and duration before its lyrics may win. Decode failures return an error so
// broken replies stay retryable instead of becoming cached misses (Greptile).
func lrclibRecordMatches(record []byte, track lyrics.TrackInfo) (bool, error) {
	var payload struct {
		TrackName  string  `json:"trackName"`
		ArtistName string  `json:"artistName"`
		Duration   float64 `json:"duration"`
	}
	if err := json.Unmarshal(record, &payload); err != nil {
		return false, fmt.Errorf("decode LRCLib record: %w", err)
	}
	if !lrclibMetadataMatches(payload.TrackName, payload.ArtistName, track) {
		return false, nil
	}
	return lrclibDurationsClose(payload.Duration, track), nil
}

// lrclibRecordMetadataMatches is the search-row variant of the identity gate.
func lrclibRecordMetadataMatches(record lrclibRecord, track lyrics.TrackInfo) bool {
	return lrclibMetadataMatches(record.TrackName, record.ArtistName, track)
}

func lrclibMetadataMatches(trackName, artistName string, track lyrics.TrackInfo) bool {
	if strings.TrimSpace(trackName) == "" || strings.TrimSpace(artistName) == "" {
		// Records without identity data cannot be trusted to describe the
		// request; fall through to broader search instead.
		return false
	}
	return lyricTokensMatch(normalizedLyricTokens(trackName), normalizedLyricTokens(track.Title)) &&
		lrclibArtistMatches(artistName, track)
}

// lrclibArtistMatches accepts any of the record's collaborating artists when
// one matches the requested artist tokens.
func lrclibArtistMatches(artistName string, track lyrics.TrackInfo) bool {
	wanted := normalizedLyricTokens(trackArtist(track))
	if len(wanted) == 0 {
		return true
	}
	for _, name := range strings.Split(artistName, " & ") {
		if lyricTokensMatch(normalizedLyricTokens(name), wanted) {
			return true
		}
	}
	return false
}

// lrclibDurationsClose keeps the historical two-second tolerance.
func lrclibDurationsClose(recordSeconds float64, track lyrics.TrackInfo) bool {
	if track.Duration <= 0 {
		return true
	}
	delta := math.Abs(recordSeconds - float64(track.Duration))
	return delta <= 2
}

func lrclibHeaders() map[string]string {
	// LRCLib asks clients to identify themselves in the User-Agent and throttles
	// requests that do not, so the plugin UA is required, not decorative.
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}
}
