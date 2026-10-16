package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const portatoQrcSample = "[0,2380]Shape(0,476) (476,95)of(571,190)\n" +
	"[228364,2284]I'm (228364,144)in (228508,177)love (228685,394)with the shape of you\n"

func TestPortatoCandidates_WordSyncedElrc(t *testing.T) {
	t.Parallel()

	var requests []host.HTTPRequest
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		return &host.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"lyrics":` + strconv.Quote(portatoQrcSample) + `}`),
		}, nil
	})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 238}
	candidates, health, err := fetchPortatoLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchPortatoLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	candidate := candidates[0]
	if candidate.quality != timingWord {
		t.Fatalf("quality = %v, want word", candidate.quality)
	}
	assertLyricsSource(t, candidate.response.Source, "Better Lyrics Portato", "elrc")
	if !strings.Contains(candidate.response.Lyrics[0].Text, "[03:48.36]") || !strings.Contains(candidate.response.Lyrics[0].Text, "<00:00.00>Shape") {
		t.Fatalf("converted text = %q, want enhanced LRC stamps", candidate.response.Lyrics[0].Text)
	}
	full, parseErr := url.Parse(requests[0].URL)
	if parseErr != nil {
		t.Fatalf("parse request URL: %v", parseErr)
	}
	if full.Query().Get("al") != "Album" || full.Query().Get("d") != "238" {
		t.Fatalf("request query = %q, want album and duration", full.RawQuery)
	}
}

func TestPortatoCandidates_MissTiersThenUncertainHealth(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 401, Body: []byte(`{"error":"API key required"}`)}, nil
	})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	candidates, health, err := fetchPortatoLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchPortatoLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want empty", candidates)
	}
	if health != lookupUncertain {
		t.Fatalf("health = %v, want uncertain", health)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1 (identical broader tiers collapse)", requests)
	}
}

func TestPortatoCandidates_NotFoundIsClean(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	candidates, health, err := fetchPortatoLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("fetchPortatoLyricsCandidates() = %#v, %v, %v; want clean miss", candidates, health, err)
	}
}

func TestPortatoCandidates_MalformedQrcErrors(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"definitely not bracketed lyrics"}`)}, nil
	})

	candidates, health, err := fetchPortatoLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil {
		t.Fatal("fetchPortatoLyricsCandidates() error = nil, want malformed QRC error")
	}
	if len(candidates) != 0 || health == lookupHealthy {
		t.Fatalf("candidates/health = %#v/%v, want degraded with no candidates", candidates, health)
	}
}
