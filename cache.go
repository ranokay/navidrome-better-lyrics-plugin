package main

import (
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
	lyricsCacheMaxSize                   = "32MB"
	lyricsCacheMaxBytes            int64 = 32 * 1024 * 1024
	lyricsCacheReservedBytes       int64 = 1 * 1024 * 1024
	maxCachedLyricsEntryBytes            = 1 * 1024 * 1024
	positiveLyricsCacheTTLSeconds        = int64(24 * time.Hour / time.Second)
	uncertainLyricsCacheTTLSeconds       = int64(time.Hour / time.Second)
	degradedLyricsCacheTTLSeconds        = int64(5 * time.Minute / time.Second)
	negativeLyricsCacheTTLSeconds        = int64(5 * time.Minute / time.Second)
	defaultRateLimitCooldown             = 30 * time.Second
	defaultUnisonRateLimitCooldown       = 60 * time.Second
	maxRateLimitCooldown                 = time.Hour
	lyricsCacheKeyPrefix                 = "lyrics:v3:"
	betterLyricsGlobalCooldownKey        = "rate-limit:better-lyrics:global:v3"
	betterLyricsQueryCooldownKey         = "rate-limit:better-lyrics:query:v3:"
	unisonGlobalCooldownKey              = "rate-limit:unison:global:v1"
)

type lookupHealth uint8

const (
	lookupHealthy lookupHealth = iota
	lookupUncertain
	lookupDegraded
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

func newBetterLyricsProviderWithDependencies(send requestSender, cache lyricsCacheStore, now func() time.Time) *betterLyricsProvider {
	if cache == nil {
		cache = noopLyricsCache{}
	}
	if now == nil {
		now = time.Now
	}
	return &betterLyricsProvider{send: send, cache: cache, now: now}
}

func lyricsCacheKey(track lyrics.TrackInfo) (string, bool) {
	title := strings.ToLower(strings.TrimSpace(track.Title))
	artist := strings.ToLower(trackArtist(track))
	if title == "" || artist == "" {
		return "", false
	}

	album := strings.ToLower(strings.TrimSpace(track.Album))
	duration := int64(math.Round(float64(track.Duration)))
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", title, artist, album, duration)))
	return fmt.Sprintf("%s%x", lyricsCacheKeyPrefix, digest), true
}

func (p *betterLyricsProvider) readCachedLyrics(track lyrics.TrackInfo) (lyrics.GetLyricsResponse, bool) {
	key, ok := lyricsCacheKey(track)
	if !ok {
		return lyrics.GetLyricsResponse{}, false
	}

	value, exists, err := p.cache.Get(key)
	if err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache read failed: %v", err))
		return lyrics.GetLyricsResponse{}, false
	}
	if !exists {
		return lyrics.GetLyricsResponse{}, false
	}

	var response lyrics.GetLyricsResponse
	if err := json.Unmarshal(value, &response); err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache entry decode failed: %v", err))
		return lyrics.GetLyricsResponse{}, false
	}
	return response, true
}

func (p *betterLyricsProvider) cacheLyrics(track lyrics.TrackInfo, response lyrics.GetLyricsResponse, health lookupHealth) {
	key, ok := lyricsCacheKey(track)
	if !ok {
		return
	}

	ttl := positiveLyricsCacheTTLSeconds
	if len(response.Lyrics) == 0 {
		ttl = negativeLyricsCacheTTLSeconds
	} else {
		switch health {
		case lookupDegraded:
			ttl = degradedLyricsCacheTTLSeconds
		case lookupUncertain:
			ttl = uncertainLyricsCacheTTLSeconds
		}
	}
	value, err := json.Marshal(response)
	if err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache encode failed: %v", err))
		return
	}
	if len(value) > maxCachedLyricsEntryBytes {
		logCacheDebug(fmt.Sprintf("lyrics cache entry skipped: %d bytes exceeds %d-byte limit", len(value), maxCachedLyricsEntryBytes))
		return
	}
	if usageStore, ok := p.cache.(lyricsCacheUsageStore); ok {
		used, err := usageStore.StorageUsed()
		if err != nil {
			logCacheDebug(fmt.Sprintf("lyrics cache usage lookup failed: %v", err))
		} else if used >= lyricsCacheMaxBytes-lyricsCacheReservedBytes || used+int64(len(value)) > lyricsCacheMaxBytes-lyricsCacheReservedBytes {
			logCacheDebug("lyrics cache entry skipped to preserve rate-limit storage headroom")
			return
		}
	}
	if err := p.cache.SetWithTTL(key, value, ttl); err != nil {
		logCacheDebug(fmt.Sprintf("lyrics cache write failed: %v", err))
	}
}

func (p *betterLyricsProvider) activeBetterLyricsCooldown(requestURL string) error {
	if remaining := p.activeCooldown(betterLyricsGlobalCooldownKey); remaining > 0 {
		return &rateLimitCooldownError{global: true, remaining: remaining}
	}
	if remaining := p.activeCooldown(rateLimitQueryKey(requestURL)); remaining > 0 {
		return &rateLimitCooldownError{remaining: remaining}
	}
	return nil
}

func (p *betterLyricsProvider) activeUnisonCooldown() error {
	if remaining := p.activeCooldown(unisonGlobalCooldownKey); remaining > 0 {
		return &providerCooldownError{provider: "unison", remaining: remaining}
	}
	return nil
}

func (p *betterLyricsProvider) activeCooldown(key string) time.Duration {
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

func (p *betterLyricsProvider) rememberRateLimit(response *host.HTTPResponse, requestURL string) time.Duration {
	retryAfter := retryAfterDuration(response.Headers, p.now())
	key := rateLimitQueryKey(requestURL)
	if globalRateLimit(response) {
		key = betterLyricsGlobalCooldownKey
	}
	p.rememberCooldown(key, retryAfter)
	return retryAfter
}

func (p *betterLyricsProvider) rememberUnisonRateLimit(response *host.HTTPResponse) time.Duration {
	retryAfter := retryAfterDurationWithDefault(response.Headers, p.now(), defaultUnisonRateLimitCooldown)
	p.rememberCooldown(unisonGlobalCooldownKey, retryAfter)
	return retryAfter
}

func (p *betterLyricsProvider) rememberCooldown(key string, retryAfter time.Duration) {
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
