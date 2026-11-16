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
	CacheMaxSize = "32MB"
	// Navidrome parses the manifest's MB unit as decimal, not MiB.
	lyricsCacheMaxBytes      int64 = 32_000_000
	lyricsCacheReservedBytes int64 = 1 * 1024 * 1024
	MaxCacheEntryBytes             = 1 * 1024 * 1024
	// Syllable winners and synchronized per-source results share a day-long TTL.
	lyricsCacheDayTTLSeconds = int64(24 * time.Hour / time.Second)
	// Word/line winners and plain per-source results share an hour-long TTL.
	lyricsCacheHourTTLSeconds     = int64(time.Hour / time.Second)
	negativeLyricsCacheTTLSeconds = int64(5 * time.Minute / time.Second)
	credentialSaltKey             = "credential-salt:v1"
	lyricsWinnerCacheKeyPrefix    = "lyrics:v5:winner:"
	lyricsSourceCacheKeyPrefix    = "lyrics:v5:source:"
	lyricsVariantCacheKeyPrefix   = "lyrics:v5:variants:"
	lyricsOriginalCacheKeyPrefix  = "lyrics:v5:original:"
	lyricsCacheGeneration         = 3
)

type lyricsCacheUsageStore interface {
	StorageUsed() (int64, error)
}

type prefixDeletingStore interface {
	DeleteByPrefix(prefix string) (int64, error)
}

// matchingCacheMigrationMarkerKey records the bounded v4 lyric-cache
// invalidation required by the recording-identity migration. It deletes only
// the plugin's own disposable lyric namespace; salts, credentials, config and
// durable control records are untouched.
const matchingCacheMigrationMarkerKey = "cache-migration:recording-match:v1"

// removedSourcePurgeMarkerKey records that installations upgraded from a
// release with the Musixmatch source no longer hold its token or lyrics.
const removedSourcePurgeMarkerKey = "cache-purge:musixmatch:v1"

// removedSourcePurgePrefixes covers the minted token, the source's own cached
// results and misses, LyricsPlus results (it used to request a Musixmatch
// backend and caches them under its own id), and every winner entry: winner
// keys hash the source order, so entries that may hold that source's lyrics
// cannot be told apart. Completed sweep markers go too, because they would
// otherwise claim coverage for the purged LyricsPlus results and block the
// sweep that refills them when the saved order did not include Musixmatch.
var removedSourcePurgePrefixes = []string{
	"musixmatch:",
	lyricsSourceCacheKeyPrefix + "musixmatch:",
	lyricsSourceCacheKeyPrefix + "lyricsplus:",
	lyricsVariantCacheKeyPrefix + "musixmatch:",
	lyricsOriginalCacheKeyPrefix + "musixmatch:",
	lyricsWinnerCacheKeyPrefix,
	sweepDonePrefix,
}

// purgeLegacyMatchingCache invalidates only the previous lyric-cache namespace.
func (p *Provider) purgeLegacyMatchingCache() {
	store, ok := p.cache.(prefixDeletingStore)
	if !ok {
		return
	}
	if _, done, err := p.cache.Get(matchingCacheMigrationMarkerKey); err != nil || done {
		return
	}
	if _, err := store.DeleteByPrefix("lyrics:v4:"); err != nil {
		p.logf(fmt.Sprintf("legacy lyric cache migration failed; retrying on next plugin load: %v", p.safeDiagnosticError(err)))
		return
	}
	_ = p.cache.SetWithTTL(matchingCacheMigrationMarkerKey, []byte("1"), credentialSaltTTL)
}

// purgeRemovedSourceCache runs once per installation. It stamps the marker
// only after every prefix is gone so a failed purge retries on the next init.
func (p *Provider) purgeRemovedSourceCache() {
	store, ok := p.cache.(prefixDeletingStore)
	if !ok {
		return
	}
	if _, done, err := p.cache.Get(removedSourcePurgeMarkerKey); err != nil || done {
		return
	}
	for _, prefix := range removedSourcePurgePrefixes {
		if _, err := store.DeleteByPrefix(prefix); err != nil {
			p.logf(fmt.Sprintf("removed-source cache purge failed; retrying on next plugin load: %v", p.safeDiagnosticError(err)))
			return
		}
	}
	_ = p.cache.SetWithTTL(removedSourcePurgeMarkerKey, []byte("1"), credentialSaltTTL)
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
	return lyricsSourceCacheKey(track, sourceID, lyricsSourceCacheKeyPrefix)
}

// lyricsSourceCacheKey centralizes track/source key construction for the
// per-source and variant-bundle cache families.
func lyricsSourceCacheKey(track lyrics.TrackInfo, sourceID, prefix string) (string, bool) {
	trackKey, ok := lyricsTrackKey(track)
	if !ok || sourceID == "" || prefix == "" {
		return "", false
	}
	return fmt.Sprintf("%s%s:%s", prefix, sourceID, trackKey), true
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
	// Variant and Original are additive identity metadata. Older readers
	// ignore them; entries without them remain readable.
	Variant  *lyric.VariantIdentity `json:"variant,omitempty"`
	Original *lyric.OriginalInfo    `json:"original,omitempty"`
	Match    lyric.RecordingMatch   `json:"match,omitempty"`
	Evidence []lyric.Evidence       `json:"evidence,omitempty"`
	// ExpiresAt is the write-time expiry (unix seconds) so readers can bound
	// derived TTLs by the entry's remaining life. Entries without it
	// are treated as unsafely-aged and refreshed.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// SourceExpiresAt preserves a winner's fetched-result lifetime when cached
	// source data is promoted again. Older winners lack this bound and must be
	// rebuilt from their source entries instead of renewing their age.
	SourceExpiresAt int64 `json:"sourceExpiresAt,omitempty"`
}

// winnerCacheTLSeconds maps a candidate's timing quality to the winner entry TTL.
func winnerCacheTTLSeconds(quality lyric.TimingQuality) int64 {
	switch quality {
	case lyric.TimingSyllable:
		return lyricsCacheDayTTLSeconds
	case lyric.TimingWord, lyric.TimingLine:
		return lyricsCacheHourTTLSeconds
	default:
		return negativeLyricsCacheTTLSeconds
	}
}

func sourceCacheTTLSeconds(quality lyric.TimingQuality) int64 {
	if quality >= lyric.TimingLine {
		return lyricsCacheDayTTLSeconds
	}
	return lyricsCacheHourTTLSeconds
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
	candidate := lyric.Candidate{Response: envelope.Response, Quality: envelope.Quality, Identity: envelope.Variant, Match: envelope.Match}
	if envelope.Original != nil {
		candidate.OriginalSummary = *envelope.Original
	}
	if envelope.Generation == lyricsCacheGeneration {
		if candidate.Match.Status == "" {
			candidate.Match = lyric.RecordingMatch{Status: lyric.MatchUnassessed}
		}
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
	original := candidate.OriginalSummary
	value, err := json.Marshal(cachedLyricsEnvelope{
		Generation:  lyricsCacheGeneration,
		Quality:     candidate.Quality,
		Response:    candidate.Response,
		Credentials: credentials,
		Variant:     candidate.Identity,
		Original:    &original,
		Match:       candidate.Match,
		Evidence:    dedupeEvidence(candidate.Evidence),
		ExpiresAt:   p.now().Add(time.Duration(ttl) * time.Second).Unix(),
	})
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
	if !p.storeCandidateBytes(key, value, len(value) > MaxCacheEntryBytes, negativeLyricsCacheTTLSeconds) {
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
			return cachedCandidate(envelope), envelope.ExpiresAt, true
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

// readBestFromSourceCache ranks fresh per-source results in configured order; the
// earlier source wins timing-quality ties. The expiry follows the selected
// source so a derived winner cannot acquire a new fetched-result lifetime.
func (p *Provider) readBestFromSourceCache(track lyrics.TrackInfo, order []source.Source) (lyric.Candidate, int64) {
	var best lyric.Candidate
	var expiresAt int64
	for _, source := range order {
		key, ok := lyricsCacheKeyForSource(track, source.ID)
		if !ok {
			continue
		}
		candidate, expiry, ok := p.readCachedCandidateWithExpiry(key)
		if ok && lyric.MorePreferred(best, candidate) {
			best, expiresAt = candidate, expiry
		}
	}
	return best, expiresAt
}

func (p *Provider) readCachedWinner(track lyrics.TrackInfo, order []source.Source) (lyric.Candidate, []lyric.Evidence, bool, bool) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return lyric.Candidate{}, nil, false, false
	}
	envelope, exists := p.readCachedEnvelope(key)
	if !exists || envelope.Generation != lyricsCacheGeneration {
		return lyric.Candidate{}, nil, false, false
	}
	if envelope.Credentials != p.lyricsCredentialsFingerprint() {
		// Winner coverage recorded under different credentials does not
		// authorise the current lookup: re-adding a key must re-walk.
		return lyric.Candidate{}, nil, false, false
	}
	if len(envelope.Response.Lyrics) > 0 && envelope.SourceExpiresAt <= p.now().Unix() {
		// Rebuild old, unbounded winners from valid source entries. In
		// particular, an old renewal must not hide a fresh miss or downgrade.
		return lyric.Candidate{}, nil, false, false
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
	return cachedCandidate(envelope), envelope.Evidence, true, sweepComplete
}

// cacheWinner stores the selected candidate; negative lookup results live here too
// so a second lookup for the same track with the same source order skips the walk.
// sweepComplete records whether the producing lookup covered every implemented
// enabled source, so cached hits know whether a sweep is still pending.
func (p *Provider) cacheWinner(track lyrics.TrackInfo, order []source.Source, candidate lyric.Candidate, sweepComplete bool) {
	p.cacheWinnerUntil(track, order, candidate, sweepComplete, p.now().Add(time.Duration(sourceCacheTTLSeconds(candidate.Quality))*time.Second).Unix(), nil)
}

func (p *Provider) cacheWinnerUntil(track lyrics.TrackInfo, order []source.Source, candidate lyric.Candidate, sweepComplete bool, sourceExpiresAt int64, evidence []lyric.Evidence) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return
	}
	now := p.now().Unix()
	expiresAt := min(now+winnerCacheTTLSeconds(candidate.Quality), sourceExpiresAt)
	ttl := expiresAt - now
	if ttl <= 0 {
		// Legacy source entries without a fetched expiry remain readable, but
		// cannot authorize another persisted winner lifetime.
		return
	}
	original := candidate.OriginalSummary
	value, err := json.Marshal(cachedLyricsEnvelope{
		Generation:      lyricsCacheGeneration,
		Quality:         candidate.Quality,
		Response:        candidate.Response,
		SweepComplete:   sweepComplete,
		Credentials:     p.lyricsCredentialsFingerprint(),
		Variant:         candidate.Identity,
		Original:        &original,
		Match:           candidate.Match,
		Evidence:        dedupeEvidence(append(evidence, candidate.Evidence...)),
		ExpiresAt:       expiresAt,
		SourceExpiresAt: sourceExpiresAt,
	})
	if err != nil {
		p.logf(fmt.Sprintf("lyrics cache encode failed: %v", p.safeDiagnosticError(err)))
		return
	}
	p.storeCandidateBytes(key, value, len(value) > MaxCacheEntryBytes, ttl)
}

func (p *Provider) cacheNegativeWinner(track lyrics.TrackInfo, order []source.Source, sweepComplete bool, evidence []lyric.Evidence) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		Generation:    lyricsCacheGeneration,
		SweepComplete: sweepComplete,
		Credentials:   p.lyricsCredentialsFingerprint(),
		Evidence:      dedupeEvidence(evidence),
		ExpiresAt:     p.now().Add(time.Duration(negativeLyricsCacheTTLSeconds) * time.Second).Unix(),
	})
	if err != nil {
		p.logf(fmt.Sprintf("lyrics cache encode failed: %v", p.safeDiagnosticError(err)))
		return
	}
	p.storeCandidateBytes(key, value, len(value) > MaxCacheEntryBytes, negativeLyricsCacheTTLSeconds)
}
