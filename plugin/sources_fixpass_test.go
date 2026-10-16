package main

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestMusixmatchCandidates_Search429RecordsCooldown(t *testing.T) {
	t.Parallel()

	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/ws/1.1/token.get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		}
		return &host.HTTPResponse{
			StatusCode: 429,
			Headers:    map[string]string{"Retry-After": "60"},
			Body:       []byte(`{"message":{"header":{"status_code":429}}}`),
		}, nil
	}, newFakeLyricsCache(), time.Now, "musixmatch")

	_, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(stageOneLookupBudget))
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("error = %v, want HTTP 429 (Review: a search-path 429 must surface and cool down)", err)
	}
	if remaining := provider.activeCooldown(sourceGlobalCooldownKey("musixmatch")); remaining <= 0 {
		t.Fatalf("musixmatch cooldown = %v, want active so the sweep skips hammering", remaining)
	}
}

func TestLyricifyCandidates_DoNotAskForGzip(t *testing.T) {
	t.Parallel()

	var lyricEncoding, lyricContentType string
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.credits.fm/v1/resolve/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBQCP1400011"}`)}, nil
		default:
			lyricEncoding = request.Headers["Accept-Encoding"]
			lyricContentType = request.Headers["Content-Type"]
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"text":"[type:LyricifyLines]\n[1000,500]plain"}`)}, nil
		}
	})

	if _, _, err := fetchLyricifyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("fetchLyricifyLyricsCandidates() error = %v, want nil", err)
	}
	if lyricEncoding == "gzip" {
		t.Fatalf("Accept-Encoding = %q, want identity (the host client does not auto-decompress, so a gzip body would fail every decode)", lyricEncoding)
	}
	if lyricContentType != "" {
		t.Fatalf("lyrics GET Content-Type = %q, want none for a GET", lyricContentType)
	}
}

func TestLyricifyCandidates_WordOutputLabelsAsElrc(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.credits.fm/v1/resolve/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBQCP1400011"}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"text":"[type:LyricifyLines]\n[from:AppleSyllable]\n[100,400]Hel(100,200) (200,100)lo(300,100)"}`)}, nil
		}
	})

	candidates, _, err := fetchLyricifyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricifyLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Lyricify", "elrc")
}
