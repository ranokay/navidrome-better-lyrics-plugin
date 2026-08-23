package main

import (
	"encoding/json"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
	"net/url"
	"strings"
	"sync"
	"testing"
)

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
