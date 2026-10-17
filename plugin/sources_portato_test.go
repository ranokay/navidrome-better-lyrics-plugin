package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
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
	if candidate.Quality != lyric.TimingWord {
		t.Fatalf("quality = %v, want word", candidate.Quality)
	}
	assertLyricsSource(t, candidate.Response.Source, "Better Lyrics Portato", "elrc")
	if !strings.Contains(candidate.Response.Lyrics[0].Text, "[03:48.36]") || !strings.Contains(candidate.Response.Lyrics[0].Text, "<00:00.00>Shape") {
		t.Fatalf("converted text = %q, want enhanced LRC stamps", candidate.Response.Lyrics[0].Text)
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

func TestPortatoQRCEnvelopeExtraction(t *testing.T) {
	t.Parallel()

	// The QQ response wraps QRC inside an XML attribute: literal newlines let
	// the bracket parser see the rows, but XML entities stay escaped and
	// entity-encoded line breaks break parsing entirely.
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "qq") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<QrcInfos>\n<LyricInfo LyricCount=\"1\">\n<Lyric_1 LyricType=\"1\" LyricContent=\"[400,400]Rock &amp; Roll(0,400)\u000a[900,300]Go(0,300)\n\"/>\n</LyricInfo>\n</QrcInfos>","provider":"qq"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	candidates, health, err := fetchPortatoLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchPortatoLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v, want one candidate from the XML envelope", candidates, health)
	}
	rendered := candidates[0].Response.Lyrics[0].Text
	if strings.Contains(rendered, "&amp;") {
		t.Fatalf("rendered = %q, want XML entities decoded", rendered)
	}
	if !strings.Contains(rendered, "Rock & Roll") || !strings.Contains(rendered, "Go") {
		t.Fatalf("rendered = %q, want the escaped lyric text unescaped and both lines parsed", rendered)
	}
}
