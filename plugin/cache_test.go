package main

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestBetterLyricsProvider_CachesFallbackResultsBasedOnLookupHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		responses    []*host.HTTPResponse
		wantProvider string
		wantFormat   string
		wantTTL      int64
	}{
		{
			name: "uses the positive TTL after clean misses",
			responses: []*host.HTTPResponse{
				{StatusCode: 404},
				{StatusCode: 404},
				{StatusCode: 404},
				{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]community line","format":"lrc","language":"en"}}`)},
			},
			wantProvider: "unison",
			wantFormat:   "lrc",
			wantTTL:      positiveLyricsCacheTTLSeconds,
		},
		{
			name: "uses a short TTL after a Better Lyrics failure",
			responses: []*host.HTTPResponse{
				{StatusCode: 503, Body: []byte(`{"error":"provider unavailable"}`)},
				{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]community line","format":"lrc","language":"en"}}`)},
			},
			wantProvider: "unison",
			wantFormat:   "lrc",
			wantTTL:      int64(5 * time.Minute / time.Second),
		},
		{
			name: "uses a short TTL for plain text after a Kugou failure",
			responses: []*host.HTTPResponse{
				{StatusCode: 404},
				{StatusCode: 404},
				{StatusCode: 404},
				{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"community plain","format":"plain","language":"en"}}`)},
				{StatusCode: 503, Body: []byte(`{"error":"provider unavailable"}`)},
			},
			wantProvider: "unison",
			wantFormat:   "plain",
			wantTTL:      int64(5 * time.Minute / time.Second),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cache := newFakeLyricsCache()
			requests := 0
			provider := newBetterLyricsProviderWithDependencies(func(host.HTTPRequest) (*host.HTTPResponse, error) {
				if requests >= len(test.responses) {
					t.Fatalf("unexpected request %d", requests+1)
				}
				response := test.responses[requests]
				requests++
				return response, nil
			}, cache, time.Now)

			result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Title: "Song", Artist: "Artist", Album: "Album", Duration: 123,
			}})
			if err != nil {
				t.Fatalf("GetLyrics() error = %v, want nil", err)
			}
			assertLyricsSource(t, result.Source, test.wantProvider, test.wantFormat)
			if requests != len(test.responses) {
				t.Fatalf("GetLyrics() request count = %d, want %d", requests, len(test.responses))
			}
			if ttl := cache.lastTTL(); ttl != test.wantTTL {
				t.Fatalf("fallback cache TTL = %d, want %d", ttl, test.wantTTL)
			}
		})
	}
}

func TestBetterLyricsProvider_CachesCompleteMisses(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	requests := 0
	provider := newBetterLyricsProviderWithDependencies(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 404}, nil
	}, cache, time.Now)
	input := lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Unknown", Artist: "Artist"}}

	for range 2 {
		result, err := provider.GetLyrics(input)
		if err != nil {
			t.Fatalf("GetLyrics() error = %v, want nil", err)
		}
		if len(result.Lyrics) != 0 {
			t.Fatalf("GetLyrics() lyrics = %#v, want empty", result.Lyrics)
		}
	}

	if requests != 3 {
		t.Fatalf("network request count = %d, want 3 from the first lookup only", requests)
	}
	if ttl := cache.lastTTL(); ttl != negativeLyricsCacheTTLSeconds {
		t.Fatalf("negative cache TTL = %d, want %d", ttl, negativeLyricsCacheTTLSeconds)
	}
}

func TestBetterLyricsProvider_UsesUncertainTTLAfterCacheOnly401(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	provider := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host == "lyrics-api.boidu.dev" {
			return &host.HTTPResponse{StatusCode: 401, Body: []byte(`{"error":"API key required"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]community line","format":"lrc","language":"en"}}`)}, nil
	}, cache, time.Now)

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 181,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "unison", "lrc")
	if ttl := cache.lastTTL(); ttl != uncertainLyricsCacheTTLSeconds {
		t.Fatalf("cache TTL = %d, want uncertain TTL %d", ttl, uncertainLyricsCacheTTLSeconds)
	}
}

func TestBetterLyricsProvider_DoesNotCacheOperationalFailures(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	requests := 0
	provider := newBetterLyricsProviderWithDependencies(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 503}, nil
	}, cache, time.Now)
	input := lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}}

	for range 2 {
		_, err := provider.GetLyrics(input)
		if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
			t.Fatalf("GetLyrics() error = %v, want HTTP 503", err)
		}
	}

	if requests != 6 {
		t.Fatalf("network request count = %d, want both three-provider lookups", requests)
	}
	if ttl := cache.lastTTL(); ttl != 0 {
		t.Fatalf("cache TTL = %d, want no cached operational failure", ttl)
	}
}

func TestBetterLyricsProvider_SeparatesCacheKeysByDuration(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	requestedDurations := make([]string, 0, 2)
	provider := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		duration := parsed.Query().Get("d")
		if duration == "" {
			t.Fatalf("request query = %q, want a duration-qualified Better Lyrics lookup", parsed.RawQuery)
		}
		requestedDurations = append(requestedDurations, duration)
		return &host.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">lyrics for duration ` + duration + `</tt>"}`),
		}, nil
	}, cache, time.Now)

	shortResult, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Intro", Artist: "Artist", Album: "Album", Duration: 151,
	}})
	if err != nil {
		t.Fatalf("short GetLyrics() error = %v, want nil", err)
	}
	longResult, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Intro", Artist: "Artist", Album: "Album", Duration: 312,
	}})
	if err != nil {
		t.Fatalf("long GetLyrics() error = %v, want nil", err)
	}

	if len(requestedDurations) != 2 {
		t.Fatalf("request durations = %v, want one network lookup per duration", requestedDurations)
	}
	if !strings.Contains(shortResult.Lyrics[0].Text, "duration 151") || !strings.Contains(longResult.Lyrics[0].Text, "duration 312") {
		t.Fatalf("results = %q / %q, want duration-specific lyrics for each track", shortResult.Lyrics[0].Text, longResult.Lyrics[0].Text)
	}
}

func TestBetterLyricsProvider_FailsOpenWhenLyricCacheWriteFails(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	cache.setError = errors.New("kv store at quota")
	requests := 0
	provider := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host != "lyrics-api.boidu.dev" {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">fresh lyrics</tt>"}`),
		}, nil
	}, cache, time.Now)

	for range 2 {
		result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
			Title: "Song", Artist: "Artist", Album: "Album", Duration: 123,
		}})
		if err != nil {
			t.Fatalf("GetLyrics() error = %v, want nil despite cache write failure", err)
		}
		if len(result.Lyrics) != 1 || !strings.Contains(result.Lyrics[0].Text, "fresh lyrics") {
			t.Fatalf("GetLyrics() lyrics = %#v, want TTML result", result.Lyrics)
		}
	}

	if requests != 2 {
		t.Fatalf("network request count = %d, want both lookups to bypass the broken cache", requests)
	}
}

type usageAwareLyricsCache struct {
	used int64
	sets int
}

func (c *usageAwareLyricsCache) Get(string) ([]byte, bool, error) { return nil, false, nil }
func (c *usageAwareLyricsCache) SetWithTTL(string, []byte, int64) error {
	c.sets++
	return nil
}
func (c *usageAwareLyricsCache) StorageUsed() (int64, error) { return c.used, nil }

func TestBetterLyricsProvider_ReservesKVHeadroomForCooldowns(t *testing.T) {
	t.Parallel()

	cache := &usageAwareLyricsCache{used: lyricsCacheMaxBytes - lyricsCacheReservedBytes}
	provider := newBetterLyricsProviderWithDependencies(nil, cache, time.Now)
	provider.cacheLyrics(
		lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
		sourcedLyricsResponse(lyrics.LyricsText{Text: "lyrics"}, "unison", "plain"),
		lookupHealthy,
	)
	if cache.sets != 0 {
		t.Fatalf("cache writes = %d, want 0 when reserved headroom is reached", cache.sets)
	}
}
