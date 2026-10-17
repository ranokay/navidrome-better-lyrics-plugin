package main

import (
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// defaultCatalogIDs is the stage-one ordering the deterministic provider tests
// walk when a test does not name a catalog.
var defaultCatalogIDs = []string{"better-lyrics", "unison", "better-lyrics-legato"}

// providerForCatalog builds a provider walking exactly the named catalog ids
// (falling back to the classic stage-one trio) so tests stay deterministic as new
// sources join the default config.
func providerForCatalog(send requestSender, ids ...string) *lyricsProvider {
	return providerForCatalogWithCache(send, newFakeLyricsCache(), time.Now, ids...)
}

// providerForCatalogWithCache is providerForCatalog with an explicit cache and
// clock for TTL and clock assertions.
func providerForCatalogWithCache(send requestSender, cache lyricsCacheStore, now func() time.Time, ids ...string) *lyricsProvider {
	if len(ids) == 0 {
		ids = defaultCatalogIDs
	}
	sources := make([]lyricsSource, 0, len(ids))
	for _, id := range ids {
		source, ok := catalogSource(id)
		if !ok {
			panic("unknown source id " + id)
		}
		sources = append(sources, source)
	}
	return newLyricsProviderWithSources(send, cache, now, sources, func(lyrics.TrackInfo) error { return nil })
}

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

// ptrBool helps build presence-aware optionals in tests.
func ptrBool(value bool) *bool { return &value }
