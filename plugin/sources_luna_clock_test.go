package main

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLunaCandidates_RticketFollowsTheInjectedClock(t *testing.T) {
	t.Parallel()

	now := time.Unix(1791953700, 0)
	var searchRequest string
	searchBody := `{"result_groups":[{"data":[{"entity":{"track":{"id":7,"duration":280000}}}]}]}`
	provider := newLyricsProviderWithSourcesAt(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "api.qishui.com/luna/search/track") {
			searchRequest = request.URL
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	}, newFakeLyricsCache(), func() time.Time { return now }, []lyricsSource{
		{
			ID: "luna", Display: "luna", Hosts: []string{"api.qishui.com"}, DefaultEnabled: true,
			Fetch: fetchLunaLyricsCandidates,
		},
	}, func(lyrics.TrackInfo) error { return nil })

	if _, _, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 280}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("fetchLunaLyricsCandidates() error = %v, want nil", err)
	}
	if !strings.Contains(searchRequest, "_rticket=1791953700000") {
		t.Fatalf("request = %q, want _rticket built from the injected clock", searchRequest)
	}
}
