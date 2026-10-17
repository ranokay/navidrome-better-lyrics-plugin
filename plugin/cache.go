package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
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
	unsyncedLyricsCacheTTLSeconds  = int64(time.Hour / time.Second)
	negativeLyricsCacheTTLSeconds  = int64(5 * time.Minute / time.Second)
	defaultRateLimitCooldown       = 30 * time.Second
	defaultUnisonRateLimitCooldown = 60 * time.Second
	maxRateLimitCooldown           = time.Hour
	lyricsWinnerCacheKeyPrefix     = "lyrics:v4:winner:"
	lyricsSourceCacheKeyPrefix     = "lyrics:v4:source:"
	betterLyricsGlobalCooldownKey  = "rate-limit:better-lyrics:global:v3"
	betterLyricsQueryCooldownKey   = "rate-limit:better-lyrics:query:v3:"
	unisonGlobalCooldownKey        = "rate-limit:unison:global:v1"
)

type lookupHealth uint8

const (
	lookupHealthy lookupHealth = iota
	lookupUncertain
	lookupDegraded
	// lookupTemporarilySkipped marks a token/config rejection: not covered
	// work, not a miss, retryable as soon as the credential situation changes
	// (better-lyrics family anonymous 401s stay uncertain and miss-cache like
	// the bounded miss coverage callers expect).
	lookupTemporarilySkipped
)

func (h lookupHealth) merge(other lookupHealth) lookupHealth {
	if other > h {
		return other
	}
	return h
}

type lyricsCacheStore interface {
	Get(key string) ([]byte, bool, error)
	SetWithTTL(key string, value []byte, ttlSeconds int64) error
}

type lyricsCacheUsageStore interface {
	StorageUsed() (int64, error)
}

type noopLyricsCache struct{}

type rateLimitCooldownError struct {
	global    bool
	remaining time.Duration
}

type providerCooldownError struct {
	provider  string
	remaining time.Duration
}

func (e *rateLimitCooldownError) Error() string {
	scope := "request"
	if e.global {
		scope = "API"
	}
	return fmt.Sprintf("better lyrics %s cooldown active for %s", scope, e.remaining)
}

func (e *providerCooldownError) Error() string {
	return fmt.Sprintf("%s API cooldown active for %s", e.provider, e.remaining)
}

func (noopLyricsCache) Get(string) ([]byte, bool, error) {
	return nil, false, nil
}

func (noopLyricsCache) SetWithTTL(string, []byte, int64) error {
	return nil
}

func newLyricsProviderWithDependencies(send requestSender, cache lyricsCacheStore, now func() time.Time) *lyricsProvider {
	provider := newLyricsProviderWithDeps(send, cache, now, defaultConfigLookup(), defaultSweepEnqueuer())
	if cache == nil {
		cache = noopLyricsCache{}
		provider.cache = cache
	}
	return provider
}

func lyricsTrackKey(track lyrics.TrackInfo) (string, bool) {
	title := strings.ToLower(strings.TrimSpace(track.Title))
	artist := strings.ToLower(trackArtist(track))
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
	Quality       lyricTimingQuality       `json:"quality"`
	Response      lyrics.GetLyricsResponse `json:"response"`
	SweepComplete bool                     `json:"sweepComplete,omitempty"`
	Credentials   string                   `json:"credentials,omitempty"`
}

// winnerCacheTLSeconds maps a candidate's timing quality to the winner entry TTL.
func winnerCacheTTLSeconds(quality lyricTimingQuality) int64 {
	switch quality {
	case timingSyllable:
		return syncedLyricsCacheTTLSeconds
	case timingWord, timingLine:
		return unsyncedLyricsCacheTTLSeconds
	default:
		return negativeLyricsCacheTTLSeconds
	}
}

func sourceCacheTTLSeconds(quality lyricTimingQuality) int64 {
	if quality >= timingLine {
		return syncedLyricsCacheTTLSeconds
	}
	return unsyncedLyricsCacheTTLSeconds
}

func (p *lyricsProvider) readCachedCandidate(key string) (lyricsCandidate, bool) {
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		return lyricsCandidate{}, false
	}
	var envelope cachedLyricsEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache entry decode failed: %v", err))
		return lyricsCandidate{}, false
	}
	return lyricsCandidate{response: envelope.Response, quality: envelope.Quality}, true
}

func (p *lyricsProvider) storeCandidateValue(key string, candidate lyricsCandidate, ttl int64) bool {
	value, err := json.Marshal(cachedLyricsEnvelope{Quality: candidate.quality, Response: candidate.response})
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

func (p *lyricsProvider) readCachedSourceCandidate(track lyrics.TrackInfo, sourceID string) (lyricsCandidate, bool) {
	key, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok {
		return lyricsCandidate{}, false
	}
	return p.readCachedCandidate(key)
}

// missingCoverageEnvelope is the serialized negative per-source entry: an
// unsynced-quality envelope with no lyrics, decodable by readCachedCandidate
// as a present=false-but-covered sentinel.
const missingCoverageEnvelope = `{"quality":0}`

// cacheCredentialPepper keys the credential-space HMAC. The namespace needs a
// stable, non-leaking fingerprint of secret material; a keyed MAC is the
// standard construction for that (and coded caching of secrets through a raw
// hash is flagged as weak-password storage).
var cacheCredentialPepper = []byte("navidrome-better-lyrics/credential-fingerprint/v1")

// lyricsCredentialsFingerprint distinguishes negative coverage stamped under
// different Better Lyrics API key / Musixmatch token configurations: adding or
// replacing a credential must re-authorize sources whose misses were recorded
// anonymously (Codex).
func (p *lyricsProvider) lyricsCredentialsFingerprint() string {
	config := readProviderConfig(p.configLookup)
	mac := hmac.New(sha256.New, cacheCredentialPepper)
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
	if p.cache.SetWithTTL(key, []byte(missingCoverageEnvelope), unsyncedLyricsCacheTTLSeconds) != nil {
		return false
	}
	// An oversized/evicted write must not count as coverage.
	_, exists, _ := p.cache.Get(key)
	return exists
}

// readCachedSourceCoverage reads whichever per-source entry exists for a
// source: a positive candidate first, otherwise the credential-namespaced
// negative entry the sweep and stage-one misses persist.
func (p *lyricsProvider) readCachedSourceCoverage(track lyrics.TrackInfo, sourceID string) (lyricsCandidate, bool) {
	if candidate, ok := p.readCachedSourceCandidate(track, sourceID); ok {
		return candidate, true
	}
	key, ok := p.sourceMissKey(track, sourceID)
	if !ok {
		return lyricsCandidate{}, false
	}
	return p.readCachedCandidate(key)
}

// cacheSourceCandidate persists one per-source result and reports whether the
// write actually landed, so the sweep only counts entries it really cached.
func (p *lyricsProvider) cacheSourceCandidate(track lyrics.TrackInfo, sourceID string, candidate lyricsCandidate) bool {
	key, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok || !candidate.present() {
		return false
	}
	return p.storeCandidateValue(key, candidate, sourceCacheTTLSeconds(candidate.quality))
}

// readBestFromSourceCache ranks fresh per-source results in configured order; the
// earlier source wins timing-quality ties because preferLyricsCandidate keeps the
// current candidate.
func (p *lyricsProvider) readBestFromSourceCache(track lyrics.TrackInfo, order []lyricsSource) lyricsCandidate {
	var best lyricsCandidate
	for _, source := range order {
		candidate, ok := p.readCachedSourceCandidate(track, source.ID)
		if !ok {
			continue
		}
		best = preferLyricsCandidate(best, candidate)
	}
	return best
}

func (p *lyricsProvider) readCachedWinner(track lyrics.TrackInfo, order []lyricsSource) (lyricsCandidate, bool, bool) {
	key, ok := lyricsWinnerCacheKey(track, sourceIDs(order))
	if !ok {
		return lyricsCandidate{}, false, false
	}
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		return lyricsCandidate{}, false, false
	}
	var envelope cachedLyricsEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache entry decode failed: %v", err))
		return lyricsCandidate{}, false, false
	}
	if envelope.Credentials != p.lyricsCredentialsFingerprint() {
		// Winner coverage recorded under different credentials does not
		// authorise the current lookup: re-adding a key must re-walk (Greptile).
		return lyricsCandidate{}, false, false
	}
	return lyricsCandidate{response: envelope.Response, quality: envelope.Quality}, true, envelope.SweepComplete
}

// cacheWinner stores the selected candidate; negative lookup results live here too
// so a second lookup for the same track with the same source order skips the walk.
// sweepComplete records whether the producing lookup covered every implemented
// enabled source, so cached hits know whether a sweep is still pending.
func (p *lyricsProvider) cacheWinner(track lyrics.TrackInfo, order []lyricsSource, candidate lyricsCandidate, sweepComplete bool) {
	key, ok := lyricsWinnerCacheKey(track, sourceIDs(order))
	if !ok {
		return
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		Quality:       candidate.quality,
		Response:      candidate.response,
		SweepComplete: sweepComplete,
		Credentials:   p.lyricsCredentialsFingerprint(),
	})
	if err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache encode failed: %v", err))
		return
	}
	p.storeCandidateBytes(key, value, len(value) > maxCachedLyricsEntryBytes, winnerCacheTTLSeconds(candidate.quality))
}

func (p *lyricsProvider) cacheNegativeWinner(track lyrics.TrackInfo, order []lyricsSource, sweepComplete bool) {
	key, ok := lyricsWinnerCacheKey(track, sourceIDs(order))
	if !ok {
		return
	}
	value, err := json.Marshal(cachedLyricsEnvelope{SweepComplete: sweepComplete, Credentials: p.lyricsCredentialsFingerprint()})
	if err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache encode failed: %v", err))
		return
	}
	p.storeCandidateBytes(key, value, len(value) > maxCachedLyricsEntryBytes, negativeLyricsCacheTTLSeconds)
}

func (p *lyricsProvider) activeBetterLyricsCooldown(requestURL string) error {
	if remaining := p.activeCooldown(betterLyricsGlobalCooldownKey); remaining > 0 {
		return &rateLimitCooldownError{global: true, remaining: remaining}
	}
	if remaining := p.activeCooldown(rateLimitQueryKey(requestURL)); remaining > 0 {
		return &rateLimitCooldownError{remaining: remaining}
	}
	return nil
}

func (p *lyricsProvider) activeUnisonCooldown() error {
	if remaining := p.activeCooldown(unisonGlobalCooldownKey); remaining > 0 {
		return &providerCooldownError{provider: "unison", remaining: remaining}
	}
	return nil
}

func (p *lyricsProvider) activeCooldown(key string) time.Duration {
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

func (p *lyricsProvider) rememberRateLimit(response *host.HTTPResponse, requestURL string) time.Duration {
	retryAfter := retryAfterDuration(response.Headers, p.now())
	key := rateLimitQueryKey(requestURL)
	if globalRateLimit(response) {
		key = betterLyricsGlobalCooldownKey
	}
	p.rememberCooldown(key, retryAfter)
	return retryAfter
}

func (p *lyricsProvider) rememberUnisonRateLimit(response *host.HTTPResponse) time.Duration {
	retryAfter := retryAfterDurationWithDefault(response.Headers, p.now(), defaultUnisonRateLimitCooldown)
	p.rememberCooldown(unisonGlobalCooldownKey, retryAfter)
	return retryAfter
}

func (p *lyricsProvider) rememberCooldown(key string, retryAfter time.Duration) {
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

func rateLimitQueryKey(requestURL string) string {
	digest := sha256.Sum256([]byte(requestURL))
	return fmt.Sprintf("%s%x", betterLyricsQueryCooldownKey, digest)
}

func globalRateLimit(response *host.HTTPResponse) bool {
	limitType := strings.ToLower(strings.TrimSpace(headerValue(response.Headers, "X-RateLimit-Type")))
	if limitType != "" {
		return limitType == "exceeded"
	}
	body := strings.ToLower(string(response.Body))
	return !strings.Contains(body, "no cached data") && !strings.Contains(body, "requires cached data")
}

func retryAfterDuration(headers map[string]string, now time.Time) time.Duration {
	return retryAfterDurationWithDefault(headers, now, defaultRateLimitCooldown)
}

func retryAfterDurationWithDefault(headers map[string]string, now time.Time, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(headerValue(headers, "Retry-After"))
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		maxSeconds := int64(maxRateLimitCooldown / time.Second)
		if seconds > maxSeconds {
			seconds = maxSeconds
		}
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
		return min(retryAt.Sub(now), maxRateLimitCooldown)
	}
	return min(fallback, maxRateLimitCooldown)
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func durationSecondsCeil(duration time.Duration) int64 {
	return int64((duration + time.Second - 1) / time.Second)
}
