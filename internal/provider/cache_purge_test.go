package provider

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

type prefixDeletingFakeCache struct {
	*fakeLyricsCache
	deleteError error
}

func (c *prefixDeletingFakeCache) DeleteByPrefix(prefix string) (int64, error) {
	if c.deleteError != nil {
		return 0, c.deleteError
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var deleted int64
	for key := range c.values {
		if strings.HasPrefix(key, prefix) {
			delete(c.values, key)
			deleted++
		}
	}
	return deleted, nil
}

func TestOnInitPurgesRemovedSourceCacheOnce(t *testing.T) {
	t.Parallel()

	cache := &prefixDeletingFakeCache{fakeLyricsCache: newFakeLyricsCache()}
	removed := []string{
		"musixmatch:token:v1",
		lyricsSourceCacheKeyPrefix + "musixmatch:track",
		lyricsSourceCacheKeyPrefix + "musixmatch:track:missing:fp",
		lyricsSourceCacheKeyPrefix + "lyricsplus:track",
		lyricsWinnerCacheKeyPrefix + "fp:track",
	}
	kept := []string{
		credentialSaltKey,
		lyricsSourceCacheKeyPrefix + "lrclib:track",
	}
	for _, key := range append(append([]string{}, removed...), kept...) {
		_ = cache.SetWithTTL(key, []byte("value"), 60)
	}

	p := newTestProviderWithSources(nil, cache, time.Now, nil, func(lyrics.TrackInfo) error { return nil })
	if err := p.OnInit(); err != nil {
		t.Fatalf("OnInit() error = %v", err)
	}
	for _, key := range removed {
		if _, exists, _ := cache.Get(key); exists {
			t.Fatalf("%q survived the purge", key)
		}
	}
	for _, key := range kept {
		if _, exists, _ := cache.Get(key); !exists {
			t.Fatalf("%q was purged; only data that may hold removed-source lyrics may go", key)
		}
	}

	winner := lyricsWinnerCacheKeyPrefix + "fp:fresh"
	_ = cache.SetWithTTL(winner, []byte("value"), 60)
	if err := p.OnInit(); err != nil {
		t.Fatalf("second OnInit() error = %v", err)
	}
	if _, exists, _ := cache.Get(winner); !exists {
		t.Fatal("a later plugin load purged winners again; the purge must run once")
	}
}

func TestOnInitRetriesRemovedSourcePurgeAfterFailure(t *testing.T) {
	t.Parallel()

	cache := &prefixDeletingFakeCache{fakeLyricsCache: newFakeLyricsCache(), deleteError: errors.New("store unavailable")}
	_ = cache.SetWithTTL("musixmatch:token:v1", []byte("token"), 60)
	p := newTestProviderWithSources(nil, cache, time.Now, nil, func(lyrics.TrackInfo) error { return nil })

	if err := p.OnInit(); err != nil {
		t.Fatalf("OnInit() error = %v, want fail open", err)
	}
	if _, done, _ := cache.Get(removedSourcePurgeMarkerKey); done {
		t.Fatal("failed purge stamped its marker")
	}

	cache.deleteError = nil
	if err := p.OnInit(); err != nil {
		t.Fatalf("OnInit() error = %v", err)
	}
	if _, exists, _ := cache.Get("musixmatch:token:v1"); exists {
		t.Fatal("token survived the retried purge")
	}
}
