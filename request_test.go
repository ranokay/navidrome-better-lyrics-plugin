package main

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
)

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
