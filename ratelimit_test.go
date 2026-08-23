package main

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
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
		{name: "zero", header: "0", want: defaultRateLimitCooldown},
		{name: "negative", header: "-5", want: defaultRateLimitCooldown},
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
