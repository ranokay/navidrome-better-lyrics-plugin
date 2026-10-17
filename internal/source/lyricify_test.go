package source

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

func TestLyricifyCandidates_ISRCResolveThenFetch(t *testing.T) {
	t.Parallel()

	resolveBody := `{"credits_id":"x","isrc":"GBQCP1400011","match_status":"matched"}`
	lyricsBody := `{"text":"[type:LyricifyLines]\n[9731,14858]The club isn't the best place\n","offset":0,"isInstrumental":false}`
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.credits.fm/v1/resolve/track"):
			// resolve is a POST
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(resolveBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(lyricsBody)}, nil
		}
	})

	candidates, _, err := fetchLyricify(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricify() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingLine {
		t.Fatalf("candidates = %#v, want one line candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "Lyricify", "elrc")
}

func TestLyricifyCandidates_MissingIsrcIsForbidden(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "api.credits.fm/v1/resolve/track") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBZZZ0000000","match_status":"matched"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 403, Body: []byte(`{"status":403}`)}, nil
	})

	candidates, health, err := fetchLyricify(rt, lyrics.TrackInfo{Title: "Rare", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v, want clean miss on missing ISRC", candidates, err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy (a missing ISRC is a clean miss)", health)
	}
}

func TestMalformedLYLPayloadStaysDegraded(t *testing.T) {
	t.Parallel()

	// The lyricify case must reach the LYL parser: a valid resolve (ISRC) and
	// a valid lyrics envelope with an invalid LYL body.
	resolveRequests, lyricsRequests := 0, 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "credits.fm"):
			resolveRequests++
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBAHS1600463"}`)}, nil
		default:
			lyricsRequests++
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"text":"not a Lyricify payload at all","offset":0}`)}, nil
		}
	})

	_, health, err := fetchLyricify(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err == nil || health != Degraded {
		t.Fatalf("result = (%v, %v); want the invalid LYL body to degrade", err, health)
	}
	if resolveRequests != 1 || lyricsRequests != 1 {
		t.Fatalf("requests = %d/%d, want the resolve and lyrics hops both reached", resolveRequests, lyricsRequests)
	}
}

func TestLyricifyCandidates_DoNotAskForGzip(t *testing.T) {
	t.Parallel()

	var lyricEncoding, lyricContentType string
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.credits.fm/v1/resolve/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBQCP1400011"}`)}, nil
		default:
			lyricEncoding = request.Headers["Accept-Encoding"]
			lyricContentType = request.Headers["Content-Type"]
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"text":"[type:LyricifyLines]\n[1000,500]plain"}`)}, nil
		}
	})

	if _, _, err := fetchLyricify(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget)); err != nil {
		t.Fatalf("fetchLyricify() error = %v, want nil", err)
	}
	if lyricEncoding == "gzip" {
		t.Fatalf("Accept-Encoding = %q, want identity (the host client does not auto-decompress, so a gzip body would fail every decode)", lyricEncoding)
	}
	if lyricContentType != "" {
		t.Fatalf("lyrics GET Content-Type = %q, want none for a GET", lyricContentType)
	}
}

func TestLyricifyCandidates_WordOutputLabelsAsElrc(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.credits.fm/v1/resolve/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBQCP1400011"}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"text":"[type:LyricifyLines]\n[from:AppleSyllable]\n[100,400]Hel(100,200) (200,100)lo(300,100)"}`)}, nil
		}
	})

	candidates, _, err := fetchLyricify(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricify() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "Lyricify", "elrc")
}
