package source

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

func TestLyricallyCandidates_ITunesSearchThenMirror(t *testing.T) {
	t.Parallel()

	itunesBody := `{"resultCount":2,"results":[{"trackId":1,"trackName":"Live Forever","artistName":"Oasis","trackTimeMillis":278000},{"trackId":2,"trackName":"Live Forever (Other)","artistName":"Artist","trackTimeMillis":999000}]}`
	mirrorBody := `{"type":"cache","lrc":"[00:01.00]plain","elrc":"[00:01.00]<00:01.10>maybe<00:01.90>line","elrcMultiPerson":"","ttmlContent":"<tt></tt>","plain":"maybe","source":"local_cache","cached_at":"2026-10-17T00:00:00Z"}`

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "itunes.apple.com/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(itunesBody)}, nil
		case strings.Contains(request.URL, "lyrics.paxsenix.org/apple-music/lyrics"):
			if !strings.Contains(request.URL, "id=1") {
				t.Fatalf("mirror request should carry the duration-matched iTunes id: %s", request.URL)
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(mirrorBody)}, nil
		default:
			t.Fatalf("unexpected request URL: %s", request.URL)
			return nil, nil
		}
	})

	candidates, health, err := fetchLyrically(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 278,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyrically() error = %v, want nil", err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingWord {
		t.Fatalf("candidates = %#v, want one word candidate from elrc", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "Apple Music (Lyrically)", "elrc")
}

func TestLyricallyCandidates_TTMLFallbackWhenNoELRC(t *testing.T) {
	t.Parallel()

	itunesBody := `{"resultCount":1,"results":[{"trackId":9,"trackName":"Song","artistName":"Artist","trackTimeMillis":200000}]}`
	mirrorBody := `{"ttmlContent":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Line\"><p begin=\"1\">line</p></tt>"}`

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "itunes.apple.com/search") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(itunesBody)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(mirrorBody)}, nil
	})

	candidates, _, err := fetchLyrically(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyrically() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingLine {
		t.Fatalf("candidates = %#v, want line candidate from TTML", candidates)
	}
}

func TestLyricallyCandidates_DurationMissIsClean(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"resultCount":1,"results":[{"trackId":3,"trackName":"Song","artistName":"Artist","trackTimeMillis":250000}]}`)}, nil
	})

	candidates, health, err := fetchLyrically(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 99}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 0 || health != Healthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss", candidates, health, err)
	}
}

func TestAppleSearch429RecordsCooldown(t *testing.T) {
	t.Parallel()

	cache := newFakeStore()
	rt := newFakeRuntimeWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 429, Headers: map[string]string{"Retry-After": "9"}}, nil
	}, cache, time.Now)

	_, _, err := fetchLyrically(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err == nil {
		t.Fatal("429 converted to a miss; want a degraded error")
	}
	if cooldown := rt.Cooldown(GlobalCooldownKey("apple-lyrically")); cooldown < 9*time.Second || cooldown > 10*time.Second {
		t.Fatalf("cooldown = %v, want the Retry-After value honored", cooldown)
	}
}
