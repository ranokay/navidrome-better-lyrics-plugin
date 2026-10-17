package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLyricsPlusCandidates_WordTimingFromLiveFixture(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/lyricsplus/get.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "v2/lyrics/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: fixture}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	candidates, health, err := fetchLyricsPlusLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 234}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricsPlusLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one enhanced candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "LyricsPlus", "elrc")
	if candidates[0].quality != timingWord {
		t.Fatalf("quality = %v, want the apple word timing from the syllabus", candidates[0].quality)
	}

	// The payload's provenance must read from the probe sources.
	missProvider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 404}, nil
	})
	missCandidates, missHealth, missErr := fetchLyricsPlusLyricsCandidates(missProvider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if missErr != nil || len(missCandidates) != 0 || missHealth != lookupHealthy {
		t.Fatalf("miss = %#v/%v/%v, want a clean miss on 404", missCandidates, missHealth, missErr)
	}
}

func TestLyricsPlusDurationRounds(t *testing.T) {
	t.Parallel()

	// Fractional seconds must not round up into the next whole second.
	// 234.2 belongs to 234.
	var captured string
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		captured = request.URL
		return &host.HTTPResponse{StatusCode: 404}, nil
	})
	fetchLyricsPlusLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 234.2}, time.Now().Add(stageOneLookupBudget))
	if !strings.Contains(captured, "duration=234") || strings.Contains(captured, "duration=235") {
		t.Fatalf("URL = %q, want duration=234", captured)
	}
}

func TestLyricsPlusEmptyPrimaryIsCleanMiss(t *testing.T) {
	t.Parallel()

	// A decoded empty 200 payload is the mirror's clean miss; a mirror failure
	// must not turn it into a degraded lookup.
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, lyricsPlusEndpoint) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"type":"Line","lyrics":[]}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 500, Body: []byte(`kaput`)}, nil
	})

	candidates, health, err := fetchLyricsPlusLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v, want a clean miss without mirror rotation", candidates, health, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the primary mirror", requests)
	}
}

func TestLyricsPlusRotatesPastAThrottledMirror(t *testing.T) {
	t.Parallel()

	// The mirrors are independent deployments: a 429 on the primary must not
	// discard a healthy secondary.
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, lyricsPlusEndpoint) {
			return &host.HTTPResponse{StatusCode: 429, Headers: map[string]string{"Retry-After": "30"}}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"type":"Line","lyrics":[{"time":1000,"duration":500,"text":"mirror line"}]}`)}, nil
	})

	candidates, health, err := fetchLyricsPlusLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || health != lookupHealthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v/%v, want the secondary mirror to serve", candidates, health, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want the throttled primary then the secondary", requests)
	}
}

func TestLyricsPlusHostCooldownsStayMirrorScoped(t *testing.T) {
	t.Parallel()

	source, ok := catalogSource("lyricsplus")
	if !ok {
		t.Fatal("lyricsplus not in the catalog")
	}
	cache := newFakeLyricsCache()
	provider := newLyricsProviderWithDeps(nil, cache, time.Now, nil, nil)
	// Cool the primary mirror's host down.
	provider.rememberCooldown(sourceGlobalCooldownKey("lyricsplus.binimum.org"), 5*time.Minute)
	if provider.sourceInCooldown(source) {
		t.Fatal("a single mirror cooldown must not suppress the whole source pre-fetch")
	}

	// The fetch still serves from the healthy sibling.
	requests := 0
	provider = newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"type":"Line","lyrics":[{"time":1000,"duration":500,"text":"line"}]}`)}, nil
	}, cache, time.Now, nil, nil)
	provider.rememberCooldown(sourceGlobalCooldownKey("lyricsplus.binimum.org"), 5*time.Minute)
	candidates, health, err := fetchLyricsPlusLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || health != lookupHealthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v/%v, want the healthy sibling to serve", candidates, health, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the non-cooled mirror queried", requests)
	}
}
