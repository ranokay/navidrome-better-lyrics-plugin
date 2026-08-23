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
	lyricsCacheMaxSize            = "8MB"
	positiveLyricsCacheTTLSeconds = int64(24 * time.Hour / time.Second)
	negativeLyricsCacheTTLSeconds = int64(5 * time.Minute / time.Second)
	defaultRateLimitCooldown      = 30 * time.Second
	lyricsCacheKeyPrefix          = "lyrics:v1:"
	betterLyricsCooldownKey       = "rate-limit:better-lyrics:v1"
)

type lyricsCacheStore interface {
	Get(key string) ([]byte, bool, error)
	SetWithTTL(key string, value []byte, ttlSeconds int64) error
}

type noopLyricsCache struct{}

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
	if err != nil || !exists {
		return lyrics.GetLyricsResponse{}, false
	}

	var response lyrics.GetLyricsResponse
	if err := json.Unmarshal(value, &response); err != nil {
		return lyrics.GetLyricsResponse{}, false
	}
	return response, true
}

func (p *betterLyricsProvider) cacheLyrics(track lyrics.TrackInfo, response lyrics.GetLyricsResponse) {
	key, ok := lyricsCacheKey(track)
	if !ok {
		return
	}

	ttl := positiveLyricsCacheTTLSeconds
	if len(response.Lyrics) == 0 {
		ttl = negativeLyricsCacheTTLSeconds
	}
	value, err := json.Marshal(response)
	if err != nil {
		return
	}
	_ = p.cache.SetWithTTL(key, value, ttl)
}

func (p *betterLyricsProvider) activeBetterLyricsCooldown() error {
	value, exists, err := p.cache.Get(betterLyricsCooldownKey)
	if err != nil || !exists {
		return nil
	}

	untilUnix, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		return nil
	}
	remaining := time.Unix(untilUnix, 0).Sub(p.now())
	if remaining <= 0 {
		return nil
	}
	seconds := durationSecondsCeil(remaining)
	return fmt.Errorf("Better Lyrics API cooldown active for %s", time.Duration(seconds)*time.Second)
}

func (p *betterLyricsProvider) rememberRateLimit(response *host.HTTPResponse) {
	duration := retryAfterDuration(response.Headers, p.now())
	seconds := durationSecondsCeil(duration)
	until := p.now().Add(time.Duration(seconds) * time.Second).Unix()
	_ = p.cache.SetWithTTL(betterLyricsCooldownKey, []byte(strconv.FormatInt(until, 10)), seconds)
}

func retryAfterDuration(headers map[string]string, now time.Time) time.Duration {
	raw := strings.TrimSpace(headerValue(headers, "Retry-After"))
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return defaultRateLimitCooldown
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
