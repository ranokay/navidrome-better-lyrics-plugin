package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestBetterLyricsProvider_BroadensBetterLyricsWithoutDroppingDurationFirst(t *testing.T) {
	t.Parallel()

	var requests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host != "lyrics-api.boidu.dev" || parsed.Path != "/getLyrics" {
			t.Fatalf("unexpected fallback request: %s", request.URL)
		}
		if len(requests) < 3 {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">cached broad match</tt>"}`)}, nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 181,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || !strings.Contains(result.Lyrics[0].Text, "cached broad match") {
		t.Fatalf("GetLyrics() lyrics = %#v, want Better Lyrics TTML", result.Lyrics)
	}
	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3 Better Lyrics metadata tiers", len(requests))
	}

	full, _ := url.Parse(requests[0].URL)
	durationOnly, _ := url.Parse(requests[1].URL)
	minimal, _ := url.Parse(requests[2].URL)
	if full.Query().Get("al") != "Album" || full.Query().Get("d") != "181" {
		t.Fatalf("full query = %q, want album and duration", full.RawQuery)
	}
	if durationOnly.Query().Has("al") || durationOnly.Query().Get("d") != "181" {
		t.Fatalf("duration-only query = %q, want duration without album", durationOnly.RawQuery)
	}
	if minimal.Query().Has("al") || minimal.Query().Has("d") {
		t.Fatalf("minimal query = %q, want title and artist only", minimal.RawQuery)
	}
}

func TestBetterLyricsProvider_BroadensUnisonMetadataInOrder(t *testing.T) {
	t.Parallel()

	var unisonRequests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		switch parsed.Host {
		case "lyrics-api.boidu.dev":
			return &host.HTTPResponse{StatusCode: 503}, nil
		case "unison.boidu.dev":
			unisonRequests = append(unisonRequests, request)
			if len(unisonRequests) < 3 {
				return &host.HTTPResponse{StatusCode: 404}, nil
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]broad community match","format":"lrc","language":"en"}}`)}, nil
		default:
			t.Fatalf("unexpected request: %s", request.URL)
			return nil, nil
		}
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 181,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "unison", "lrc")
	if len(unisonRequests) != 3 {
		t.Fatalf("Unison request count = %d, want 3 metadata tiers", len(unisonRequests))
	}

	full, _ := url.Parse(unisonRequests[0].URL)
	durationOnly, _ := url.Parse(unisonRequests[1].URL)
	minimal, _ := url.Parse(unisonRequests[2].URL)
	if full.Query().Get("album") != "Album" || full.Query().Get("duration") != "181" {
		t.Fatalf("full Unison query = %q, want album and duration", full.RawQuery)
	}
	if durationOnly.Query().Has("album") || durationOnly.Query().Get("duration") != "181" {
		t.Fatalf("duration-only Unison query = %q, want duration without album", durationOnly.RawQuery)
	}
	if minimal.Query().Has("album") || minimal.Query().Has("duration") {
		t.Fatalf("minimal Unison query = %q, want title and artist only", minimal.RawQuery)
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

func TestBetterLyricsProvider_Unison429StartsIndependentCooldown(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 7, 0, 0, 0, time.UTC)
	cache := newFakeLyricsCache()
	firstUnisonRequests := 0
	firstKugouRequests := 0
	first := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		switch {
		case parsed.Host == "unison.boidu.dev":
			firstUnisonRequests++
			return &host.HTTPResponse{StatusCode: 429, Body: []byte(`{"success":false,"error":"Rate limited. Try again later."}`)}, nil
		case parsed.Path == "/kugou/getLyrics":
			firstKugouRequests++
			return &host.HTTPResponse{StatusCode: 404}, nil
		default:
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
	}, cache, func() time.Time { return now })

	_, err := first.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "First", Artist: "Artist"}})
	if err == nil || !strings.Contains(err.Error(), "Unison API returned HTTP 429") {
		t.Fatalf("first GetLyrics() error = %v, want Unison 429", err)
	}
	if firstUnisonRequests != 1 || firstKugouRequests != 1 {
		t.Fatalf("first requests: Unison=%d Kugou=%d, want 1 each", firstUnisonRequests, firstKugouRequests)
	}
	if ttl := cache.lastTTL(); ttl != int64(defaultUnisonRateLimitCooldown/time.Second) {
		t.Fatalf("Unison cooldown TTL = %d, want %d", ttl, int64(defaultUnisonRateLimitCooldown/time.Second))
	}

	secondUnisonRequests := 0
	secondKugouRequests := 0
	second := newBetterLyricsProviderWithDependencies(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host == "unison.boidu.dev" {
			secondUnisonRequests++
			t.Fatalf("request reached Unison during cooldown: %s", request.URL)
		}
		if parsed.Path == "/kugou/getLyrics" {
			secondKugouRequests++
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	}, cache, func() time.Time { return now })

	_, err = second.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Second", Artist: "Artist"}})
	if err == nil || !strings.Contains(err.Error(), "unison API cooldown active") {
		t.Fatalf("second GetLyrics() error = %v, want Unison cooldown", err)
	}
	if secondUnisonRequests != 0 || secondKugouRequests != 1 {
		t.Fatalf("second requests: Unison=%d Kugou=%d, want 0 and 1", secondUnisonRequests, secondKugouRequests)
	}
}

func TestBetterLyricsProvider_MalformedBetterLyricsTTMLFallsBack(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host == "lyrics-api.boidu.dev" {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\"><p>broken"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]valid fallback","format":"lrc","language":"en"}}`)}, nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil because a fallback succeeded", err)
	}
	assertLyricsSource(t, result.Source, "unison", "lrc")
	if requests != 2 {
		t.Fatalf("request count = %d, want malformed Better Lyrics then Unison", requests)
	}
}

func TestBetterLyricsProvider_UntimedLRCDoesNotBeatKugouLineSync(t *testing.T) {
	t.Parallel()

	provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		switch {
		case parsed.Host == "unison.boidu.dev":
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"plain text mislabeled as lrc","format":"lrc","language":"en"}}`)}, nil
		case parsed.Path == "/kugou/getLyrics":
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"[00:01.00]real line sync"}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "kugou", "lrc")
}

func TestRetryAfterDurationCapsExcessiveValues(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 7, 0, 0, 0, time.UTC)
	if got := retryAfterDuration(map[string]string{"Retry-After": "999999999999999999"}, now); got != maxRateLimitCooldown {
		t.Fatalf("numeric Retry-After = %s, want cap %s", got, maxRateLimitCooldown)
	}
	if got := retryAfterDuration(map[string]string{"Retry-After": now.Add(24 * time.Hour).Format(http.TimeFormat)}, now); got != maxRateLimitCooldown {
		t.Fatalf("date Retry-After = %s, want cap %s", got, maxRateLimitCooldown)
	}
}

func TestBetterLyricsProvider_UsesRemainingLookupBudgetForRequestTimeout(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 7, 0, 0, 0, time.UTC)
	current := now
	provider := newBetterLyricsProviderWithDependencies(nil, newFakeLyricsCache(), func() time.Time { return current })
	deadline := now.Add(lyricsLookupTimeout)

	if got, err := provider.requestTimeoutMilliseconds(deadline); err != nil || got != httpTimeoutMilliseconds {
		t.Fatalf("initial timeout = %d, %v; want %d, nil", got, err, httpTimeoutMilliseconds)
	}
	current = deadline.Add(-1500 * time.Millisecond)
	if got, err := provider.requestTimeoutMilliseconds(deadline); err != nil || got != 1500 {
		t.Fatalf("remaining timeout = %d, %v; want 1500, nil", got, err)
	}
	current = deadline
	if _, err := provider.requestTimeoutMilliseconds(deadline); err == nil {
		t.Fatal("timeout at deadline = nil error, want lookup budget exhaustion")
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
