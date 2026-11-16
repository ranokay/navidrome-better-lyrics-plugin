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

	evidence := make([]lyric.Candidate, 0)
	response, health, err := fetchLRCLibEndpoint(rt, "LRCLib get", getURL, deadline)
	if err != nil {
		return nil, health, err
	}
	var exactFallback []lyric.Candidate
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
			candidates, candErr := lrclibCandidates(rt, response)
			if candErr != nil && !errors.Is(candErr, errNoLRCLibPayload) {
				return nil, Degraded, candErr
			}
			if candErr == nil {
				var present []lyric.Candidate
				for _, candidate := range candidates {
					if candidate.Present() {
						present = append(present, candidate)
					} else {
						evidence = append(evidence, candidate)
					}
				}
				if len(present) > 0 {
					match := lyric.AssessRecording(lyric.RequestRecordingInfo(track), candidates[0].RecordingInfo)
					if match.Status == lyric.MatchValidated {
						return append(present, evidence...), health, nil
					}
					exactFallback = present
				}
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
		return lrclibFallback(exactFallback, evidence, health, err)
	}
	if len(searchBody) == 0 {
		// The search endpoint answered a miss (404) with no body: decoding nil
		// would degrade a clean miss (spotted while wiring the metadata gate).
		return lrclibFallback(exactFallback, evidence, health, nil)
	}

	var results []lrclibRecord
	if err := json.Unmarshal(searchBody, &results); err != nil {
		return lrclibFallback(exactFallback, evidence, Degraded, fmt.Errorf("decode LRCLib search response: %w", err))
	}
	target := int64(math.Round(float64(track.Duration)))
	var best []lyric.Candidate
	var fallback []lyric.Candidate
	bestDelta := int64(math.MaxInt64)
	var candidateErrors []error
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
		if len(best) > 0 && delta >= bestDelta {
			continue
		}
		rowJSON, err := json.Marshal(result)
		if err != nil {
			candidateErrors = append(candidateErrors, fmt.Errorf("re-encode LRCLib match: %w", err))
			continue
		}
		candidates, err := lrclibCandidates(rt, rowJSON)
		if err != nil {
			if !errors.Is(err, errNoLRCLibPayload) {
				candidateErrors = append(candidateErrors, err)
			}
			continue
		}
		var present []lyric.Candidate
		for _, candidate := range candidates {
			if candidate.Present() {
				present = append(present, candidate)
			} else {
				evidence = append(evidence, candidate)
			}
		}
		if len(present) > 0 {
			match := lyric.AssessRecording(lyric.RequestRecordingInfo(track), present[0].RecordingInfo)
			switch match.Status {
			case lyric.MatchValidated:
				best, bestDelta = present, delta
			case lyric.MatchAmbiguous:
				if len(fallback) == 0 {
					fallback = present
				}
			}
		}
	}
	if len(best) > 0 {
		return append(best, evidence...), Healthy, nil
	}
	if len(fallback) == 0 {
		fallback = exactFallback
	}
	if len(fallback) > 0 {
		return append(fallback, evidence...), Healthy, nil
	}
	if err := errors.Join(candidateErrors...); err != nil {
		return nil, Degraded, err
	}
	if len(evidence) > 0 {
		return evidence, Healthy, nil
	}
	return nil, Healthy, nil
}

// lrclibFallback preserves already-decoded exact suggestions and assertions
// when the broader search misses or fails. The assertion is never derived from
// the failed attempt.
func lrclibFallback(fallback, evidence []lyric.Candidate, health Health, err error) ([]lyric.Candidate, Health, error) {
	out := make([]lyric.Candidate, 0, len(fallback)+len(evidence))
	out = append(out, fallback...)
	out = append(out, evidence...)
	if len(out) == 0 {
		return nil, health, err
	}
	return out, health, err
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

// lrclibCandidates turns an LRCLib record into every representation the wire
// supplies: lyricsfile words (ELRC), synced LRC, then plain text. Each candidate
// keeps its exact source document; reconstructed ELRC never stands in for the
// lyricsfile.
func lrclibCandidates(rt Runtime, record []byte) ([]lyric.Candidate, error) {
	var payload struct {
		ID           int64   `json:"id"`
		TrackName    string  `json:"trackName"`
		ArtistName   string  `json:"artistName"`
		AlbumName    string  `json:"albumName"`
		Duration     float64 `json:"duration"`
		Instrumental bool    `json:"instrumental"`
		HasWordSync  bool    `json:"hasWordSync"`
		PlainLyrics  string  `json:"plainLyrics"`
		SyncedLyrics string  `json:"syncedLyrics"`
		LyricsFile   string  `json:"lyricsfile"`
	}
	if err := json.Unmarshal(record, &payload); err != nil {
		return nil, fmt.Errorf("decode LRCLib record: %w", err)
	}
	recording := ""
	if payload.ID > 0 {
		recording = strconv.FormatInt(payload.ID, 10)
	}
	info := lyric.RecordingInfo{
		Title: payload.TrackName, Artist: payload.ArtistName, Album: payload.AlbumName,
		Duration: payload.Duration,
	}
	instrumentalEvidence := payload.Instrumental
	candidates := make([]lyric.Candidate, 0, 3)
	var lyricsFileErr error
	if payload.HasWordSync && strings.TrimSpace(payload.LyricsFile) != "" {
		lines, err := lyric.ParseLRCLibLyricsFile([]byte(payload.LyricsFile))
		if err == nil && len(lines) > 0 {
			text := lyrics.LyricsText{Text: lyric.EnhancedLRC(lines)}
			if candidate := lyric.NewCandidate(text, "LRCLib", "elrc").
				WithOriginal("lyricsfile", lyric.OriginalSupplied, []byte(payload.LyricsFile)).
				WithRecording(recording).WithRecordingInfo(info); candidate.Present() {
				candidates = append(candidates, candidate)
			}
		} else {
			// A malformed lyricsfile stays retryable when no fallback exists.
			lyricsFileErr = err
		}
	}
	if strings.TrimSpace(payload.SyncedLyrics) != "" {
		text := lyrics.LyricsText{Text: payload.SyncedLyrics}
		if candidate := lyric.NewCandidate(text, "LRCLib", "lrc").
			WithOriginal("lrc", lyric.OriginalSupplied, []byte(payload.SyncedLyrics)).
			WithRecording(recording).WithRecordingInfo(info); candidate.Present() {
			candidates = append(candidates, candidate)
		}
	}
	if strings.TrimSpace(payload.PlainLyrics) != "" {
		text := lyrics.LyricsText{Text: payload.PlainLyrics}
		if candidate := lyric.NewCandidate(text, "LRCLib", "plain").
			WithOriginal("plain", lyric.OriginalSupplied, []byte(payload.PlainLyrics)).
			WithRecording(recording).WithRecordingInfo(info); candidate.Present() {
			candidates = append(candidates, candidate)
		}
	}
	if instrumentalEvidence {
		candidates = append(candidates, lyric.Candidate{
			RecordingInfo: info,
			Evidence: []lyric.Evidence{{
				Kind: lyric.EvidenceInstrumental, Provider: "LRCLib", Recording: recording,
			}},
		})
	}
	if len(candidates) > 0 {
		return candidates, nil
	}
	if lyricsFileErr != nil {
		return nil, fmt.Errorf("parse LRCLib lyricsfile: %w", lyricsFileErr)
	}
	return nil, errNoLRCLibPayload
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
	ID           int64   `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
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
