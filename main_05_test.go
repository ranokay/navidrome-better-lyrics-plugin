package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"net/url"
	"testing"
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
