package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"net/url"
	"strings"
	"testing"
	"time"
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

func TestBetterLyricsProvider_SharesRetryAfterCooldownAcrossInstances(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 6, 0, 0, 0, time.UTC)
	cache := newFakeLyricsCache()
	firstRequests := 0
	first := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		firstRequests++
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host == "unison.boidu.dev" {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{
			StatusCode: 429,
			Headers: map[string]string{
				"Retry-After":      "12",
				"X-RateLimit-Type": "exceeded",
			},
			Body: []byte(`{"error":"Rate limit exceeded"}`),
		}, nil
	}, cache, func() time.Time { return now })

	_, err := first.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "First", Artist: "Artist"}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("first GetLyrics() error = %v, want HTTP 429", err)
	}
	if firstRequests != 2 {
		t.Fatalf("first request count = %d, want Better Lyrics then Unison with Kugou suppressed", firstRequests)
	}
	if ttl := cache.lastTTL(); ttl != 12 {
		t.Fatalf("cooldown TTL = %d, want Retry-After value 12", ttl)
	}

	secondRequests := 0
	second := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		secondRequests++
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host != "unison.boidu.dev" {
			t.Fatalf("request during shared cooldown = %s, want Unison only", request.URL)
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	}, cache, func() time.Time { return now })

	_, err = second.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Second", Artist: "Artist"}})
	if err == nil || !strings.Contains(err.Error(), "cooldown active") {
		t.Fatalf("second GetLyrics() error = %v, want active cooldown", err)
	}
	if secondRequests != 1 {
		t.Fatalf("second request count = %d, want one Unison request", secondRequests)
	}
}
