package provider

import (
	"encoding/base64"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// testUserAgent stands in for the entrypoint's versioned User-Agent.
const testUserAgent = "NavidromeBetterLyricsPlugin/test (+https://example.test)"

// newTestProviderWithDeps builds a provider with every dependency injectable;
// nil falls back to the inert defaults New applies.
func newTestProviderWithDeps(send requestSender, cache Store, now func() time.Time, config ConfigLookup, enqueue func(lyrics.TrackInfo) error) *Provider {
	p := New(Deps{Send: send, Store: cache, Now: now, Config: config, UserAgent: testUserAgent})
	if enqueue != nil {
		p.enqueueSweep = enqueue
	}
	return p
}

// newTestProviderWithSources bypasses the config service for source-walk tests
// and injects stub sources directly.
func newTestProviderWithSources(send requestSender, cache Store, now func() time.Time, sources []source.Source, enqueue func(lyrics.TrackInfo) error) *Provider {
	p := newTestProviderWithDeps(send, cache, now, nil, enqueue)
	p.sources = sources
	return p
}

// newTestProviderWithDependencies builds a provider over the production-shaped
// defaults for tests driving the config and sweep-enqueue paths.
func newTestProviderWithDependencies(send requestSender, cache Store, now func() time.Time) *Provider {
	return newTestProviderWithDeps(send, cache, now, nil, nil)
}

// catalogSources resolves catalog ids into walkable sources, skipping unknown
// ids so tests stay deterministic as the catalog changes.
func catalogSources(ids []string) []source.Source {
	sources := make([]source.Source, 0, len(ids))
	for _, id := range ids {
		if src, ok := source.ByID(id); ok {
			sources = append(sources, src)
		}
	}
	return sources
}

// hostXMLResponse is the 200/XML response shape the source tests serve.
func hostXMLResponse(body string) *host.HTTPResponse {
	return &host.HTTPResponse{StatusCode: 200, Body: []byte(body)}
}

// base64Encode encodes fixture payloads the way the providers ship them.
func base64Encode(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

// defaultCatalogIDs is the stage-one ordering the deterministic provider tests
// walk when a test does not name a catalog.
var defaultCatalogIDs = []string{"better-lyrics", "unison", "better-lyrics-legato"}

// providerForCatalog builds a provider walking exactly the named catalog ids
// (falling back to the classic stage-one trio) so tests stay deterministic as new
// sources join the default config.
func providerForCatalog(send requestSender, ids ...string) *Provider {
	return providerForCatalogWithCache(send, newFakeLyricsCache(), time.Now, ids...)
}

// providerForCatalogWithCache is providerForCatalog with an explicit cache and
// clock for TTL and clock assertions.
func providerForCatalogWithCache(send requestSender, cache Store, now func() time.Time, ids ...string) *Provider {
	if len(ids) == 0 {
		ids = defaultCatalogIDs
	}
	sources := make([]source.Source, 0, len(ids))
	for _, id := range ids {
		src, ok := source.ByID(id)
		if !ok {
			panic("unknown source id " + id)
		}
		sources = append(sources, src)
	}
	return newTestProviderWithSources(send, cache, now, sources, func(lyrics.TrackInfo) error { return nil })
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

// winnerText returns the first (winning) variant's text and language under the
// per-source variant contract: responses may carry several source-tagged
// entries, so ordering tests assert on the winner instead of the entry count.
func winnerText(t *testing.T, result lyrics.GetLyricsResponse) (string, string) {
	t.Helper()
	if len(result.Lyrics) == 0 {
		t.Fatalf("GetLyrics() returned no lyrics, want at least the winner")
	}
	return result.Lyrics[0].Text, result.Lyrics[0].Lang
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
func (c *fakeLyricsCache) winnerTTL(track lyrics.TrackInfo, order []source.Source) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	key, _ := lyricsWinnerCacheKey(track, source.IDs(order))
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
