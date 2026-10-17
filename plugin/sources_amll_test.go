package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestAMLLPickFailsClosedWithoutTitleTokens(t *testing.T) {
	t.Parallel()

	// A request title that collapses to no alphanumerics (emoji-only, say)
	// cannot verify any row: accepting one serves and caches an unrelated
	// song.
	items := []amllItem{{MusicNames: []string{"Live Forever"}, ArtistNames: []string{"Oasis"}}}
	if _, found := pickAMLLItem(items, lyrics.TrackInfo{Title: "🎵", Artist: "Oasis"}); found {
		t.Fatal("pickAMLLItem accepted a row for a title with no verifiable tokens")
	}
	if amllNamesMatch([]string{"Live Forever"}, nil) {
		t.Fatal("amllNamesMatch accepted an empty wanted token list")
	}
}

func amllFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/amll/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func TestAMLLCandidates_TTMLFromSearchAndFetch(t *testing.T) {
	t.Parallel()

	var requests []host.HTTPRequest
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		switch {
		case strings.Contains(request.URL, "/v1/lyrics/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: amllFixture(t, "search.json")}, nil
		case strings.Contains(request.URL, "/v1/lyrics/get"):
			// The saved get.json fixture is truncated mid-element for size, so the
			// fetch step serves a small complete TTML document here instead.
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"lyrics":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Word\"><p begin=\"1\" end=\"2\"><span begin=\"1\" end=\"2\">word</span></p></tt>"}}`)}, nil
		default:
			t.Fatalf("unexpected request URL: %s", request.URL)
			return nil, nil
		}
	})

	candidates, health, err := fetchAMLLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Shape of You", Artist: "Ed Sheeran", Album: "Pop", Duration: 234,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchAMLLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	candidate := candidates[0]
	if candidate.quality == timingUnsynced {
		t.Fatalf("quality = %v, want TTML-graded timing (fixture is word-timed)", candidate.quality)
	}
	assertLyricsSource(t, candidate.response.Source, "AMLL", "ttml")
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want search then fetch", len(requests))
	}
}

func TestAMLLCandidates_SearchMissIsClean(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[]}}`)}, nil
	})

	candidates, health, err := fetchAMLLyricsCandidates(provider, lyrics.TrackInfo{Title: "Unknown", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss", candidates, health, err)
	}
}

func TestAMLLCandidates_GetFailureSurfaces(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/v1/lyrics/search") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[{"id":"1","musicNames":["Song"],"artistNames":["Artist"]}]}}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 500, Body: []byte(`{"error":"boom"}`)}, nil
	})

	candidates, health, err := fetchAMLLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("fetchAMLLyricsCandidates() error = %v, want HTTP 500", err)
	}
	if len(candidates) != 0 || health != lookupDegraded {
		t.Fatalf("candidates/health = %#v/%v, want degraded", candidates, health)
	}
}

func TestAMLLCandidates_UnexpectedGetPayloadErrors(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/v1/lyrics/search") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[{"id":"1","musicNames":["Song"],"artistNames":["Artist"]}]}}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"lyrics":""}}`)}, nil
	})

	candidates, health, err := fetchAMLLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("fetchAMLLyricsCandidates() = %#v, %v; want malformed TTML error", candidates, err)
	}
	if health != lookupDegraded {
		t.Fatalf("health = %v, want degraded", health)
	}
}

func TestAMLLyricsEnvelopeStatusesClassified(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	facade := func(searchExecutor func() string) func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			switch {
			case strings.Contains(request.URL, "amll"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchExecutor())}, nil
			default:
				return &host.HTTPResponse{StatusCode: 404}, nil
			}
		}
	}

	t.Run("item envelope server status degrades", func(t *testing.T) {
		provider := newLyricsProvider(facade(func() string { return `{"status":500}` }))
		_, health, err := fetchAMLLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want degraded on envelope 500", err, health)
		}
	})

	t.Run("item envelope 429 activates cooldown", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(facade(func() string { return `{"status":429}` }), cache, time.Now, nil, nil)
		_, _, err := fetchAMLLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatal("envelope 429 converted to a miss; want a degraded error")
		}
		if provider.activeCooldown(sourceGlobalCooldownKey("amll")) <= 0 {
			t.Fatal("envelope 429 activated no cooldown")
		}
	})

	t.Run("lyrics envelope server status degrades", func(t *testing.T) {
		requests := 0
		provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			requests++
			switch {
			case strings.Contains(request.URL, "v1/lyrics/search"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[{"id":"1","musicNames":["Song"],"artistNames":["Artist"]}]}}`)}, nil
			case strings.Contains(request.URL, "v1/lyrics/get"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":500}`)}, nil
			default:
				t.Errorf("unexpected request %q", request.URL)
				return &host.HTTPResponse{StatusCode: 404}, nil
			}
		})
		_, _, err := fetchAMLLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatalf("lookup succeeded; want a degraded error when the lyrics envelope reports 500 (requests=%d)", requests)
		}
	})
}
