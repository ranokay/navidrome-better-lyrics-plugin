package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

const (
	lyricsCacheMaxSize              = "32MB"
	lyricsCacheMaxBytes       int64 = 32 * 1024 * 1024
	lyricsCacheReservedBytes  int64 = 1 * 1024 * 1024
	maxCachedLyricsEntryBytes       = 1 * 1024 * 1024
	// syncedLyricsCacheTTLSeconds covers synchronized winner entries and the
	// per-source cache of synchronized results.
	syncedLyricsCacheTTLSeconds = int64(24 * time.Hour / time.Second)
	// unsyncedLyricsCacheTTLSeconds covers word/line-quality winners and the
	// per-source cache of plain results; sources that miss can be retried soon.
	unsyncedLyricsCacheTTLSeconds = int64(time.Hour / time.Second)
	negativeLyricsCacheTTLSeconds = int64(5 * time.Minute / time.Second)
	credentialSaltKey             = "credential-salt:v1"
	lyricsWinnerCacheKeyPrefix    = "lyrics:v4:winner:"
	lyricsSourceCacheKeyPrefix    = "lyrics:v4:source:"
)

type lyricsCacheStore interface {
	Get(key string) ([]byte, bool, error)
	SetWithTTL(key string, value []byte, ttlSeconds int64) error
}

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

func (p *lyricsProvider) readCachedCandidate(key string) (lyric.Candidate, bool) {
	candidate, _, ok := p.readCachedCandidateWithExpiry(key)
	return candidate, ok
}

// readCachedCandidateWithExpiry also returns the write-time expiry so callers
// that derive their own TTLs (the sweep's marker bounds) can use the REMAINING
// lifetime instead of the original one. Zero means the entry predates
// expiry stamping and cannot be trusted for derived TTLs.
func (p *lyricsProvider) readCachedCandidateWithExpiry(key string) (lyric.Candidate, int64, bool) {
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		return lyric.Candidate{}, 0, false
	}
	var envelope cachedLyricsEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache entry decode failed: %v", err))
		return lyric.Candidate{}, 0, false
	}
	return lyric.Candidate{Response: envelope.Response, Quality: envelope.Quality}, envelope.ExpiresAt, true
}

func (p *lyricsProvider) storeCandidateValue(key string, candidate lyric.Candidate, ttl int64) bool {
	value, err := json.Marshal(cachedLyricsEnvelope{Quality: candidate.Quality, Response: candidate.Response, ExpiresAt: p.now().Add(time.Duration(ttl) * time.Second).Unix()})
	if err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache encode failed: %v", err))
		return false
	}
	return p.storeCandidateBytes(key, value, len(value) > maxCachedLyricsEntryBytes, ttl)
}

// storeCandidateBytes writes an already-marshaled envelope with the shared
// per-entry cap and KV headroom guards.
func (p *lyricsProvider) storeCandidateBytes(key string, value []byte, oversized bool, ttl int64) bool {
	if oversized {
		logCacheDebug(fmt.Sprintf("lyrics cache entry skipped: %d bytes exceeds %d-byte limit", len(value), maxCachedLyricsEntryBytes))
		return false
	}
	if usageStore, ok := p.cache.(lyricsCacheUsageStore); ok {
		used, err := usageStore.StorageUsed()
		if err != nil {
			logCacheDebug(fmt.Sprintf("lyrics cache usage lookup failed: %v", err))
		} else if used >= lyricsCacheMaxBytes-lyricsCacheReservedBytes || used+int64(len(value)) > lyricsCacheMaxBytes-lyricsCacheReservedBytes {
			logCacheDebug("lyrics cache entry skipped to preserve rate-limit storage headroom")
			return false
		}
	}
	if err := p.cache.SetWithTTL(key, value, ttl); err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache write failed: %v", err))
		return false
	}
	return true
}

func (p *lyricsProvider) readCachedSourceCandidate(track lyrics.TrackInfo, sourceID string) (lyric.Candidate, bool) {
	key, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok {
		return lyric.Candidate{}, false
	}
	return p.readCachedCandidate(key)
}

// missingCoverageEnvelope is the serialized negative per-source entry: an
// unsynced-quality envelope with no lyrics, decodable by readCachedCandidate
// as a present=false-but-covered sentinel.
const missingCoverageEnvelope = `{"quality":0}`

// cacheCredentialPepper is the fallback HMAC key used only when the KV store
// cannot hold the per-installation salt.
var cacheCredentialPepper = []byte("navidrome-better-lyrics/credential-fingerprint/v1")

// credentialSaltTTL keeps the salt effectively permanent while still riding
// the TTL API.
const credentialSaltTTL = int64(10 * 365 * 24 * time.Hour / time.Second)

// credentialSalt returns the per-installation HMAC salt, minting one on first
// use and persisting it in the plugin KV store. A public constant would let
// anyone who reads cached entries test credential guesses offline (CWE-760);
// a per-installation salt removes that verifiability. The constant
// remains only as a last resort so fingerprinting never blocks lyrics when the
// store refuses writes.
// credentialSaltMutex serializes first-use salt initialization so concurrent
// callers mint and persist one salt instead of racing.
var credentialSaltMutex sync.Mutex

func (p *lyricsProvider) credentialSalt() []byte {
	credentialSaltMutex.Lock()
	defer credentialSaltMutex.Unlock()
	if len(p.credentialSaltBytes) >= 16 {
		return p.credentialSaltBytes
	}
	if raw, exists, _ := p.cache.Get(credentialSaltKey); exists && len(raw) >= 16 {
		p.credentialSaltBytes = raw
		return raw
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return cacheCredentialPepper
	}
	if p.cache.SetWithTTL(credentialSaltKey, salt, credentialSaltTTL) != nil {
		// Keep the fallback consistent while the store refuses writes: the
		// next call retries the mint so a recovered store gets one stable
		// salt instead of splitting fingerprints mid-process.
		return cacheCredentialPepper
	}
	p.credentialSaltBytes = salt
	return salt
}

// lyricsCredentialsFingerprint distinguishes negative coverage stamped under
// different Better Lyrics API key / Musixmatch token configurations: adding or
// replacing a credential must re-authorize sources whose misses were recorded
// anonymously.
func (p *lyricsProvider) lyricsCredentialsFingerprint() string {
	config := readProviderConfig(p.configLookup)
	mac := hmac.New(sha256.New, p.credentialSalt())
	mac.Write([]byte(config.BetterLyricsAPIKey))
	mac.Write([]byte{0})
	mac.Write([]byte(config.MusixmatchUserToken))
	return fmt.Sprintf("%x", mac.Sum(nil)[:8])
}

// sourceMissKey namespaces the negative per-source entries by credential state
// so a newly configured token or API key cannot stay suppressed by an old miss.
func (p *lyricsProvider) sourceMissKey(track lyrics.TrackInfo, sourceID string) (string, bool) {
	base, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s:missing:%s", base, p.lyricsCredentialsFingerprint()), true
}

// cacheSourceMiss records a healthy source's "no lyrics here" as a negative
// per-source entry (positives always override it via the main key) so later
// passes and continuations skip the refetch.
func (p *lyricsProvider) cacheSourceMiss(track lyrics.TrackInfo, sourceID string) bool {
	key, ok := p.sourceMissKey(track, sourceID)
	if !ok {
		return false
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		Quality:   lyric.TimingUnsynced,
		ExpiresAt: p.now().Add(time.Duration(unsyncedLyricsCacheTTLSeconds) * time.Second).Unix(),
	})
	if err != nil {
		return false
	}
	if p.cache.SetWithTTL(key, value, unsyncedLyricsCacheTTLSeconds) != nil {
		return false
	}
	// An oversized/evicted write must not count as coverage.
	_, exists, _ := p.cache.Get(key)
	return exists
}

// readCachedSourceCoverage reads whichever per-source entry exists for a
// source: a positive candidate first, otherwise the credential-namespaced
// negative entry the sweep and stage-one misses persist.
func (p *lyricsProvider) readCachedSourceCoverage(track lyrics.TrackInfo, sourceID string) (lyric.Candidate, bool) {
	candidate, _, ok := p.readCachedSourceCoverageWithExpiry(track, sourceID)
	return candidate, ok
}

// readCachedSourceCoverageWithExpiry reads whichever per-source entry exists
// (positive first, then the credential-namespaced miss) together with its
// write-time expiry for remaining-lifetime bounds.
func (p *lyricsProvider) readCachedSourceCoverageWithExpiry(track lyrics.TrackInfo, sourceID string) (lyric.Candidate, int64, bool) {
	if key, ok := lyricsCacheKeyForSource(track, sourceID); ok {
		if candidate, expiresAt, found := p.readCachedCandidateWithExpiry(key); found {
			return candidate, expiresAt, true
		}
	}
	key, ok := p.sourceMissKey(track, sourceID)
	if !ok {
		return lyric.Candidate{}, 0, false
	}
	return p.readCachedCandidateWithExpiry(key)
}

// cacheSourceCandidate persists one per-source result and reports whether the
// write actually landed, so the sweep only counts entries it really cached.
func (p *lyricsProvider) cacheSourceCandidate(track lyrics.TrackInfo, sourceID string, candidate lyric.Candidate) bool {
	key, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok || !candidate.Present() {
		return false
	}
	return p.storeCandidateValue(key, candidate, sourceCacheTTLSeconds(candidate.Quality))
}

// readBestFromSourceCache ranks fresh per-source results in configured order; the
// earlier source wins timing-quality ties because lyric.Prefer keeps the
// current candidate.
func (p *lyricsProvider) readBestFromSourceCache(track lyrics.TrackInfo, order []source.Source) lyric.Candidate {
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

func (p *lyricsProvider) readCachedWinner(track lyrics.TrackInfo, order []source.Source) (lyric.Candidate, bool, bool) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return lyric.Candidate{}, false, false
	}
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		return lyric.Candidate{}, false, false
	}
	var envelope cachedLyricsEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache entry decode failed: %v", err))
		return lyric.Candidate{}, false, false
	}
	if envelope.Credentials != p.lyricsCredentialsFingerprint() {
		// Winner coverage recorded under different credentials does not
		// authorise the current lookup: re-adding a key must re-walk.
		return lyric.Candidate{}, false, false
	}
	return lyric.Candidate{Response: envelope.Response, Quality: envelope.Quality}, true, envelope.SweepComplete
}

// cacheWinner stores the selected candidate; negative lookup results live here too
// so a second lookup for the same track with the same source order skips the walk.
// sweepComplete records whether the producing lookup covered every implemented
// enabled source, so cached hits know whether a sweep is still pending.
func (p *lyricsProvider) cacheWinner(track lyrics.TrackInfo, order []source.Source, candidate lyric.Candidate, sweepComplete bool) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		Quality:       candidate.Quality,
		Response:      candidate.Response,
		SweepComplete: sweepComplete,
		Credentials:   p.lyricsCredentialsFingerprint(),
		ExpiresAt:     p.now().Add(time.Duration(winnerCacheTTLSeconds(candidate.Quality)) * time.Second).Unix(),
	})
	if err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache encode failed: %v", err))
		return
	}
	p.storeCandidateBytes(key, value, len(value) > maxCachedLyricsEntryBytes, winnerCacheTTLSeconds(candidate.Quality))
}

func (p *lyricsProvider) cacheNegativeWinner(track lyrics.TrackInfo, order []source.Source, sweepComplete bool) {
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		return
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		SweepComplete: sweepComplete,
		Credentials:   p.lyricsCredentialsFingerprint(),
		ExpiresAt:     p.now().Add(time.Duration(negativeLyricsCacheTTLSeconds) * time.Second).Unix(),
	})
	if err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache encode failed: %v", err))
		return
	}
	p.storeCandidateBytes(key, value, len(value) > maxCachedLyricsEntryBytes, negativeLyricsCacheTTLSeconds)
}

// Cooldown reports the remaining provider-wide cooldown for a key; a cache
// failure or corrupt value fails open.
func (p *lyricsProvider) Cooldown(key string) time.Duration {
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		// A cache failure must not make the lyrics provider unavailable.
		//nolint:nilerr // Cooldown enforcement intentionally fails open.
		return 0
	}

	untilUnix, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		//nolint:nilerr // Ignore a corrupt cooldown value and keep serving requests.
		return 0
	}
	remaining := time.Unix(untilUnix, 0).Sub(p.now())
	if remaining <= 0 {
		return 0
	}
	seconds := durationSecondsCeil(remaining)
	return time.Duration(seconds) * time.Second
}

// RememberCooldown persists a cooldown deadline so sibling requests and
// processes observe the same window.
func (p *lyricsProvider) RememberCooldown(key string, retryAfter time.Duration) {
	now := p.now()
	deadline := now.Add(retryAfter)
	untilUnix := deadline.Unix()
	if deadline.Nanosecond() != 0 {
		// The deadline is stored as whole Unix seconds, so round up rather than shorten Retry-After.
		untilUnix++
	}
	ttlSeconds := durationSecondsCeil(time.Unix(untilUnix, 0).Sub(now))
	if err := p.cache.SetWithTTL(key, []byte(strconv.FormatInt(untilUnix, 10)), ttlSeconds); err != nil {
		logCacheDebug(fmt.Sprintf("rate-limit cooldown persistence failed: %v", err))
	}
}

func durationSecondsCeil(duration time.Duration) int64 {
	return int64((duration + time.Second - 1) / time.Second)
}
