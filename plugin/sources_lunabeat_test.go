package main

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLunaBeatCandidates_TTMLFromFile(t *testing.T) {
	t.Parallel()

	songs := []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"3c82991d4043191e","title":"confidence","artists":["Gareth.T"],"album":"Pop","path":"lyrics/3c/3c82991d4043191e.ttml"}]}`)
	fetched := 0
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			fetched++
			return &host.HTTPResponse{StatusCode: 200, Body: songs}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt xmlns:itunes="urn:itunes" itunes:timing="Word"><p begin="1"><span begin="1">word</span></p></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now)

	candidates, health, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "confidence", Artist: "Gareth.T"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLunaBeatLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].quality < timingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "LunaBeat", "ttml")
}

func TestLunaBeatCandidates_SecondLookupUsesCachedIndex(t *testing.T) {
	t.Parallel()

	songs := []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"e","title":"confidence","artists":["Gareth.T"],"path":"lyrics/e/e.ttml"}]}`)
	indexRequests := 0
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			indexRequests++
			return &host.HTTPResponse{StatusCode: 200, Body: songs}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt xmlns:itunes="urn:itunes"><p begin="1" end="2"><span begin="1" end="2">stub line</span></p></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now)

	if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "confidence", Artist: "Gareth.T"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("first fetch error = %v, want nil", err)
	}
	if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "confidence", Artist: "Gareth.T"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("second fetch error = %v, want nil", err)
	}
	if indexRequests != 1 {
		t.Fatalf("index requests = %d, want one cached across lookups", indexRequests)
	}
}

func TestLunaBeatCandidates_IndexFailureSkipsSource(t *testing.T) {
	t.Parallel()

	// Index acquisition is an operational failure, not a clean miss: sweeps
	// must not mark the source covered while the hub is down.
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "api/v1/manifest.json") || strings.Contains(request.URL, "api/v1/songs.json") {
			return &host.HTTPResponse{StatusCode: 503, Body: []byte(`unavailable`)}, nil
		}
		t.Fatal("a broken index must not trigger lyric fetches")
		return nil, nil
	}, newFakeLyricsCache(), time.Now)

	candidates, health, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "confidence", Artist: "Gareth.T"}, time.Now().Add(stageOneLookupBudget))
	if err == nil || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v; want a degraded operational failure on a broken index", candidates, err)
	}
	if health != lookupDegraded {
		t.Fatalf("health = %v, want degraded", health)
	}
}

func TestLunaBeatCandidates_ReorderedRevisionRefetchesIndex(t *testing.T) {
	t.Parallel()

	revision := "rev-1"
	musicIndex := []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"e","title":"confidence","artists":["Gareth.T"],"path":"lyrics/e/e.ttml"}]}`)
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"` + revision + `"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: musicIndex}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt xmlns:itunes="urn:itunes"><p begin="1" end="2"><span begin="1" end="2">stub line</span></p></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now)

	if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "confidence", Artist: "Gareth.T"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("first fetch error = %v, want nil", err)
	}
	revision = "rev-2"
	if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "confidence", Artist: "Gareth.T"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("revision-bumped fetch error = %v, want nil", err)
	}
}

func TestLunaBeatIndexFailuresStayDegraded(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}
	facade := func(manifestStatus, indexStatus int32) func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			switch {
			case strings.Contains(request.URL, "manifest.json"):
				if manifestStatus == 0 {
					return &host.HTTPResponse{StatusCode: 404}, nil
				}
				return &host.HTTPResponse{StatusCode: manifestStatus, Body: []byte(`{"revision":"r1"}`)}, nil
			case strings.Contains(request.URL, "songs.json"):
				return &host.HTTPResponse{StatusCode: indexStatus, Body: []byte(`{"schemaVersion":6,"revision":"r1","songs":[]}`)}, nil
			default:
				t.Errorf("unexpected request %q", request.URL)
				return &host.HTTPResponse{StatusCode: 404}, nil
			}
		}
	}

	t.Run("index http failure degrades", func(t *testing.T) {
		provider := newLyricsProviderWithDeps(facade(404, 503), newFakeLyricsCache(), time.Now, nil, nil)
		_, health, err := fetchLunaBeatLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want a degraded operational failure", err, health)
		}
	})

	t.Run("index 429 activates cooldown", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(facade(404, 429), cache, time.Now, nil, nil)
		_, _, err := fetchLunaBeatLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatal("index 429 converted to a miss; want a degraded error")
		}
		if provider.activeCooldown(sourceGlobalCooldownKey("luna-beat")) <= 0 {
			t.Fatal("index 429 activated no cooldown")
		}
	})

	t.Run("valid index without a match stays a healthy miss", func(t *testing.T) {
		provider := newLyricsProviderWithDeps(facade(200, 200), newFakeLyricsCache(), time.Now, nil, nil)
		_, health, err := fetchLunaBeatLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err != nil || health != lookupHealthy {
			t.Fatalf("result = (%v, %v); want a quiet miss with an empty valid index", err, health)
		}
	})
}
