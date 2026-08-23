package main

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
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
			response:     &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"  <tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\" xml:lang=\"en\">timed text</tt>\n"}`)},
			wantText:     "  <tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\" xml:lang=\"en\">timed text</tt>\n",
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
			Body:       []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">cached lyrics</tt>"}`),
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
