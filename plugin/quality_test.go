package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLyricsProvider_PrefersHigherTimingQualityAcrossProviders(t *testing.T) {
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
			wantProvider: "Unison",
			wantRequests: 2,
		},
		{
			name:         "keeps Better Lyrics when timing quality ties",
			betterTTML:   betterTie,
			unisonTTML:   unisonTie,
			wantTTML:     betterTie,
			wantProvider: "Better Lyrics",
			wantRequests: 3,
		},
		{
			name:         "returns Better Lyrics syllables without a lower-priority request",
			betterTTML:   betterSyllable,
			wantTTML:     betterSyllable,
			wantProvider: "Better Lyrics",
			wantRequests: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
				requests++
				parsed, err := url.Parse(request.URL)
				if err != nil {
					t.Fatalf("parse request URL: %v", err)
				}
				switch {
				case parsed.Path == "/kugou/getLyrics":
					return &host.HTTPResponse{StatusCode: 404}, nil
				case parsed.Host == "lyrics-api.boidu.dev":
					return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":` + strconv.Quote(test.betterTTML) + `}`)}, nil
				case parsed.Host == "unison.boidu.dev":
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

func TestLyricsProvider_MalformedBetterLyricsTTMLFallsBack(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host == "lyrics-api.boidu.dev" {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\"><p>broken"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]valid fallback","format":"lrc","language":"en"}}`)}, nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil because a fallback succeeded", err)
	}
	assertLyricsSource(t, result.Source, "Unison", "lrc")
	if requests != 3 {
		t.Fatalf("request count = %d, want malformed Better Lyrics, Unison and Kugou", requests)
	}
}

func TestLyricsProvider_UntimedLRCDoesNotBeatKugouLineSync(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		switch {
		case parsed.Host == "unison.boidu.dev":
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"plain text mislabeled as lrc","format":"lrc","language":"en"}}`)}, nil
		case parsed.Path == "/kugou/getLyrics":
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"[00:01.00]real line sync"}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "Better Lyrics Legato", "lrc")
}

func TestLyricsProvider_RejectsUnknownUnisonFormatAfterFallbacks(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 404},
		{StatusCode: 404},
		{StatusCode: 404},
		{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"subtitle","format":"srt"}}`)},
		{StatusCode: 404},
	}
	requests := 0
	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
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

func TestEmptyTTMLDocumentIsNotACandidate(t *testing.T) {
	t.Parallel()

	empty := newLyricsCandidate(lyrics.LyricsText{Text: "<tt></tt>"}, "Stub", "ttml")
	if empty.present() {
		t.Fatal("an empty TTML document must not become a candidate")
	}
	if !emptyLyricsDocument("<tt></tt>", "ttml") {
		t.Fatal("an empty TTML document is a well-formed empty payload")
	}
	metadataOnly := newLyricsCandidate(lyrics.LyricsText{Text: `<tt xmlns:itunes="urn:itunes"></tt>`}, "Stub", "ttml")
	if metadataOnly.present() {
		t.Fatal("a metadata-only TTML document must not become a candidate")
	}
	if emptyLyricsDocument("<tt><broken", "ttml") {
		t.Fatal("malformed XML is not a well-formed empty payload")
	}
}

func TestCDATALyricsCountAsContent(t *testing.T) {
	t.Parallel()

	// CDATA carries real lyric text: rejecting it would cache a miss for
	// valid documents.
	cdata := `<tt xmlns:itunes="urn:itunes"><body><div><p begin="1" end="2"><![CDATA[Rock & Roll]]></p></div></body></tt>`
	if !lyricsTextHasContent(cdata, "ttml") {
		t.Fatal("CDATA lyric text must count as servable content")
	}
	candidate := newLyricsCandidate(lyrics.LyricsText{Text: cdata}, "Stub", "ttml")
	if !candidate.present() {
		t.Fatal("a CDATA-bearing document must produce a candidate")
	}
	if emptyLyricsDocument(cdata, "ttml") {
		t.Fatal("a CDATA-bearing document is not an empty document")
	}
}

func TestTTMLContentIgnoresMetadataNodes(t *testing.T) {
	t.Parallel()

	metadataOnly := `<tt xmlns:itunes="urn:itunes"><head><metadata>Song Title Words Here</metadata></head><body></body></tt>`
	if lyricsTextHasContent(metadataOnly, "ttml") {
		t.Fatal("metadata-only text must not count as lyric content")
	}
	withLyrics := `<tt xmlns:itunes="urn:itunes"><head><metadata>Song Title</metadata></head><body><p begin="1">real line</p></body></tt>`
	if !lyricsTextHasContent(withLyrics, "ttml") {
		t.Fatal("body text must count as lyric content")
	}
	bodyless := `<tt>bare line</tt>`
	if !lyricsTextHasContent(bodyless, "ttml") {
		t.Fatal("documents without a body fall back to any character data")
	}
}
