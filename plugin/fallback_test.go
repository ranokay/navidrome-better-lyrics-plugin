package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestBetterLyricsProvider_FallbackOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		unisonResponse   *host.HTTPResponse
		kugouResponse    *host.HTTPResponse
		wantText         string
		wantLanguage     string
		wantProvider     string
		wantFormat       string
		wantRequestCount int
	}{
		{
			name:             "returns Unison TTML before trying Kugou",
			unisonResponse:   &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">unison rich</tt>","format":"ttml","language":"ko","syncType":"richsync"}}`)},
			wantText:         `<tt xmlns:itunes="urn:itunes" itunes:timing="Syllable">unison rich</tt>`,
			wantLanguage:     "ko",
			wantProvider:     "unison",
			wantFormat:       "ttml",
			wantRequestCount: 4,
		},
		{
			name:             "returns Unison LRC before trying Kugou",
			unisonResponse:   &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]unison line","format":"lrc","language":"en","syncType":"linesync"}}`)},
			wantText:         "[00:01.00]unison line",
			wantLanguage:     "en",
			wantProvider:     "unison",
			wantFormat:       "lrc",
			wantRequestCount: 4,
		},
		{
			name:             "prefers synchronized Kugou over Unison plain text",
			unisonResponse:   &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"unison plain","format":"plain","language":"en","syncType":"plain"}}`)},
			kugouResponse:    &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"[00:01.00]kugou line","provider":"kugou"}`)},
			wantText:         "[00:01.00]kugou line",
			wantProvider:     "kugou",
			wantFormat:       "lrc",
			wantRequestCount: 5,
		},
		{
			name:             "keeps Unison plain text as the last resort",
			unisonResponse:   &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"unison plain","format":"plain","language":"en","syncType":"plain"}}`)},
			kugouResponse:    &host.HTTPResponse{StatusCode: 404},
			wantText:         "unison plain",
			wantLanguage:     "en",
			wantProvider:     "unison",
			wantFormat:       "plain",
			wantRequestCount: 5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			responses := []*host.HTTPResponse{
				{StatusCode: 401, Body: []byte(`{"error":"API key required"}`)},
				{StatusCode: 404, Body: []byte(`{"error":"not found"}`)},
				{StatusCode: 404, Body: []byte(`{"error":"not found"}`)},
				test.unisonResponse,
			}
			if test.kugouResponse != nil {
				responses = append(responses, test.kugouResponse)
			}

			var requests []host.HTTPRequest
			provider := newBetterLyricsProvider(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
				requests = append(requests, input)
				if len(requests) > len(responses) {
					t.Fatalf("unexpected request %d: %s", len(requests), input.URL)
				}
				return responses[len(requests)-1], nil
			})

			result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Title:    "SHEESH",
				Artist:   "BABYMONSTER",
				Album:    "BABYMONS7ER - EP",
				Duration: 170.36,
			}})
			if err != nil {
				t.Fatalf("GetLyrics() error = %v, want nil", err)
			}
			if len(result.Lyrics) != 1 || result.Lyrics[0].Text != test.wantText || result.Lyrics[0].Lang != test.wantLanguage {
				t.Fatalf("GetLyrics() lyrics = %#v, want text %q and language %q", result.Lyrics, test.wantText, test.wantLanguage)
			}
			assertLyricsSource(t, result.Source, test.wantProvider, test.wantFormat)
			if len(requests) != test.wantRequestCount {
				t.Fatalf("GetLyrics() request count = %d, want %d", len(requests), test.wantRequestCount)
			}

			assertRequestEndpoint(t, requests[0], "https://lyrics-api.boidu.dev/getLyrics")
			assertRequestEndpoint(t, requests[1], "https://lyrics-api.boidu.dev/getLyrics")
			assertRequestEndpoint(t, requests[2], "https://lyrics-api.boidu.dev/getLyrics")
			assertRequestEndpoint(t, requests[3], "https://unison.boidu.dev/lyrics")
			unisonQuery, err := url.Parse(requests[3].URL)
			if err != nil {
				t.Fatalf("parse Unison request URL: %v", err)
			}
			if unisonQuery.Query().Get("song") != "SHEESH" || unisonQuery.Query().Get("artist") != "BABYMONSTER" || unisonQuery.Query().Get("duration") != "170" || unisonQuery.Query().Get("album") != "BABYMONS7ER - EP" {
				t.Fatalf("Unison request query = %q, want song, artist, album, and duration", unisonQuery.RawQuery)
			}
			if len(requests) == 5 {
				assertRequestEndpoint(t, requests[4], "https://lyrics-api.boidu.dev/kugou/getLyrics")
				kugouQuery, err := url.Parse(requests[4].URL)
				if err != nil {
					t.Fatalf("parse Kugou request URL: %v", err)
				}
				if kugouQuery.Query().Has("al") || kugouQuery.Query().Has("d") {
					t.Fatalf("Kugou request query = %q, want title and artist only", kugouQuery.RawQuery)
				}
			}
		})
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

func TestBetterLyricsProvider_UsesKugouAfterBetterLyricsFailureAndUnisonPlainText(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 503, Body: []byte(`{"error":"provider unavailable"}`)},
		{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"community plain","format":"plain","language":"en"}}`)},
		{StatusCode: 200, Body: []byte(`{"lyrics":"[00:01.00]kugou line","provider":"kugou"}`)},
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
	if len(result.Lyrics) != 1 || result.Lyrics[0].Text != "[00:01.00]kugou line" {
		t.Fatalf("GetLyrics() lyrics = %#v, want Kugou LRC", result.Lyrics)
	}
	assertLyricsSource(t, result.Source, "kugou", "lrc")
	if len(requests) != 3 {
		t.Fatalf("GetLyrics() request count = %d, want 3", len(requests))
	}
	assertRequestEndpoint(t, requests[0], "https://lyrics-api.boidu.dev/getLyrics")
	assertRequestEndpoint(t, requests[1], "https://unison.boidu.dev/lyrics")
	assertRequestEndpoint(t, requests[2], "https://lyrics-api.boidu.dev/kugou/getLyrics")
}

func TestBetterLyricsProvider_RetriesWithoutOptionalMetadata(t *testing.T) {
	t.Parallel()

	responses := []*host.HTTPResponse{
		{StatusCode: 401, Body: []byte(`{"error":"API key required"}`)},
		{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">cached lyrics</tt>"}`)},
	}
	var requests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(input host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, input)
		return responses[len(requests)-1], nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title:    "SHEESH",
		Artist:   "BABYMONSTER",
		Album:    "BABYMONS7ER - EP",
		Duration: 170.36,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || !strings.Contains(result.Lyrics[0].Text, "cached lyrics") {
		t.Fatalf("GetLyrics() lyrics = %#v, want cached TTML", result.Lyrics)
	}
	assertLyricsSource(t, result.Source, "ttml", "ttml")
	if len(requests) != 2 {
		t.Fatalf("GetLyrics() request count = %d, want 2", len(requests))
	}

	fullQuery, err := url.Parse(requests[0].URL)
	if err != nil {
		t.Fatalf("parse full request URL: %v", err)
	}
	if fullQuery.Query().Get("al") != "BABYMONS7ER - EP" || fullQuery.Query().Get("d") != "170" {
		t.Fatalf("full request query = %q, want album and duration", fullQuery.RawQuery)
	}

	durationQuery, err := url.Parse(requests[1].URL)
	if err != nil {
		t.Fatalf("parse duration-preserving request URL: %v", err)
	}
	if durationQuery.Query().Get("s") != "SHEESH" || durationQuery.Query().Get("a") != "BABYMONSTER" {
		t.Fatalf("duration-preserving request query = %q, want title and artist", durationQuery.RawQuery)
	}
	if durationQuery.Query().Has("al") || durationQuery.Query().Get("d") != "170" {
		t.Fatalf("duration-preserving request query = %q, want duration without album", durationQuery.RawQuery)
	}
}

func TestBetterLyricsProvider_BroadensBetterLyricsWithoutDroppingDurationFirst(t *testing.T) {
	t.Parallel()

	var requests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host != "lyrics-api.boidu.dev" || parsed.Path != "/getLyrics" {
			t.Fatalf("unexpected fallback request: %s", request.URL)
		}
		if len(requests) < 3 {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">cached broad match</tt>"}`)}, nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 181,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || !strings.Contains(result.Lyrics[0].Text, "cached broad match") {
		t.Fatalf("GetLyrics() lyrics = %#v, want Better Lyrics TTML", result.Lyrics)
	}
	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3 Better Lyrics metadata tiers", len(requests))
	}

	full, _ := url.Parse(requests[0].URL)
	durationOnly, _ := url.Parse(requests[1].URL)
	minimal, _ := url.Parse(requests[2].URL)
	if full.Query().Get("al") != "Album" || full.Query().Get("d") != "181" {
		t.Fatalf("full query = %q, want album and duration", full.RawQuery)
	}
	if durationOnly.Query().Has("al") || durationOnly.Query().Get("d") != "181" {
		t.Fatalf("duration-only query = %q, want duration without album", durationOnly.RawQuery)
	}
	if minimal.Query().Has("al") || minimal.Query().Has("d") {
		t.Fatalf("minimal query = %q, want title and artist only", minimal.RawQuery)
	}
}

func TestBetterLyricsProvider_BroadensUnisonMetadataInOrder(t *testing.T) {
	t.Parallel()

	var unisonRequests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		switch parsed.Host {
		case "lyrics-api.boidu.dev":
			return &host.HTTPResponse{StatusCode: 503}, nil
		case "unison.boidu.dev":
			unisonRequests = append(unisonRequests, request)
			if len(unisonRequests) < 3 {
				return &host.HTTPResponse{StatusCode: 404}, nil
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"success":true,"data":{"lyrics":"[00:01.00]broad community match","format":"lrc","language":"en"}}`)}, nil
		default:
			t.Fatalf("unexpected request: %s", request.URL)
			return nil, nil
		}
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 181,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "unison", "lrc")
	if len(unisonRequests) != 3 {
		t.Fatalf("Unison request count = %d, want 3 metadata tiers", len(unisonRequests))
	}

	full, _ := url.Parse(unisonRequests[0].URL)
	durationOnly, _ := url.Parse(unisonRequests[1].URL)
	minimal, _ := url.Parse(unisonRequests[2].URL)
	if full.Query().Get("album") != "Album" || full.Query().Get("duration") != "181" {
		t.Fatalf("full Unison query = %q, want album and duration", full.RawQuery)
	}
	if durationOnly.Query().Has("album") || durationOnly.Query().Get("duration") != "181" {
		t.Fatalf("duration-only Unison query = %q, want duration without album", durationOnly.RawQuery)
	}
	if minimal.Query().Has("album") || minimal.Query().Has("duration") {
		t.Fatalf("minimal Unison query = %q, want title and artist only", minimal.RawQuery)
	}
}

func TestBetterLyricsProvider_BroadensAfterAnEmptyTTMLHit(t *testing.T) {
	t.Parallel()

	var requests []host.HTTPRequest
	provider := newBetterLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse request URL: %v", err)
		}
		if parsed.Host != "lyrics-api.boidu.dev" || parsed.Path != "/getLyrics" {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		if parsed.Query().Has("al") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":""}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">broader cached match</tt>"}`)}, nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 181,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || !strings.Contains(result.Lyrics[0].Text, "broader cached match") {
		t.Fatalf("GetLyrics() lyrics = %#v, want broader cached TTML", result.Lyrics)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want an empty full-metadata hit followed by a duration-only retry", len(requests))
	}
	second, err := url.Parse(requests[1].URL)
	if err != nil {
		t.Fatalf("parse second request URL: %v", err)
	}
	if second.Query().Has("al") || second.Query().Get("d") != "181" {
		t.Fatalf("second request query = %q, want duration without album", second.RawQuery)
	}
}
