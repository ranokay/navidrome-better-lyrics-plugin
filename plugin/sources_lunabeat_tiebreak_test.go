package main

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLunaBeatCandidates_AlbumTiebreakKeepsFirstMatch(t *testing.T) {
	t.Parallel()

	index := []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[` +
		`{"id":"1","title":"confidence","artists":["Gareth.T"],"album":"","path":"lyrics/nomatch.ttml"},` +
		`{"id":"2","title":"confidence","artists":["Gareth.T"],"album":"Pop","path":"lyrics/e/first-album.ttml"},` +
		`{"id":"3","title":"confidence","artists":["Gareth.T"],"album":"Pop","path":"lyrics/e/second-album.ttml"}]}`)
	var ttmlPath string
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: index}, nil
		default:
			ttmlPath = request.URL
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now, "luna-beat")

	if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "confidence", Artist: "Gareth.T", Album: "Pop"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("fetchLunaBeatLyricsCandidates() error = %v, want nil", err)
	}
	if !strings.Contains(ttmlPath, "first-album.ttml") {
		t.Fatalf("TTML fetch = %q, want the first album-matched index row (tiebreak keeps the earliest row)", ttmlPath)
	}
}
