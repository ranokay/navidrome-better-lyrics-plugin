package main

import (
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestKugouCandidates_SendConfiguredAPIKey(t *testing.T) {
	t.Parallel()

	requests := []host.HTTPRequest{}
	provider := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"[00:01.00]line"}`)}, nil
	}, newFakeLyricsCache(), time.Now, func(key string) (string, bool) {
		if key == "betterLyricsApiKey" {
			return "secret-key", true
		}
		return "", false
	}, nil)

	candidates, _, err := fetchKugouLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchKugouLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	if len(requests) != 1 || requests[0].Headers["X-API-Key"] != "secret-key" {
		t.Fatalf("kugou request headers = %#v, want the configured X-API-Key", requests[0].Headers)
	}
}
