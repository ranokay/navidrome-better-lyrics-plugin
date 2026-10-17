package source

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const neteaseSearchBody = `{"result":{"songs":[{"id":17822787,"name":"Live Forever","ar":[{"name":"Oasis"}],"dt":280000}]}}`
const neteaseLrcBody = `{"lrc":{"lyric":"{\"t\":0,\"c\":[{\"tx\":\"作词: \"},{\"tx\":\"Noel Gallagher\"}]}\n[00:11.93]Maybe\n"}, "tlyric":{}, "yrc":{}}`
const neteaseYrcBody = `{"lrc":{"lyric":""}, "yrc":{"lyric":"[10000,3000](10200,500,0)Hey (10700,500,0)there\n"}}`

func TestNetEaseCandidates_WordsFromYRC(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseSearchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseYrcBody)}, nil
		}
	})

	candidates, health, err := fetchNetEase(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchNetEase() error = %v, want nil", err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "NetEase", "elrc")
	if !strings.Contains(candidates[0].Response.Lyrics[0].Text, "<00:10.20>Hey") {
		t.Fatalf("text = %q, want word markers", candidates[0].Response.Lyrics[0].Text)
	}
}

func TestNetEaseCandidates_StripsRichJsonCreditLines(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseSearchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseLrcBody)}, nil
		}
	})

	candidates, _, err := fetchNetEase(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchNetEase() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingLine {
		t.Fatalf("candidates = %#v, want line candidate", candidates)
	}
	if strings.Contains(candidates[0].Response.Lyrics[0].Text, `"t":0`) {
		t.Fatalf("text = %q, want JSON credit lines stripped", candidates[0].Response.Lyrics[0].Text)
	}
}

func TestNetEaseCandidates_NoDurationMatchMeansNoLyricCall(t *testing.T) {
	t.Parallel()

	requests := 0
	rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseSearchBody)}, nil
	})

	candidates, health, err := fetchNetEase(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 99,
	}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 0 || health != Healthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss", candidates, health, err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want search only", requests)
	}
}

func TestNetEaseCandidates_MatchesLiveDTDuration(t *testing.T) {
	t.Parallel()

	// The live NetEase cloudsearch payload stores duration as `dt` (ms).
	requests := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":17822787,"name":"Live Forever","ar":[{"name":"Oasis"}],"dt":280000}]}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lrc":{"lyric":"[00:11.93]Maybe\n"},"yrc":{}}`)}, nil
		}
	})

	candidates, health, err := fetchNetEase(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchNetEase() error = %v, want nil", err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one after the dt-field duration decode", candidates)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want search then lyric", requests)
	}
}

func TestNetEaseSearchEnvelopeStatusesClassified(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}
	t.Run("search envelope 500 degrades", func(t *testing.T) {
		rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":500}`)}, nil
		})
		_, health, err := fetchNetEase(rt, track, time.Now().Add(testLookupBudget))
		if err == nil || health == Healthy {
			t.Fatalf("result = (%v, %v); want degraded on envelope 500", err, health)
		}
	})

	t.Run("search envelope 429 activates cooldown", func(t *testing.T) {
		cache := newFakeStore()
		rt := newFakeRuntimeWithCache(func(host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":429}`)}, nil
		}, cache, time.Now)
		_, _, err := fetchNetEase(rt, track, time.Now().Add(testLookupBudget))
		if err == nil {
			t.Fatal("envelope 429 converted to a miss; want a degraded error")
		}
		if rt.Cooldown(GlobalCooldownKey("netease")) <= 0 {
			t.Fatal("envelope 429 activated no cooldown")
		}
	})

	t.Run("search envelope 404 keeps a quiet miss", func(t *testing.T) {
		rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":404}`)}, nil
		})
		_, health, err := fetchNetEase(rt, track, time.Now().Add(testLookupBudget))
		if err != nil || health != Healthy {
			t.Fatalf("result = (%v, %v); want the 404 kept quiet", err, health)
		}
	})
}

func TestNetEaseLyricEnvelopeFailureDegrades(t *testing.T) {
	t.Parallel()

	// The lyric envelope carries its own code: a transient failure must not
	// fall through as cacheable coverage.
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":111,"name":"Song","ar":[{"name":"Artist"}],"dt":280000}]},"code":200}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":500}`)}, nil
		}
	})

	_, health, err := fetchNetEase(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 280}, time.Now().Add(testLookupBudget))
	if err == nil || health != Degraded {
		t.Fatalf("result = (%v, %v); want degraded on the lyric envelope 500", err, health)
	}
}

func TestNetEaseMalformedYRCDegrades(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":111,"name":"Song","ar":[{"name":"Artist"}],"dt":240000}]},"code":200}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200,"lrc":{"lyric":""},"yrc":{"lyric":"(not a YRC envelope at all"}}`)}, nil
		}
	})

	_, health, err := fetchNetEase(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 240}, time.Now().Add(testLookupBudget))
	if err == nil || health != Degraded {
		t.Fatalf("result = (%v, %v); want the malformed YRC to degrade", err, health)
	}
}

func TestNetEaseFormContentType(t *testing.T) {
	t.Parallel()

	var contentType string
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "cloudsearch/pc") {
			contentType = request.Headers["Content-Type"]
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":1,"duration":100000}]}}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	if _, _, err := fetchNetEase(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 100}, time.Now().Add(testLookupBudget)); err != nil {
		t.Fatalf("fetchNetEase() error = %v, want nil", err)
	}
	if contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("NetEase search Content-Type = %q, want form-urlencoded (opaque bodies break server-side form parsing)", contentType)
	}
}
