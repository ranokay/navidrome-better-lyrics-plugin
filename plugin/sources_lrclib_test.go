package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLRCLibCandidates_PrefersWordSyncedLyricsFile(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/lrclib/get.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: fixture}, nil
		}
		t.Fatalf("unexpected request: %s", request.URL)
		return nil, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Shape of You", Artist: "Ed Sheeran", Duration: 234,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLRCLibLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	candidate := candidates[0]
	if candidate.quality != timingWord && candidate.quality != timingLine {
		t.Fatalf("quality = %v, want synchronized", candidate.quality)
	}
	assertLyricsSource(t, candidate.response.Source, "LRCLib", candidate.response.Source.Format)
}

func TestLRCLibCandidates_FallsBackToSearchOnExactMiss(t *testing.T) {
	t.Parallel()

	searchFixture, err := os.ReadFile("testdata/lrclib/search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	requests := 0
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: searchFixture}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 281,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLRCLibLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want get then search", requests)
	}
}

func TestLRCLibCandidates_SearchMissWhenNoNearDuration(t *testing.T) {
	t.Parallel()

	searchFixture, err := os.ReadFile("testdata/lrclib/search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: searchFixture}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 9999,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss", candidates, health, err)
	}
}

func TestLRCLibCandidates_ThrottleLeadsCooldown(t *testing.T) {
	t.Parallel()

	throttles := 0
	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		if throttles == 0 {
			throttles++
			return &host.HTTPResponse{
				StatusCode: 429,
				Headers:    map[string]string{"Retry-After": "30"},
				Body:       []byte(`{"name":"RateLimited"}`),
			}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"syncedLyrics":"[00:01.00]line","plainLyrics":"plain"}`)}, nil
	})

	_, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("error = %v, want HTTP 429", err)
	}
	if remaining := provider.activeCooldown(sourceGlobalCooldownKey("lrclib")); remaining <= 0 {
		t.Fatalf("lrclib cooldown = %v, want active", remaining)
	}
	if health != lookupDegraded {
		t.Fatalf("health = %v, want degraded", health)
	}
}

func TestLRCLibCandidates_PlainFallback(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"plainLyrics":"just plain lyrics","syncedLyrics":""}`)}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLRCLibLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingUnsynced {
		t.Fatalf("candidates = %#v, want one unsynced candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "LRCLib", "plain")
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
}
