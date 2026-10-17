package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const lrclibEndpoint = "https://lrclib.net"

// errNoLRCLibPayload marks a record that decoded but carries no usable lyrics
// body in any of the supported shapes.
var errNoLRCLibPayload = errors.New("no usable LRCLib lyrics payload")

// fetchLRCLib resolves lyrics from the LRCLib community API: the
// exact `get` endpoint first, then the `search` endpoint scoped to a duration drift
// of at most two seconds. `lyricsfile` (word timing), `syncedLyrics` (line), and
// `plainLyrics` map to enhanced LRC, line LRC, and plain text respectively.
func fetchLRCLib(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	getURL, ok := lrclibGetURL(track)
	if !ok {
		return nil, Healthy, nil
	}

	response, health, err := fetchLRCLibEndpoint(rt, "LRCLib get", getURL, deadline)
	if err != nil {
		return nil, health, err
	}
	if len(response) > 0 {
		// The exact endpoint uses fuzzy metadata too: its reply is not
		// guaranteed to describe the requested recording, so the returned
		// identity must match before the record can win (an unrelated
		// same-shape record would otherwise become the cached winner).
		matched, gateErr := lrclibRecordMatches(response, track)
		if gateErr != nil {
			// A malformed 200 must stay a retryable provider failure, not
			// melt into a cacheable miss.
			return nil, Degraded, gateErr
		}
		if matched {
			candidate, candErr := lrclibCandidate(rt, response)
			if candErr != nil && !errors.Is(candErr, errNoLRCLibPayload) {
				return nil, Degraded, candErr
			}
			if candErr == nil && candidate.Present() {
				return []lyric.Candidate{candidate}, health, nil
			}
		}
	}
	// An unusable exact match (instrumental, empty) is a miss rather than a
	// failure: the duration-scoped search still gets its chance below.
	// The exact lookup misses or the payload cannot be used; broaden by title and
	// artist, keeping the best duration match.
	searchQuery := url.Values{"track_name": {strings.TrimSpace(track.Title)}, "artist_name": {lyric.TrackArtist(track)}}
	searchURL := lrclibEndpoint + "/api/search?" + searchQuery.Encode()
	searchBody, health, err := fetchLRCLibEndpoint(rt, "LRCLib search", searchURL, deadline)
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
		return nil, Degraded, fmt.Errorf("decode LRCLib search response: %w", err)
	}
	target := int64(math.Round(float64(track.Duration)))
	var best lrclibRecord
	bestDelta := int64(math.MaxInt64)
	found := false
	for _, result := range results {
		// Fuzzy search rows only qualify when their identity matches; the
		// duration gate then ranks the qualifying candidates, and
		// only applies when the request carries a duration.
		if !lrclibRecordMetadataMatches(result, track) {
			continue
		}
		delta := int64(0)
		if track.Duration > 0 {
			delta = int64(math.Abs(float64(int64(math.Round(result.Duration)) - target)))
			if delta > 2 {
				continue
			}
		}
		if delta < bestDelta || !found {
			best, bestDelta, found = result, delta, true
		}
	}
	if !found {
		return nil, Healthy, nil
	}

	bestJSON, err := json.Marshal(best)
	if err != nil {
		return nil, Degraded, fmt.Errorf("re-encode LRCLib match: %w", err)
	}
	matchCandidate, candErr := lrclibCandidate(rt, bestJSON)
	if candErr != nil && !errors.Is(candErr, errNoLRCLibPayload) {
		return nil, Degraded, candErr
	}
	if candErr != nil || !matchCandidate.Present() {
		// The duration-matched record is unusable (instrumental, empty): a clean
		// miss, so later sources still get their turn.
		return nil, Healthy, nil
	}
	return []lyric.Candidate{matchCandidate}, Healthy, nil
}

// fetchLRCLibEndpoint performs one LRCLib HTTP call with the shared status and
// throttling handling.
func fetchLRCLibEndpoint(rt Runtime, source, requestURL string, deadline time.Time) ([]byte, Health, error) {
	response, err := get(rt, source, requestURL, rt.DefaultHeaders(), deadline)
	if err != nil {
		return nil, Degraded, err
	}

	switch response.StatusCode {
	case 200:
		return response.Body, Healthy, nil
	case 404:
		return nil, Healthy, nil
	case 429:
		return nil, Degraded, rateLimitFailure(rt, "lrclib", source, response)
	case 503:
		// LRCLib sheds load with a 503 + Retry-After; treat like a rate limit.
		return nil, Degraded, rateLimitFailure(rt, "lrclib", source, response)
	default:
		return nil, Degraded, providerResponseError(source, response)
	}
}

// lrclibCandidate turns an LRCLib record (JSON) into the best ranked candidate:
// lyricsfile words, then synced LRC, then plain text.
func lrclibCandidate(rt Runtime, record []byte) (lyric.Candidate, error) {
	var payload struct {
		Instrumental bool   `json:"instrumental"`
		HasWordSync  bool   `json:"hasWordSync"`
		PlainLyrics  string `json:"plainLyrics"`
		SyncedLyrics string `json:"syncedLyrics"`
		LyricsFile   string `json:"lyricsfile"`
	}
	if err := json.Unmarshal(record, &payload); err != nil {
		return lyric.Candidate{}, fmt.Errorf("decode LRCLib record: %w", err)
	}
	if payload.Instrumental {
		return lyric.Candidate{}, errNoLRCLibPayload
	}

	var lyricsFileErr error
	if payload.HasWordSync && strings.TrimSpace(payload.LyricsFile) != "" {
		lines, err := lyric.ParseLRCLibLyricsFile([]byte(payload.LyricsFile))
		if err == nil && len(lines) > 0 {
			text := lyrics.LyricsText{Text: lyric.EnhancedLRC(lines)}
			if candidate := lyric.NewCandidate(text, "LRCLib", "elrc"); candidate.Present() {
				return candidate, nil
			}
		}
		// A malformed lyricsfile stays retryable when no fallback exists.
		lyricsFileErr = err
	}
	if strings.TrimSpace(payload.SyncedLyrics) != "" {
		text := lyrics.LyricsText{Text: payload.SyncedLyrics}
		if candidate := lyric.NewCandidate(text, "LRCLib", "lrc"); candidate.Present() {
			return candidate, nil
		}
	}
	if strings.TrimSpace(payload.PlainLyrics) != "" {
		text := lyrics.LyricsText{Text: payload.PlainLyrics}
		if candidate := lyric.NewCandidate(text, "LRCLib", "plain"); candidate.Present() {
			return candidate, nil
		}
	}
	if lyricsFileErr != nil {
		return lyric.Candidate{}, fmt.Errorf("parse LRCLib lyricsfile: %w", lyricsFileErr)
	}
	return lyric.Candidate{}, errNoLRCLibPayload
}

// lrclibGetURL builds the exact-match lookup; the album is optional but improves
// the cache key upstream.
func lrclibGetURL(track lyrics.TrackInfo) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := lyric.TrackArtist(track)
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
// broken replies stay retryable instead of becoming cached misses.
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
	return lyric.TokensMatch(lyric.NormalizedTokens(trackName), lyric.NormalizedTokens(track.Title)) &&
		lrclibArtistMatches(artistName, track)
}

// lrclibArtistMatches accepts the record when any collaborating artist name
// matches the request completely (no prefix tolerance for artists).
func lrclibArtistMatches(artistName string, track lyrics.TrackInfo) bool {
	return lyric.ArtistMatch(artistName, lyric.TrackArtist(track))
}

// lrclibDurationsClose keeps the historical two-second tolerance.
func lrclibDurationsClose(recordSeconds float64, track lyrics.TrackInfo) bool {
	if track.Duration <= 0 {
		return true
	}
	delta := math.Abs(recordSeconds - float64(track.Duration))
	return delta <= 2
}
