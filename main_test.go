package main

import (
	"errors"
	"net/url"
	"strings"
	"testing"

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
		wantError    string
		wantRequests int
	}{
		{
			name:         "returns TTML unchanged",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"  <tt xml:lang=\"en\">timed text</tt>\n"}`)},
			wantText:     "  <tt xml:lang=\"en\">timed text</tt>\n",
			wantRequests: 1,
		},
		{
			name:         "treats not found as no match",
			track:        lyrics.TrackInfo{Title: "Unknown", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 404, Body: []byte(`{"error":"Lyrics not available"}`)},
			wantRequests: 1,
		},
		{
			name:         "treats empty TTML as no match",
			track:        lyrics.TrackInfo{Title: "Instrumental", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"  "}`)},
			wantRequests: 1,
		},
		{
			name:         "rejects malformed success response",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":`)},
			wantError:    "decode Better Lyrics API response",
			wantRequests: 1,
		},
		{
			name:         "treats unauthenticated cache miss as no match",
			track:        lyrics.TrackInfo{Title: "Uncached", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 401, Body: []byte(`{"error":"API key required"}`)},
			wantRequests: 1,
		},
		{
			name:         "surfaces invalid request response",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 422, Body: []byte(`{"error":"Invalid track metadata"}`)},
			wantError:    "HTTP 422: Invalid track metadata",
			wantRequests: 1,
		},
		{
			name:         "surfaces rate limit failure",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 429, Body: []byte(`{"message":"Please try again later"}`)},
			wantError:    "HTTP 429: Please try again later",
			wantRequests: 1,
		},
		{
			name:         "surfaces server status without echoing invalid body",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			response:     &host.HTTPResponse{StatusCode: 503, Body: []byte(`not-json`)},
			wantError:    "better lyrics API returned HTTP 503",
			wantRequests: 1,
		},
		{
			name:         "surfaces transport failure",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			sendError:    errors.New("network unavailable"),
			wantError:    "request Better Lyrics API: network unavailable",
			wantRequests: 1,
		},
		{
			name:         "rejects nil transport response",
			track:        lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
			wantError:    "better lyrics API returned no response",
			wantRequests: 1,
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
			provider := newBetterLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
				requests++
				return test.response, test.sendError
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
		})
	}
}

func TestBetterLyricsProvider_GetLyricsBuildsRequest(t *testing.T) {
	t.Parallel()

	var request host.HTTPRequest
	provider := newBetterLyricsProvider(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
		request = input
		return &host.HTTPResponse{StatusCode: 404}, nil
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
