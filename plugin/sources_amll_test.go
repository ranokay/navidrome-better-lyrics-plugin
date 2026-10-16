package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

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
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		switch {
		case strings.Contains(request.URL, "/v1/lyrics/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: amllFixture(t, "search.json")}, nil
		case strings.Contains(request.URL, "/v1/lyrics/get"):
			return &host.HTTPResponse{StatusCode: 200, Body: amllFixture(t, "get.json")}, nil
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

	provider := newLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[]}}`)}, nil
	})

	candidates, health, err := fetchAMLLyricsCandidates(provider, lyrics.TrackInfo{Title: "Unknown", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss", candidates, health, err)
	}
}

func TestAMLLCandidates_GetFailureSurfaces(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/v1/lyrics/search") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[{"id":"1"}]}}`)}, nil
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

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/v1/lyrics/search") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[{"id":"1"}]}}`)}, nil
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
