package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

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
		{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">cached lyrics</tt>"}`)},
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
	if len(result.Lyrics) != 1 || !strings.Contains(result.Lyrics[0].Text, "cached lyrics") {
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

	durationQuery, err := url.Parse(requests[1].URL)
	if err != nil {
		t.Fatalf("parse duration-preserving request URL: %v", err)
	}
	if durationQuery.Query().Get("s") != "SHEESH" || durationQuery.Query().Get("a") != "BABYMONSTER" {
		t.Fatalf("duration-preserving request query = %q, want title and artist", durationQuery.RawQuery)
	}
	if durationQuery.Query().Has("al") || durationQuery.Query().Get("d") != "170" {
		t.Fatalf("duration-preserving request query = %q, want duration without album", durationQuery.RawQuery)
	}
}
