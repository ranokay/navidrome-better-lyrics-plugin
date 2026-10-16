package main

import (
	"net/url"
	"sync"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func assertRequestEndpoint(t *testing.T, request host.HTTPRequest, want string) {
	t.Helper()

	parsed, err := url.Parse(request.URL)
	if err != nil {
		t.Fatalf("parse request URL: %v", err)
	}
	if got := parsed.Scheme + "://" + parsed.Host + parsed.Path; got != want {
		t.Fatalf("request endpoint = %q, want %q", got, want)
	}
}

func assertLyricsSource(t *testing.T, source *lyrics.LyricsSource, wantProvider, wantFormat string) {
	t.Helper()
	if source == nil {
		t.Fatalf("GetLyrics() source = nil, want provider %q and format %q", wantProvider, wantFormat)
	}
	if source.Provider != wantProvider || source.Format != wantFormat {
		t.Fatalf(
			"GetLyrics() source = %#v, want provider %q and format %q",
			source,
			wantProvider,
			wantFormat,
		)
	}
}

type fakeLyricsCache struct {
	mu         sync.Mutex
	values     map[string][]byte
	ttls       []int64
	ttlsForKey map[string]int64
	setError   error
}

func newFakeLyricsCache() *fakeLyricsCache {
	return &fakeLyricsCache{values: make(map[string][]byte), ttlsForKey: make(map[string]int64)}
}

func (c *fakeLyricsCache) Get(key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, exists := c.values[key]
	return append([]byte(nil), value...), exists, nil
}

func (c *fakeLyricsCache) SetWithTTL(key string, value []byte, ttlSeconds int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setError != nil {
		return c.setError
	}
	c.values[key] = append([]byte(nil), value...)
	c.ttls = append(c.ttls, ttlSeconds)
	c.ttlsForKey[key] = ttlSeconds
	return nil
}

func (c *fakeLyricsCache) lastTTL() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ttls) == 0 {
		return 0
	}
	return c.ttls[len(c.ttls)-1]
}

// winnerTTL returns the TTL recorded for the winner cache entry of a track under a
// source order; per-source and sweep-marker writes are excluded.
func (c *fakeLyricsCache) winnerTTL(track lyrics.TrackInfo, order []lyricsSource) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	key, _ := lyricsWinnerCacheKey(track, sourceIDs(order))
	return c.ttlsForKey[key]
}

// sourceTTL returns the TTL recorded for one per-source cache entry.
func (c *fakeLyricsCache) sourceTTL(track lyrics.TrackInfo, sourceID string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	key, _ := lyricsCacheKeyForSource(track, sourceID)
	return c.ttlsForKey[key]
}
