package provider

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLyricsProvider_SweepUpgradesTheFirstSynchronizedResult(t *testing.T) {
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
			wantRequests: 3,
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
			wantRequests: 3,
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

			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			first, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
			if err != nil || requests != 1 {
				t.Fatalf("foreground lookup = %v, %d requests; want the first synchronized source immediately", err, requests)
			}
			assertLyricsSource(t, first.Source, "Better Lyrics", "ttml")
			provider.runSweep(track)
			result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
			if err != nil {
				t.Fatalf("GetLyrics() error = %v, want nil", err)
			}
			if text, _ := winnerText(t, result); text != test.wantTTML {
				t.Fatalf("GetLyrics() text = %q, want %q", result.Lyrics[0].Text, test.wantTTML)
			}
			assertLyricsSource(t, result.Source, test.wantProvider, "ttml")
			if requests != test.wantRequests {
				t.Fatalf("GetLyrics() request count = %d, want %d", requests, test.wantRequests)
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
	if requests != 2 {
		t.Fatalf("request count = %d, want malformed Better Lyrics then Unison", requests)
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
