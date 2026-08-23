package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
)

func TestBetterLyricsProvider_GetLyrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		track        lyrics.TrackInfo
		response     *host.HTTPResponse
		sendError    error
		wantText     string
		wantProvider string
		wantFormat   string
		wantError    string
		wantRequests int
	}{
		{
			name:         "returns TTML unchanged",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"  <tt xml:lang=\"en\">timed text</tt>\n"}`)},
			wantText:     "  <tt xml:lang=\"en\">timed text</tt>\n",
			wantProvider: "ttml",
			wantFormat:   "ttml",
			wantRequests: 1,
		},
		{
			name:         "treats not found as no match",
			track:        lyrics.TrackInfo{Title: "Unknown", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 404, Body: []byte(`{"error":"Lyrics not available"}`)},
			wantRequests: 3,
		},
		{
			name:         "treats empty TTML as no match",
			track:        lyrics.TrackInfo{Title: "Instrumental", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"  "}`)},
			wantRequests: 3,
		},
		{
			name:         "rejects malformed success response",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":`)},
			wantError:    "decode Better Lyrics API response",
			wantRequests: 3,
		},
		{
			name:         "treats unauthenticated cache miss as no match",
			track:        lyrics.TrackInfo{Title: "Uncached", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 401, Body: []byte(`{"error":"API key required"}`)},
			wantRequests: 3,
		},
		{
			name:         "surfaces invalid request response",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 422, Body: []byte(`{"error":"Invalid track metadata"}`)},
			wantError:    "HTTP 422: Invalid track metadata",
			wantRequests: 3,
		},
		{
			name:         "surfaces rate limit failure",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 429, Body: []byte(`{"message":"Please try again later"}`)},
			wantError:    "HTTP 429: Please try again later",
			wantRequests: 2,
		},
		{
			name:         "surfaces server status without echoing invalid body",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 503, Body: []byte(`not-json`)},
			wantError:    "better lyrics API returned HTTP 503",
			wantRequests: 3,
		},
		{
			name:         "surfaces transport failure",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			sendError:    errors.New("network unavailable"),
			wantError:    "request Better Lyrics API: network unavailable",
			wantRequests: 3,
		},
		{
			name:         "rejects nil transport response",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			wantError:    "better lyrics API returned no response",
			wantRequests: 3,
		},
		{
			name:         "skips incomplete track metadata",
			track:        lyrics.TrackInfo{Title: "Song"},
			wantRequests: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
				requests++
				parsed, err := url.Parse(request.URL)
				if err != nil {
					t.Fatalf("parse request URL: %v", err)
				}
				if parsed.Host == "lyrics-api.boidu.dev" && parsed.Path == "/getLyrics" {
					return test.response, test.sendError
				}
				return &host.HTTPResponse{StatusCode: 404}, nil
			})

			result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: test.track})
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("GetLyrics() error = %v, want nil", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("GetLyrics() error = %v, want containing %q", err, test.wantError)
			}

			if requests != test.wantRequests {
				t.Fatalf("GetLyrics() request count = %d, want %d", requests, test.wantRequests)
			}
			if test.wantText == "" {
				if len(result.Lyrics) != 0 {
					t.Fatalf("GetLyrics() lyrics = %#v, want empty", result.Lyrics)
				}
				return
			}
			if len(result.Lyrics) != 1 || result.Lyrics[0].Text != test.wantText {
				t.Fatalf("GetLyrics() lyrics = %#v, want one raw TTML entry %q", result.Lyrics, test.wantText)
			}
			assertLyricsSource(t, result.Source, test.wantProvider, test.wantFormat)
		})
	}
}

func TestBetterLyricsProvider_CachesSuccessfulResultsAcrossInstances(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	requests := 0
	first := newBetterLyricsProviderWithDependencies(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"ttml":"<tt>cached lyrics</tt>"}`),
		}, nil
	}, cache, time.Now)

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 123}
	firstResult, err := first.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}

	second := newBetterLyricsProviderWithDependencies(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		t.Fatal("second GetLyrics() made a network request, want shared cache hit")
		return nil, nil
	}, cache, time.Now)
	secondResult, err := second.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("second GetLyrics() error = %v, want nil", err)
	}

	if requests != 1 {
		t.Fatalf("network request count = %d, want 1", requests)
	}
	if len(firstResult.Lyrics) != 1 || len(secondResult.Lyrics) != 1 || secondResult.Lyrics[0].Text != firstResult.Lyrics[0].Text {
		t.Fatalf("cached result = %#v, want %#v", secondResult, firstResult)
	}
	assertLyricsSource(t, secondResult.Source, "ttml", "ttml")
	if ttl := cache.lastTTL(); ttl != positiveLyricsCacheTTLSeconds {
		t.Fatalf("positive cache TTL = %d, want %d", ttl, positiveLyricsCacheTTLSeconds)
	}
}

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
			Headers:    map[string]string{"Retry-After": "12"},
			Body:       []byte(`{"error":"Rate limit exceeded"}`),
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
	provider.rememberRateLimit(&host.HTTPResponse{Headers: map[string]string{"Retry-After": "1"}})

	currentTime = now.Add(100 * time.Millisecond)
	if err := provider.activeBetterLyricsCooldown(); err == nil {
		t.Fatal("cooldown after 100ms = nil, want the one-second Retry-After to remain active")
	}
	if ttl := cache.lastTTL(); ttl != 2 {
		t.Fatalf("fractional cooldown TTL = %d, want 2 seconds to cover the rounded deadline", ttl)
	}

	currentTime = now.Add(1100 * time.Millisecond)
	if err := provider.activeBetterLyricsCooldown(); err != nil {
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

func TestRetryAfterDuration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 6, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "seconds", header: "12", want: 12 * time.Second},
		{name: "HTTP date", header: now.Add(45 * time.Second).Format(http.TimeFormat), want: 45 * time.Second},
		{name: "missing", want: defaultRateLimitCooldown},
		{name: "invalid", header: "later", want: defaultRateLimitCooldown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := retryAfterDuration(map[string]string{"retry-after": test.header}, now); got != test.want {
				t.Fatalf("retryAfterDuration() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestBetterLyricsProvider_GetLyricsBuildsRequest(t *testing.T) {
	t.Parallel()

	var request host.HTTPRequest
	provider := newBetterLyricsProvider(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
		request = input
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"<tt>lyrics</tt>"}`)}, nil
	})

	_, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title:    " Café & Tea ",
		Artist:   " Artist/Guest ",
		Album:    " Album + Deluxe ",
		Duration: 234.6,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}

	parsedURL, err := url.Parse(request.URL)
	if err != nil {
		t.Fatalf("parse request URL: %v", err)
	}
	if parsedURL.Scheme+"://"+parsedURL.Host+parsedURL.Path != apiEndpoint {
		t.Fatalf("request endpoint = %q, want %q", parsedURL.Scheme+"://"+parsedURL.Host+parsedURL.Path, apiEndpoint)
	}
	wantQuery := map[string]string{
		"s":  "Café & Tea",
		"a":  "Artist/Guest",
		"al": "Album + Deluxe",
		"d":  "235",
	}
	for key, want := range wantQuery {
		if got := parsedURL.Query().Get(key); got != want {
			t.Errorf("query %q = %q, want %q", key, got, want)
		}
	}
	if request.Method != "GET" {
		t.Errorf("request method = %q, want GET", request.Method)
	}
	if request.Headers["Accept"] != "application/json" {
		t.Errorf("Accept header = %q, want application/json", request.Headers["Accept"])
	}
	if _, present := request.Headers["X-API-Key"]; present {
		t.Error("X-API-Key header is present, want unauthenticated request")
	}
	if !strings.Contains(request.Headers["User-Agent"], "NavidromeBetterLyricsPlugin/0.1.0") {
		t.Errorf("User-Agent header = %q, want plugin name and version", request.Headers["User-Agent"])
	}
	if request.TimeoutMs != httpTimeoutMilliseconds {
		t.Errorf("timeout = %d, want %d", request.TimeoutMs, httpTimeoutMilliseconds)
	}
}

func TestBetterLyricsProvider_RetriesWithoutOptionalMetadata(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 401, Body: []byte(`{"error":"API key required"}`)},
		{StatusCode: 200, Body: []byte(`{"ttml":"<tt>cached lyrics</tt>"}`)},
	}
	var requests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, input)
		return responses[len(requests)-1], nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title:    "SHEESH",
		Artist:   "BABYMONSTER",
		Album:    "BABYMONS7ER - EP",
		Duration: 170.36,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || result.Lyrics[0].Text != "<tt>cached lyrics</tt>" {
		t.Fatalf("GetLyrics() lyrics = %#v, want cached TTML", result.Lyrics)
	}
	assertLyricsSource(t, result.Source, "ttml", "ttml")
	if len(requests) != 2 {
		t.Fatalf("GetLyrics() request count = %d, want 2", len(requests))
	}

	fullQuery, err := url.Parse(requests[0].URL)
	if err != nil {
		t.Fatalf("parse full request URL: %v", err)
	}
	if fullQuery.Query().Get("al") != "BABYMONS7ER - EP" || fullQuery.Query().Get("d") != "170" {
		t.Fatalf("full request query = %q, want album and duration", fullQuery.RawQuery)
	}

	minimalQuery, err := url.Parse(requests[1].URL)
	if err != nil {
		t.Fatalf("parse minimal request URL: %v", err)
	}
	if minimalQuery.Query().Get("s") != "SHEESH" || minimalQuery.Query().Get("a") != "BABYMONSTER" {
		t.Fatalf("minimal request query = %q, want title and artist", minimalQuery.RawQuery)
	}
	if minimalQuery.Query().Has("al") || minimalQuery.Query().Has("d") {
		t.Fatalf("minimal request query = %q, want no album or duration", minimalQuery.RawQuery)
	}
}

func TestBetterLyricsProvider_FallbackOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		unisonResponse   *host.HTTPResponse
		kugouResponse    *host.HTTPResponse
		wantText         string
		wantLanguage     string
		wantProvider     string
		wantFormat       string
		wantRequestCount int
	}{
		{
			name:             "returns Unison TTML before trying Kugou",
			unisonResponse:   &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"<tt>unison rich</tt>","format":"ttml","language":"ko","syncType":"richsync"}}`)},
			wantText:         "<tt>unison rich</tt>",
			wantLanguage:     "ko",
			wantProvider:     "unison",
			wantFormat:       "ttml",
			wantRequestCount: 3,
		},
		{
			name:             "returns Unison LRC before trying Kugou",
			unisonResponse:   &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]unison line","format":"lrc","language":"en","syncType":"linesync"}}`)},
			wantText:         "[00:01.00]unison line",
			wantLanguage:     "en",
			wantProvider:     "unison",
			wantFormat:       "lrc",
			wantRequestCount: 3,
		},
		{
			name:             "prefers synchronized Kugou over Unison plain text",
			unisonResponse:   &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"unison plain","format":"plain","language":"en","syncType":"plain"}}`)},
			kugouResponse:    &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"[00:01.00]kugou line","provider":"kugou"}`)},
			wantText:         "[00:01.00]kugou line",
			wantProvider:     "kugou",
			wantFormat:       "lrc",
			wantRequestCount: 4,
		},
		{
			name:             "keeps Unison plain text as the last resort",
			unisonResponse:   &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"unison plain","format":"plain","language":"en","syncType":"plain"}}`)},
			kugouResponse:    &host.HTTPResponse{StatusCode: 404},
			wantText:         "unison plain",
			wantLanguage:     "en",
			wantProvider:     "unison",
			wantFormat:       "plain",
			wantRequestCount: 4,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			responses := []*host.HTTPResponse{
				{StatusCode: 401, Body: []byte(`{"error":"API key required"}`)},
				{StatusCode: 404, Body: []byte(`{"error":"not found"}`)},
				test.unisonResponse,
			}
			if test.kugouResponse != nil {
				responses = append(responses, test.kugouResponse)
			}

			var requests []host.HTTPRequest
			provider := newBetterLyricsProvider(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
				requests = append(requests, input)
				if len(requests) > len(responses) {
					t.Fatalf("unexpected request %d: %s", len(requests), input.URL)
				}
				return responses[len(requests)-1], nil
			})

			result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Title:    "SHEESH",
				Artist:   "BABYMONSTER",
				Album:    "BABYMONS7ER - EP",
				Duration: 170.36,
			}})
			if err != nil {
				t.Fatalf("GetLyrics() error = %v, want nil", err)
			}
			if len(result.Lyrics) != 1 || result.Lyrics[0].Text != test.wantText || result.Lyrics[0].Lang != test.wantLanguage {
				t.Fatalf("GetLyrics() lyrics = %#v, want text %q and language %q", result.Lyrics, test.wantText, test.wantLanguage)
			}
			assertLyricsSource(t, result.Source, test.wantProvider, test.wantFormat)
			if len(requests) != test.wantRequestCount {
				t.Fatalf("GetLyrics() request count = %d, want %d", len(requests), test.wantRequestCount)
			}

			assertRequestEndpoint(t, requests[0], "https://lyrics-api.boidu.dev/getLyrics")
			assertRequestEndpoint(t, requests[1], "https://lyrics-api.boidu.dev/getLyrics")
			assertRequestEndpoint(t, requests[2], "https://unison.boidu.dev/lyrics")
			unisonQuery, err := url.Parse(requests[2].URL)
			if err != nil {
				t.Fatalf("parse Unison request URL: %v", err)
			}
			if unisonQuery.Query().Get("song") != "SHEESH" || unisonQuery.Query().Get("artist") != "BABYMONSTER" || unisonQuery.Query().Get("duration") != "170" {
				t.Fatalf("Unison request query = %q, want song, artist, and duration", unisonQuery.RawQuery)
			}
			if unisonQuery.Query().Has("album") {
				t.Fatalf("Unison request query = %q, want album omitted for broader matching", unisonQuery.RawQuery)
			}
			if len(requests) == 4 {
				assertRequestEndpoint(t, requests[3], "https://lyrics-api.boidu.dev/kugou/getLyrics")
				kugouQuery, err := url.Parse(requests[3].URL)
				if err != nil {
					t.Fatalf("parse Kugou request URL: %v", err)
				}
				if kugouQuery.Query().Has("al") || kugouQuery.Query().Has("d") {
					t.Fatalf("Kugou request query = %q, want title and artist only", kugouQuery.RawQuery)
				}
			}
		})
	}
}

func TestBetterLyricsProvider_RejectsUnknownUnisonFormatAfterFallbacks(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 404},
		{StatusCode: 404},
		{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"subtitle","format":"srt"}}`)},
		{StatusCode: 404},
	}
	requests := 0
	provider := newBetterLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		response := responses[requests]
		requests++
		return response, nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 123,
	}})
	if err == nil || !strings.Contains(err.Error(), `unsupported Unison lyrics format "srt"`) {
		t.Fatalf("GetLyrics() error = %v, want unsupported Unison format", err)
	}
	if len(result.Lyrics) != 0 {
		t.Fatalf("GetLyrics() lyrics = %#v, want empty", result.Lyrics)
	}
	if requests != 4 {
		t.Fatalf("GetLyrics() request count = %d, want 4", requests)
	}
}

func TestBetterLyricsProvider_UsesUnisonWhenBetterLyricsFails(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 503, Body: []byte(`{"error":"provider unavailable"}`)},
		{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]community line","format":"lrc","language":"en"}}`)},
	}
	var requests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, input)
		return responses[len(requests)-1], nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 123,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || result.Lyrics[0].Text != "[00:01.00]community line" {
		t.Fatalf("GetLyrics() lyrics = %#v, want Unison LRC", result.Lyrics)
	}
	assertLyricsSource(t, result.Source, "unison", "lrc")
	if len(requests) != 2 {
		t.Fatalf("GetLyrics() request count = %d, want 2", len(requests))
	}
	assertRequestEndpoint(t, requests[0], "https://lyrics-api.boidu.dev/getLyrics")
	assertRequestEndpoint(t, requests[1], "https://unison.boidu.dev/lyrics")
}

func TestBetterLyricsProvider_UsesKugouAfterBetterLyricsFailureAndUnisonPlainText(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 503, Body: []byte(`{"error":"provider unavailable"}`)},
		{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"community plain","format":"plain","language":"en"}}`)},
		{StatusCode: 200, Body: []byte(`{"lyrics":"[00:01.00]kugou line","provider":"kugou"}`)},
	}
	var requests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, input)
		return responses[len(requests)-1], nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 123,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || result.Lyrics[0].Text != "[00:01.00]kugou line" {
		t.Fatalf("GetLyrics() lyrics = %#v, want Kugou LRC", result.Lyrics)
	}
	assertLyricsSource(t, result.Source, "kugou", "lrc")
	if len(requests) != 3 {
		t.Fatalf("GetLyrics() request count = %d, want 3", len(requests))
	}
	assertRequestEndpoint(t, requests[0], "https://lyrics-api.boidu.dev/getLyrics")
	assertRequestEndpoint(t, requests[1], "https://unison.boidu.dev/lyrics")
	assertRequestEndpoint(t, requests[2], "https://lyrics-api.boidu.dev/kugou/getLyrics")
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

func TestLyricsRequestURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		track        lyrics.TrackInfo
		wantOK       bool
		wantTitle    string
		wantArtist   string
		wantAlbum    string
		wantDuration string
	}{
		{
			name:         "uses formatted artist and optional metadata",
			track:        lyrics.TrackInfo{Title: " Song ", Artist: " Main Artist ", Album: " Album ", Duration: 89.5},
			wantOK:       true,
			wantTitle:    "Song",
			wantArtist:   "Main Artist",
			wantAlbum:    "Album",
			wantDuration: "90",
		},
		{
			name: "falls back to structured artists",
			track: lyrics.TrackInfo{
				Title: "Song",
				Artists: []types.ArtistRef{
					{Name: "First"},
					{Name: " "},
					{Name: "Second"},
				},
			},
			wantOK:     true,
			wantTitle:  "Song",
			wantArtist: "First, Second",
		},
		{
			name:   "rejects missing title",
			track:  lyrics.TrackInfo{Artist: "Artist"},
			wantOK: false,
		},
		{
			name:   "rejects missing artist",
			track:  lyrics.TrackInfo{Title: "Song"},
			wantOK: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, ok := lyricsRequestURL(test.track)
			if ok != test.wantOK {
				t.Fatalf("lyricsRequestURL() ok = %v, want %v", ok, test.wantOK)
			}
			if !ok {
				return
			}

			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("parse request URL: %v", err)
			}
			query := parsed.Query()
			if query.Get("s") != test.wantTitle {
				t.Errorf("title = %q, want %q", query.Get("s"), test.wantTitle)
			}
			if query.Get("a") != test.wantArtist {
				t.Errorf("artist = %q, want %q", query.Get("a"), test.wantArtist)
			}
			if query.Get("al") != test.wantAlbum {
				t.Errorf("album = %q, want %q", query.Get("al"), test.wantAlbum)
			}
			if query.Get("d") != test.wantDuration {
				t.Errorf("duration = %q, want %q", query.Get("d"), test.wantDuration)
			}
		})
	}
}

func TestAPIResponseErrorTruncatesDetail(t *testing.T) {
	t.Parallel()

	detail := strings.Repeat("x", maximumAPIErrorRunes+20)
	err := apiResponseError(&host.HTTPResponse{
		StatusCode: 500,
		Body:       []byte(`{"error":"` + detail + `"}`),
	})
	if !strings.HasSuffix(err.Error(), "…") {
		t.Fatalf("apiResponseError() = %q, want truncated detail", err)
	}
}

func TestPluginVersion(t *testing.T) {
	t.Parallel()

	if got := pluginVersion(); got != "0.1.0" {
		t.Fatalf("pluginVersion() = %q, want 0.1.0", got)
	}
}

func TestManifestAllowsOnlyProviderHosts(t *testing.T) {
	t.Parallel()

	var manifest struct {
		Permissions struct {
			HTTP struct {
				RequiredHosts []string `json:"requiredHosts"`
			} `json:"http"`
			KVStore struct {
				MaxSize string `json:"maxSize"`
			} `json:"kvstore"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	got := strings.Join(manifest.Permissions.HTTP.RequiredHosts, ",")
	want := "lyrics-api.boidu.dev,unison.boidu.dev"
	if got != want {
		t.Fatalf("manifest HTTP hosts = %q, want %q", got, want)
	}
	if got := manifest.Permissions.KVStore.MaxSize; got != lyricsCacheMaxSize {
		t.Fatalf("manifest KV store maxSize = %q, want %q", got, lyricsCacheMaxSize)
	}
}

type fakeLyricsCache struct {
	mu       sync.Mutex
	values   map[string][]byte
	ttls     []int64
	setError error
}

func newFakeLyricsCache() *fakeLyricsCache {
	return &fakeLyricsCache{values: make(map[string][]byte)}
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
