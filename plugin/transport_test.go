package main

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLyricsProvider_GetLyricsBuildsRequest(t *testing.T) {
	t.Parallel()

	const apiEndpoint = "https://lyrics-api.boidu.dev/getLyrics"
	var request host.HTTPRequest
	provider := providerForCatalog(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
		request = input
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">lyrics</tt>"}`)}, nil
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
	if !strings.Contains(request.Headers["User-Agent"], "NavidromeBetterLyricsPlugin/0.3.0") {
		t.Errorf("User-Agent header = %q, want plugin name and version", request.Headers["User-Agent"])
	}
	if request.TimeoutMs != httpTimeoutMilliseconds {
		t.Errorf("timeout = %d, want %d", request.TimeoutMs, httpTimeoutMilliseconds)
	}
}

func TestLyricsProvider_UsesRemainingLookupBudgetForRequestTimeout(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 23, 7, 0, 0, 0, time.UTC)
	current := now
	provider := newLyricsProviderWithDependencies(nil, newFakeLyricsCache(), func() time.Time { return current })
	deadline := now.Add(stageOneLookupBudget)

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
