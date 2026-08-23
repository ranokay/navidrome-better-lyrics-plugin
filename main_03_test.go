package main

import (
	"errors"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBetterLyricsProvider_CachedTierRateLimitOnlyCoolsDownTheFailedQuery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 6, 0, 0, 0, time.UTC)
	cache := newFakeLyricsCache()
	requests := make([]host.HTTPRequest, 0, 4)
	provider := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Query().Get("s") == "Wanted" {
			return &host.HTTPResponse{
				StatusCode: 200,
				Body:       []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">wanted lyrics</tt>"}`),
			}, nil
		}
		if parsed.Host == "unison.boidu.dev" {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{
			StatusCode: 429,
			Headers: map[string]string{
				"Retry-After":      "30",
				"X-RateLimit-Type": "cached",
			},
			Body: []byte(`{"error":"Rate limit exceeded. No cached data available."}`),
		}, nil
	}, cache, func() time.Time { return now })

	_, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Skipped", Artist: "Artist"}})
	if err == nil || !strings.Contains(err.Error(), "retry after 30s") {
		t.Fatalf("first GetLyrics() error = %v, want query cooldown retry time", err)
	}

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Wanted", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("wanted GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || result.Lyrics[0].Text == "" {
		t.Fatalf("wanted GetLyrics() lyrics = %#v, want TTML", result.Lyrics)
	}
	if got := requests[len(requests)-1].URL; !strings.Contains(got, "s=Wanted") {
		t.Fatalf("last request = %q, want the different song to reach Better Lyrics", got)
	}
}

func TestBetterLyricsProvider_RetriesAQueryRateLimitWithBroaderMetadata(t *testing.T) {
	t.Parallel()

	requests := make([]host.HTTPRequest, 0, 2)
	provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Query().Has("al") {
			return &host.HTTPResponse{
				StatusCode: 429,
				Headers: map[string]string{
					"Retry-After":      "30",
					"X-RateLimit-Type": "cached",
				},
				Body: []byte(`{"error":"This request requires cached data, but no cache is available for this query."}`),
			}, nil
		}
		return &host.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">broader cached match</tt>"}`),
		}, nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 180,
	}})

	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || !strings.Contains(result.Lyrics[0].Text, "broader cached match") {
		t.Fatalf("GetLyrics() lyrics = %#v, want broader cached TTML", result.Lyrics)
	}
	if len(requests) != 2 {
		t.Fatalf("GetLyrics() request count = %d, want full and duration-only TTML queries", len(requests))
	}
	broader, err := url.Parse(requests[1].URL)
	if err != nil {
		t.Fatalf("parse broader request URL: %v", err)
	}
	if broader.Query().Has("al") || broader.Query().Get("d") != "180" {
		t.Fatalf("broader request query = %q, want duration without album", broader.RawQuery)
	}
}

func TestBetterLyricsProvider_SkipsKugouWhenCooldownPersistenceFails(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	cache.setError = errors.New("cache unavailable")
	requests := 0
	provider := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host == "unison.boidu.dev" {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		if requests > 1 {
			t.Fatalf("request after observed rate limit = %s, want only Unison fallback", request.URL)
		}
		return &host.HTTPResponse{
			StatusCode: 429,
			Headers:    map[string]string{"Retry-After": "12"},
			Body:       []byte(`{"error":"Rate limit exceeded"}`),
		}, nil
	}, cache, time.Now)

	_, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("GetLyrics() error = %v, want HTTP 429", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want Better Lyrics then Unison with Kugou suppressed", requests)
	}
}

func TestBetterLyricsProvider_PreservesFractionalCooldownDeadline(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 6, 0, 0, int(900*time.Millisecond), time.UTC)
	currentTime := now
	cache := newFakeLyricsCache()
	provider := newBetterLyricsProviderWithDependencies(nil, cache, func() time.Time { return currentTime })
	const requestURL = "https://lyrics-api.boidu.dev/getLyrics?s=Song&a=Artist"
	provider.rememberRateLimit(&host.HTTPResponse{
		Headers: map[string]string{"Retry-After": "1", "X-RateLimit-Type": "exceeded"},
	}, requestURL)

	currentTime = now.Add(100 * time.Millisecond)
	if err := provider.activeBetterLyricsCooldown(requestURL); err == nil {
		t.Fatal("cooldown after 100ms = nil, want the one-second Retry-After to remain active")
	}
	if ttl := cache.lastTTL(); ttl != 2 {
		t.Fatalf("fractional cooldown TTL = %d, want 2 seconds to cover the rounded deadline", ttl)
	}

	currentTime = now.Add(1100 * time.Millisecond)
	if err := provider.activeBetterLyricsCooldown(requestURL); err != nil {
		t.Fatalf("cooldown after rounded deadline = %v, want nil", err)
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
