package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestBetterLyricsProvider_PrefersHigherTimingQualityAcrossProviders(t *testing.T) {
	t.Parallel()

	const (
		betterWord     = `<tt xmlns:itunes="urn:itunes" itunes:timing="Word"><p begin="1" end="2"><span begin="1" end="2">words</span></p></tt>`
		unisonSyllable = `<tt xmlns:itunes="urn:itunes" itunes:timing="Syllable"><p begin="1" end="2"><span begin="1" end="1.5">sylla</span><span begin="1.5" end="2">bles</span></p></tt>`
		betterTie      = `<tt xmlns:itunes="urn:itunes" itunes:timing="Word"><p begin="1" end="2"><span begin="1" end="2">better</span></p></tt>`
		unisonTie      = `<tt xmlns:itunes="urn:itunes" itunes:timing="Word"><p begin="1" end="2"><span begin="1" end="2">unison</span></p></tt>`
		betterSyllable = `<tt xmlns:itunes="urn:itunes" itunes:timing="Syllable"><p begin="1" end="2"><span begin="1" end="2">best</span></p></tt>`
	)

	tests := []struct {
		name         string
		betterTTML   string
		unisonTTML   string
		wantTTML     string
		wantProvider string
		wantRequests int
	}{
		{
			name:         "prefers Unison syllables over Better Lyrics words",
			betterTTML:   betterWord,
			unisonTTML:   unisonSyllable,
			wantTTML:     unisonSyllable,
			wantProvider: "unison",
			wantRequests: 2,
		},
		{
			name:         "keeps Better Lyrics when timing quality ties",
			betterTTML:   betterTie,
			unisonTTML:   unisonTie,
			wantTTML:     betterTie,
			wantProvider: "ttml",
			wantRequests: 2,
		},
		{
			name:         "returns Better Lyrics syllables without a lower-priority request",
			betterTTML:   betterSyllable,
			wantTTML:     betterSyllable,
			wantProvider: "ttml",
			wantRequests: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
				requests++
				parsed, err := url.Parse(request.URL)
				if err != nil {
					t.Fatalf("parse request URL: %v", err)
				}
				switch parsed.Host {
				case "lyrics-api.boidu.dev":
					return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":` + strconv.Quote(test.betterTTML) + `}`)}, nil
				case "unison.boidu.dev":
					return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":` + strconv.Quote(test.unisonTTML) + `,"format":"ttml","language":"en"}}`)}, nil
				default:
					t.Fatalf("unexpected request URL: %s", request.URL)
					return nil, nil
				}
			})

			result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
			if err != nil {
				t.Fatalf("GetLyrics() error = %v, want nil", err)
			}
			if len(result.Lyrics) != 1 {
				t.Fatalf("GetLyrics() lyrics = %#v, want one result", result.Lyrics)
			}
			if result.Lyrics[0].Text != test.wantTTML {
				t.Fatalf("GetLyrics() text = %q, want %q", result.Lyrics[0].Text, test.wantTTML)
			}
			assertLyricsSource(t, result.Source, test.wantProvider, "ttml")
			if requests != test.wantRequests {
				t.Fatalf("GetLyrics() request count = %d, want %d", requests, test.wantRequests)
			}
		})
	}
}

func TestTTMLTimingQuality(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ttml string
		want lyricTimingQuality
	}{
		{name: "syllable metadata", ttml: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><p begin="1"/></tt>`, want: timingSyllable},
		{name: "word metadata", ttml: `<tt xmlns:i="urn:itunes" i:timing="Word"><p begin="1"/></tt>`, want: timingWord},
		{name: "line metadata", ttml: `<tt xmlns:i="urn:itunes" i:timing="Line"><p begin="1"/></tt>`, want: timingLine},
		{name: "timed spans without metadata", ttml: `<tt><p begin="1" end="2"><span begin="1" end="2">word</span></p></tt>`, want: timingWord},
		{name: "ignores timed metadata spans", ttml: `<tt><head><text><span begin="1">translation</span></text></head><body><p begin="1">line</p></body></tt>`, want: timingLine},
		{name: "timed lines only", ttml: `<tt><p begin="1" end="2">line</p></tt>`, want: timingLine},
		{name: "untimed text", ttml: `<tt><p>plain</p></tt>`, want: timingUnsynced},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ttmlTimingQuality(test.ttml); got != test.want {
				t.Fatalf("ttmlTimingQuality() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBetterLyricsProvider_RejectsUnknownUnisonFormatAfterFallbacks(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 404},
		{StatusCode: 404},
		{StatusCode: 404},
		{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"subtitle","format":"srt"}}`)},
		{StatusCode: 404},
	}
	requests := 0
	provider := newBetterLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		response := responses[requests]
		requests++
		return response, nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 123,
	}})
	if err == nil || !strings.Contains(err.Error(), `unsupported Unison lyrics format "srt"`) {
		t.Fatalf("GetLyrics() error = %v, want unsupported Unison format", err)
	}
	if len(result.Lyrics) != 0 {
		t.Fatalf("GetLyrics() lyrics = %#v, want empty", result.Lyrics)
	}
	if requests != 5 {
		t.Fatalf("GetLyrics() request count = %d, want 5", requests)
	}
}

func TestBetterLyricsProvider_UsesUnisonWhenBetterLyricsFails(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 503, Body: []byte(`{"error":"provider unavailable"}`)},
		{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]community line","format":"lrc","language":"en"}}`)},
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
	if len(result.Lyrics) != 1 || result.Lyrics[0].Text != "[00:01.00]community line" {
		t.Fatalf("GetLyrics() lyrics = %#v, want Unison LRC", result.Lyrics)
	}
	assertLyricsSource(t, result.Source, "unison", "lrc")
	if len(requests) != 2 {
		t.Fatalf("GetLyrics() request count = %d, want 2", len(requests))
	}
	assertRequestEndpoint(t, requests[0], "https://lyrics-api.boidu.dev/getLyrics")
	assertRequestEndpoint(t, requests[1], "https://unison.boidu.dev/lyrics")
}
