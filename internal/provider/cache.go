package provider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

const (
	CacheMaxSize                   = "32MB"
	lyricsCacheMaxBytes      int64 = 32 * 1024 * 1024
	lyricsCacheReservedBytes int64 = 1 * 1024 * 1024
	MaxCacheEntryBytes             = 1 * 1024 * 1024
	// syncedLyricsCacheTTLSeconds covers synchronized winner entries and the
	// per-source cache of synchronized results.
	syncedLyricsCacheTTLSeconds = int64(24 * time.Hour / time.Second)
	// unsyncedLyricsCacheTTLSeconds covers word/line-quality winners and the
	// per-source cache of plain results.
	unsyncedLyricsCacheTTLSeconds = int64(time.Hour / time.Second)
	negativeLyricsCacheTTLSeconds = int64(5 * time.Minute / time.Second)
	credentialSaltKey             = "credential-salt:v1"
	lyricsWinnerCacheKeyPrefix    = "lyrics:v4:winner:"
	lyricsSourceCacheKeyPrefix    = "lyrics:v4:source:"
	lyricsCacheGeneration         = 1
)

type lyricsCacheUsageStore interface {
	StorageUsed() (int64, error)
}

type noopLyricsCache struct{}

func (noopLyricsCache) Get(string) ([]byte, bool, error) {
	return nil, false, nil
}

func (noopLyricsCache) SetWithTTL(string, []byte, int64) error {
	return nil
}

func lyricsTrackKey(track lyrics.TrackInfo) (string, bool) {
	title := strings.ToLower(strings.TrimSpace(track.Title))
	artist := strings.ToLower(lyric.TrackArtist(track))
	if title == "" || artist == "" {
		return "", false
	}

	album := strings.ToLower(strings.TrimSpace(track.Album))
	duration := int64(math.Round(float64(track.Duration)))
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", title, artist, album, duration)))
	return fmt.Sprintf("%x", digest), true
}

func lyricsCacheKeyForSource(track lyrics.TrackInfo, sourceID string) (string, bool) {
	trackKey, ok := lyricsTrackKey(track)
	if !ok || sourceID == "" {
		return "", false
	}
	return fmt.Sprintf("%s%s:%s", lyricsSourceCacheKeyPrefix, sourceID, trackKey), true
}

// lyricsWinnerCacheKey fingerprints the enabled, ordered source list into the key
// so reordering or rescaling the source set takes effect on the next lookup.
func lyricsWinnerCacheKey(track lyrics.TrackInfo, sourceOrder []string) (string, bool) {
	trackKey, ok := lyricsTrackKey(track)
	if !ok || len(sourceOrder) == 0 {
		return "", false
	}
	fingerprint := sha256.Sum256([]byte(strings.Join(sourceOrder, "|")))
	return fmt.Sprintf("%s%x:%s", lyricsWinnerCacheKeyPrefix, fingerprint, trackKey), true
}

type cachedLyricsEnvelope struct {
	Generation    int                      `json:"generation,omitempty"`
	Quality       lyric.TimingQuality      `json:"quality"`
	Response      lyrics.GetLyricsResponse `json:"response"`
	SweepComplete bool                     `json:"sweepComplete,omitempty"`
	Credentials   string                   `json:"credentials,omitempty"`
	// ExpiresAt is the write-time expiry (unix seconds) so readers can bound
	// derived TTLs by the entry's remaining life. Entries without it
	// are treated as unsafely-aged and refreshed.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
}

// winnerCacheTLSeconds maps a candidate's timing quality to the winner entry TTL.
func winnerCacheTTLSeconds(quality lyric.TimingQuality) int64 {
	switch quality {
	case lyric.TimingSyllable:
		return syncedLyricsCacheTTLSeconds
	case lyric.TimingWord, lyric.TimingLine:
		return unsyncedLyricsCacheTTLSeconds
	default:
		return negativeLyricsCacheTTLSeconds
	}
}

func sourceCacheTTLSeconds(quality lyric.TimingQuality) int64 {
	if quality >= lyric.TimingLine {
		return syncedLyricsCacheTTLSeconds
	}
	return unsyncedLyricsCacheTTLSeconds
}

func (p *Provider) readCachedCandidate(key string) (lyric.Candidate, bool) {
	candidate, _, ok := p.readCachedCandidateWithExpiry(key)
	return candidate, ok
}

// readCachedCandidateWithExpiry also returns the write-time expiry so callers
// that derive their own TTLs (the sweep's marker bounds) can use the REMAINING
// lifetime instead of the original one. Zero means the entry predates
// expiry stamping and cannot be trusted for derived TTLs.
func (p *Provider) readCachedCandidateWithExpiry(key string) (lyric.Candidate, int64, bool) {
	envelope, ok := p.readCachedEnvelope(key)
	if !ok {
		return lyric.Candidate{}, 0, false
	}
	return cachedCandidate(envelope), envelope.ExpiresAt, true
}

func cachedCandidate(envelope cachedLyricsEnvelope) lyric.Candidate {
	candidate := lyric.Candidate{Response: envelope.Response, Quality: envelope.Quality}
	if envelope.Generation == lyricsCacheGeneration {
		return candidate
	}
	// Older grading trusted TTML metadata alone. Regrade readable legacy
	// content before it can establish a new winner while its source refreshes.
	var best lyric.Candidate
	for _, text := range envelope.Response.Lyrics {
		provenance := text.Source
		if provenance == nil {
			provenance = envelope.Response.Source
		}
		format := "elrc"
		if provenance != nil && provenance.Format != "" {
			format = provenance.Format
		} else if strings.HasPrefix(strings.TrimSpace(text.Text), "<") {
			format = "ttml"
		}
		best = lyric.Prefer(best, lyric.NewCandidate(text, "", format))
	}
	if !best.Present() {
		return lyric.Candidate{}
	}
	candidate.Quality = best.Quality
	return candidate
}

func (p *Provider) readCachedEnvelope(key string) (cachedLyricsEnvelope, bool) {
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		return cachedLyricsEnvelope{}, false
	}
	var envelope cachedLyricsEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		p.logf(fmt.Sprintf("lyrics cache entry decode failed: %v", p.safeDiagnosticError(err)))
		return cachedLyricsEnvelope{}, false
	}
	if envelope.ExpiresAt > 0 && envelope.ExpiresAt <= p.now().Unix() {
		return cachedLyricsEnvelope{}, false
	}
	if envelope.ExpiresAt == 0 && len(envelope.Response.Lyrics) == 0 {
		// Only existing lyrics justify serving a legacy unstamped entry.
		// An undated miss cannot suppress a fresh provider lookup.
		return cachedLyricsEnvelope{}, false
	}
	return envelope, true
}

func (p *Provider) storeCandidateValue(key string, candidate lyric.Candidate, ttl int64, credentials string) bool {
	value, err := json.Marshal(cachedLyricsEnvelope{Generation: lyricsCacheGeneration, Quality: candidate.Quality, Response: candidate.Response, Credentials: credentials, ExpiresAt: p.now().Add(time.Duration(ttl) * time.Second).Unix()})
	if err != nil {
		p.logf(fmt.Sprintf("lyrics cache encode failed: %v", p.safeDiagnosticError(err)))
		return false
	}
	return p.storeCandidateBytes(key, value, len(value) > MaxCacheEntryBytes, ttl)
}

// storeCandidateBytes writes an already-marshaled envelope with the shared
// per-entry cap and KV headroom guards.
func (p *Provider) storeCandidateBytes(key string, value []byte, oversized bool, ttl int64) bool {
	if oversized {
		p.logf(fmt.Sprintf("lyrics cache entry skipped: %d bytes exceeds %d-byte limit", len(value), MaxCacheEntryBytes))
		return false
	}
	if usageStore, ok := p.cache.(lyricsCacheUsageStore); ok {
		used, err := usageStore.StorageUsed()
		if err != nil {
			p.logf(fmt.Sprintf("lyrics cache usage lookup failed: %v", p.safeDiagnosticError(err)))
		} else if used >= lyricsCacheMaxBytes-lyricsCacheReservedBytes || used+int64(len(value)) > lyricsCacheMaxBytes-lyricsCacheReservedBytes {
			p.logf("lyrics cache entry skipped to preserve rate-limit storage headroom")
			return false
		}
	}
	if err := p.cache.SetWithTTL(key, value, ttl); err != nil {
		p.logf(fmt.Sprintf("lyrics cache write failed: %v", p.safeDiagnosticError(err)))
		return false
	}
	return true
}

func (p *Provider) readCachedSourceCandidate(track lyrics.TrackInfo, sourceID string) (lyric.Candidate, bool) {
	key, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok {
		return lyric.Candidate{}, false
	}
	return p.readCachedCandidate(key)
}

// cacheSourceMiss records a healthy source's "no lyrics here" as a negative
// per-source entry (positives always override it via the main key) so later
// passes and continuations skip the refetch.
func (p *Provider) cacheSourceMiss(track lyrics.TrackInfo, sourceID string) bool {
	key, ok := p.sourceMissKey(track, sourceID)
	if !ok {
		return false
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		Generation: lyricsCacheGeneration,
		Quality:    lyric.TimingUnsynced,
		ExpiresAt:  p.now().Add(time.Duration(negativeLyricsCacheTTLSeconds) * time.Second).Unix(),
	})
	if err != nil {
		return false
	}
	if p.cache.SetWithTTL(key, value, negativeLyricsCacheTTLSeconds) != nil {
		return false
	}
	// An oversized/evicted write must not count as coverage.
	_, exists, _ := p.cache.Get(key)
	return exists
}

// readCachedSourceCoverage reads whichever per-source entry exists for a
// source: a positive candidate first, otherwise the credential-namespaced
// negative entry the sweep and stage-one misses persist.
func (p *Provider) readCachedSourceCoverage(track lyrics.TrackInfo, sourceID string) (lyric.Candidate, bool) {
	candidate, _, ok := p.readCachedSourceCoverageWithExpiry(track, sourceID)
	return candidate, ok
}

// readCachedSourceCoverageWithExpiry reads whichever per-source entry exists
// (positive first, then the credential-namespaced miss) together with its
// write-time expiry for remaining-lifetime bounds.
func (p *Provider) readCachedSourceCoverageWithExpiry(track lyrics.TrackInfo, sourceID string) (lyric.Candidate, int64, bool) {
	if key, ok := lyricsCacheKeyForSource(track, sourceID); ok {
		if envelope, found := p.readCachedEnvelope(key); found && envelope.Generation == lyricsCacheGeneration && envelope.Credentials == p.sourceCredentialsFingerprint(sourceID) {
			return lyric.Candidate{Response: envelope.Response, Quality: envelope.Quality}, envelope.ExpiresAt, true
		}
	}
	key, ok := p.sourceMissKey(track, sourceID)
	if !ok {
		return lyric.Candidate{}, 0, false
	}
	envelope, found := p.readCachedEnvelope(key)
	if !found || envelope.Generation != lyricsCacheGeneration {
		return lyric.Candidate{}, 0, false
	}
	return cachedCandidate(envelope), envelope.ExpiresAt, true
}

// cacheSourceCandidate persists one per-source result and reports whether the
// write actually landed, so the sweep only counts entries it really cached.
func (p *Provider) cacheSourceCandidate(track lyrics.TrackInfo, sourceID string, candidate lyric.Candidate) bool {
	key, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok || !candidate.Present() {
		return false
	}
	return p.storeCandidateValue(key, candidate, sourceCacheTTLSeconds(candidate.Quality), p.sourceCredentialsFingerprint(sourceID))
}

// readBestFromSourceCache ranks fresh per-source results in configured order; the
// earlier source wins timing-quality ties because lyric.Prefer keeps the
// current candidate.
func (p *Provider) readBestFromSourceCache(track lyrics.TrackInfo, order []source.Source) lyric.Candidate {
	var best lyric.Candidate
	for _, source := range order {
		candidate, ok := p.readCachedSourceCandidate(track, source.ID)
		if !ok {
			continue
		}
		best = lyric.Prefer(best, candidate)
	}
	return best
}

func (p *Provider) readCachedWinner(track lyrics.TrackInfo, order []source.Source) (lyric.Candidate, bool, bool) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return lyric.Candidate{}, false, false
	}
	envelope, exists := p.readCachedEnvelope(key)
	if !exists || envelope.Generation != lyricsCacheGeneration {
		return lyric.Candidate{}, false, false
	}
	if envelope.Credentials != p.lyricsCredentialsFingerprint() {
		// Winner coverage recorded under different credentials does not
		// authorise the current lookup: re-adding a key must re-walk.
		return lyric.Candidate{}, false, false
	}
	sweepComplete := envelope.SweepComplete
	if sweepComplete {
		// A positive winner can outlive a sibling's short miss coverage.
		// Its display TTL must not postpone discovery after that miss expires.
		for _, src := range order {
			if src.Fetch == nil {
				continue
			}
			_, expiry, covered := p.readCachedSourceCoverageWithExpiry(track, src.ID)
			if !covered || expiry <= p.now().Unix() {
				sweepComplete = false
				break
			}
		}
	}
	return lyric.Candidate{Response: envelope.Response, Quality: envelope.Quality}, true, sweepComplete
}

// cacheWinner stores the selected candidate; negative lookup results live here too
// so a second lookup for the same track with the same source order skips the walk.
// sweepComplete records whether the producing lookup covered every implemented
// enabled source, so cached hits know whether a sweep is still pending.
func (p *Provider) cacheWinner(track lyrics.TrackInfo, order []source.Source, candidate lyric.Candidate, sweepComplete bool) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		Generation:    lyricsCacheGeneration,
		Quality:       candidate.Quality,
		Response:      candidate.Response,
		SweepComplete: sweepComplete,
		Credentials:   p.lyricsCredentialsFingerprint(),
		ExpiresAt:     p.now().Add(time.Duration(winnerCacheTTLSeconds(candidate.Quality)) * time.Second).Unix(),
	})
	if err != nil {
		p.logf(fmt.Sprintf("lyrics cache encode failed: %v", p.safeDiagnosticError(err)))
		return
	}
	p.storeCandidateBytes(key, value, len(value) > MaxCacheEntryBytes, winnerCacheTTLSeconds(candidate.Quality))
}

func (p *Provider) cacheNegativeWinner(track lyrics.TrackInfo, order []source.Source, sweepComplete bool) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		Generation:    lyricsCacheGeneration,
		SweepComplete: sweepComplete,
		Credentials:   p.lyricsCredentialsFingerprint(),
		ExpiresAt:     p.now().Add(time.Duration(negativeLyricsCacheTTLSeconds) * time.Second).Unix(),
	})
	if err != nil {
		p.logf(fmt.Sprintf("lyrics cache encode failed: %v", p.safeDiagnosticError(err)))
		return
	}
	p.storeCandidateBytes(key, value, len(value) > MaxCacheEntryBytes, negativeLyricsCacheTTLSeconds)
}
