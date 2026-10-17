package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

func binimumFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/binimum/search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func TestBinimumCandidates_PreservesTTML(t *testing.T) {
	t.Parallel()

	search := binimumFixture(t)
	ttml := []byte("<tt xmlns=\"http://www.w3.org/ns/ttml\" lrc:timing=\"Word\"><p begin=\"100\">word-timed row</p></tt>")
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "lrc.red/s/") {
			return &host.HTTPResponse{StatusCode: 200, Body: ttml}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: search}, nil
	})

	// The Glastonbury record in the fixture runs 281 s and carries ISRC GBBQY9502039.
	candidates, health, err := fetchBinimumLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 281,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchBinimumLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	if candidate := candidates[0]; candidate.Quality == lyric.TimingUnsynced {
		t.Fatalf("candidate quality = %v, want graded TTML timing", candidate.Quality)
	} else {
		assertLyricsSource(t, candidate.Response.Source, "BiniLyrics", "ttml")
	}
}

func TestBinimumCandidates_PrefersDurationMatch(t *testing.T) {
	t.Parallel()

	search := binimumFixture(t)
	fetches := 0
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		fetches++
		if strings.Contains(request.URL, "lrc.red/s/GBQCP1400011") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte("<tt xmlns:itunes=\"urn:itunes\"><p begin=\"1\" end=\"2\"><span begin=\"1\" end=\"2\">stub line</span></p></tt>")}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: search}, nil
	})

	candidates, health, err := fetchBinimumLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 279,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchBinimumLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v, want one candidate", candidates, health)
	}
	if fetches != 2 {
		t.Fatalf("request count = %d, want search plus lyrics fetch", fetches)
	}
}

func TestBinimumCandidates_MissesWithoutDurationMatch(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: binimumFixture(t)}, nil
	})

	candidates, health, err := fetchBinimumLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 9999,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss on unmatched duration", candidates, health, err)
	}
}

func TestBinimumCandidates_SearchFailures(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	candidates, health, err := fetchBinimumLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss on 404", candidates, health, err)
	}
}

func TestBinimumCandidates_LyricsFetchFailureReturnsError(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "lrc.red/s/") {
			return &host.HTTPResponse{StatusCode: 503, Body: []byte(`{"error":"unavailable"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"results":[{"duration":281,"artist_name":"Oasis","lyricsUrl":"https://lrc.red/s/GBBQY9502039.ttml","timing_type":"line","track_name":"Live"}]}`)}, nil
	})

	candidates, health, err := fetchBinimumLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live", Artist: "Oasis", Duration: 281}, time.Now().Add(stageOneLookupBudget))
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("fetchBinimumLyricsCandidates() error = %v, want HTTP 503", err)
	}
	if len(candidates) != 0 || health != lookupDegraded {
		t.Fatalf("candidates/health = %#v/%v, want no candidate and degraded health", candidates, health)
	}
}

func TestBininumRejectsHostileLyricsURLs(t *testing.T) {
	t.Parallel()

	// Provider-supplied URLs are untrusted: a hostile search row must never
	// be fetched through the server's HTTP client (SSRF).
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "evil.example") {
			t.Fatalf("hostile URL fetched: %s", request.URL)
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"results":[{"track_name":"Song","artist_name":"Artist","duration":200,"lyricsUrl":"https://evil.example/steal.ttml"}]}`)}, nil
	})

	candidates, health, err := fetchBinimumLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want a clean miss without fetching the hostile URL", candidates, health, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the search call", requests)
	}
}
