package provider

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func TestLyricsProvider_CachedTierRateLimitOnlyCoolsDownTheFailedQuery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 6, 0, 0, 0, time.UTC)
	cache := newFakeLyricsCache()
	requests := make([]host.HTTPRequest, 0, 4)
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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

func TestLyricsProvider_RetriesAQueryRateLimitWithBroaderMetadata(t *testing.T) {
	t.Parallel()

	requests := make([]host.HTTPRequest, 0, 2)
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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

func TestLyricsProvider_SkipsKugouWhenCooldownPersistenceFails(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	cache.setError = errors.New("cache unavailable")
	requests := 0
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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

func TestLyricsProvider_CooldownPreservesFractionalDeadline(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 6, 0, 0, int(900*time.Millisecond), time.UTC)
	currentTime := now
	cache := newFakeLyricsCache()
	provider := newTestProviderWithDependencies(nil, cache, func() time.Time { return currentTime })
	key := source.BetterLyricsGlobalCooldownKey
	provider.RememberCooldown(key, time.Second)

	currentTime = now.Add(100 * time.Millisecond)
	if remaining := provider.Cooldown(key); remaining <= 0 {
		t.Fatal("cooldown after 100ms = 0, want the one-second Retry-After to remain active")
	}
	if ttl := cache.lastTTL(); ttl != 2 {
		t.Fatalf("fractional cooldown TTL = %d, want 2 seconds to cover the rounded deadline", ttl)
	}

	currentTime = now.Add(1100 * time.Millisecond)
	if remaining := provider.Cooldown(key); remaining != 0 {
		t.Fatalf("cooldown after rounded deadline = %s, want 0", remaining)
	}
}

func TestLyricsProvider_SharesRetryAfterCooldownAcrossInstances(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 6, 0, 0, 0, time.UTC)
	cache := newFakeLyricsCache()
	firstRequests := 0
	first := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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
	if ttl := cache.ttlsForKey[source.BetterLyricsGlobalCooldownKey]; ttl != 12 {
		t.Fatalf("cooldown TTL = %d, want Retry-After value 12", ttl)
	}

	secondRequests := 0
	second := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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
	// The walker now pre-gates cooled-down sources, so the shared cooldown shows
	// up as zero requests to the better-lyrics family instead of a cooldown error.
	if err != nil {
		t.Fatalf("second GetLyrics() error = %v, want clean miss", err)
	}
	if secondRequests != 1 {
		t.Fatalf("second request count = %d, want one Unison request", secondRequests)
	}
}

func TestLyricsProvider_Unison429StartsIndependentCooldown(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 7, 0, 0, 0, time.UTC)
	cache := newFakeLyricsCache()
	firstUnisonRequests := 0
	firstKugouRequests := 0
	first := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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
	// Unison's 429 without Retry-After records its own one-minute default —
	// independent of the Better Lyrics family's 30-second window.
	const wantUnisonCooldownTTL = int64(60)
	cooldownTTL := int64(0)
	for _, recorded := range cache.ttls {
		if recorded == wantUnisonCooldownTTL {
			cooldownTTL = recorded
		}
	}
	if cooldownTTL == 0 {
		t.Fatalf("Unison cooldown TTL taints = %v, want %d", cache.ttls, wantUnisonCooldownTTL)
	}

	secondUnisonRequests := 0
	secondKugouRequests := 0
	second := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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
	// Pre-gating turns "unison API cooldown active" into "no Unison request".
	if err != nil {
		t.Fatalf("second GetLyrics() error = %v, want clean miss", err)
	}
	if secondUnisonRequests != 0 || secondKugouRequests != 1 {
		t.Fatalf("second requests: Unison=%d Kugou=%d, want 0 and 1", secondUnisonRequests, secondKugouRequests)
	}
}
