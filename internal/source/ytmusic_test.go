package source

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestYTMusicChain_ResolvesLyricsTab(t *testing.T) {
	t.Parallel()

	// The innertube chain is 3 hops: search with duration gating, watch-next
	// playlist extract, then the description shelf (synthetic bodies mirror
	// the probes).
	requests := 0
	nextRequestBody := ""
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "v1/next") {
			nextRequestBody = string(request.Body)
		}
		switch {
		case strings.Contains(request.URL, "v1/search"):
			// Realistic renderer shape: identity columns and arrays sit
			// between the video id and the rendered length, and the first
			// row is an unrelated song with the requested runtime.
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"contents":{"sectionListRenderer":{"contents":[{"musicShelfRenderer":{"contents":[
				{"musicResponsiveListItemRenderer":{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Other Song"}]}}},{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Other Artist"}]}}}],"lengthText":{"runs":[{"text":"3:58"}]},"playlistItemData":{"videoId":"AAAAAAAAA01"}}},
				{"musicResponsiveListItemRenderer":{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Song"}]}}},{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Artist"}]}}}],"lengthText":{"runs":[{"text":"3:58"}]},"playlistItemData":{"videoId":"JGwWNGJdvx8"}}}
			]}}]}}}`)}, nil
		case strings.Contains(request.URL, "v1/next"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"queueContextParams":{},"contents":{"rows":[{"browseId":"MPLYt_SbJhx3gEYmp","title":{"runs":[]}}]}}`)}, nil
		case strings.Contains(request.URL, "v1/browse"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"contents":{"shelf":{"musicDescriptionShelfRenderer":{"description":{"runs":[
				{"text":"Lyrics"},
				{"text":"The club isn't the best place"},
				{"text":"So the bar is where I go"},
				{"text":"Source:\u00aaa"}
			]}}}}}`)}, nil
		default:
			t.Errorf("unexpected request %q", request.URL)
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
	})

	candidates, health, err := fetchYTMusic(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 238}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchYTMusic() error = %v, want nil", err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one plain candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "YouTube Music", "plain")
	rendered := candidates[0].Response.Lyrics[0].Text
	if !strings.Contains(rendered, "The club isn't the best place") || strings.Contains(rendered, "Source:") {
		t.Fatalf("rendered = %q, want the shelf body without attribution", rendered)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want search+next+browse", requests)
	}
	if !strings.Contains(nextRequestBody, `"videoId":"JGwWNGJdvx8"`) {
		t.Fatalf("next body = %q, want the identity-matched video id (the unrelated row must be rejected by the title/artist gate)", nextRequestBody)
	}
}

func TestYTMusicSearch404IsCleanMiss(t *testing.T) {
	t.Parallel()

	// A 404 body is empty/non-JSON: decoding it would degrade a clean miss.
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	candidates, health, err := fetchYTMusic(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 0 || health != Healthy {
		t.Fatalf("result = %#v/%v/%v; want a clean miss on 404", candidates, health, err)
	}
}

func TestYTMusicMalformedBrowseDegrades(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "v1/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"contents":{"musicShelfRenderer":{"contents":[{"musicResponsiveListItemRenderer":{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Song"}]}}},{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Artist"}]}}}],"lengthText":{"runs":[{"text":"3:58"}]},"playlistItemData":{"videoId":"JGwWNGJdvx8"}}}]}}}`)}, nil
		case strings.Contains(request.URL, "v1/next"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"contents":{"browseId":"MPLYt_SbJhx3gEYmp"}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"contents": {"musicDescriptionShel`)}, nil
		}
	})

	_, health, err := fetchYTMusic(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 238}, time.Now().Add(testLookupBudget))
	if err == nil || health != Degraded {
		t.Fatalf("result = (%v, %v); want the malformed browse body to degrade", err, health)
	}
}

func TestYTMusicMalformedWatchDegrades(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "v1/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"contents":{"musicShelfRenderer":{"contents":[{"musicResponsiveListItemRenderer":{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Song"}]}}},{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Artist"}]}}}],"lengthText":{"runs":[{"text":"3:58"}]},"playlistItemData":{"videoId":"JGwWNGJdvx8"}}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"queueContextParams": {"browseId": "MPL`)}, nil
		}
	})

	_, health, err := fetchYTMusic(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 238}, time.Now().Add(testLookupBudget))
	if err == nil || health != Degraded {
		t.Fatalf("result = (%v, %v); want the malformed watch body to degrade", err, health)
	}
}
