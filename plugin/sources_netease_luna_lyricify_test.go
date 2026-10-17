package main

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const neteaseSearchBody = `{"result":{"songs":[{"id":17822787,"name":"Live Forever","ar":[{"name":"Oasis"}],"dt":280000}]}}`
const neteaseLrcBody = `{"lrc":{"lyric":"{\"t\":0,\"c\":[{\"tx\":\"作词: \"},{\"tx\":\"Noel Gallagher\"}]}\n[00:11.93]Maybe\n"}, "tlyric":{}, "yrc":{}}`
const neteaseYrcBody = `{"lrc":{"lyric":""}, "yrc":{"lyric":"[10000,3000](10200,500,0)Hey (10700,500,0)there\n"}}`

func TestLunaPickFailsClosedWithoutNormalizedIdentity(t *testing.T) {
	t.Parallel()

	// The collapsed-containment gate treats an empty needle as matching every
	// row (strings.Contains(x, "") is true), and an artist that collapses to
	// nothing used to accept any name.
	row := lunaGroupDatum{Entity: struct {
		Track lunaSearchTrack `json:"track"`
	}{Track: lunaSearchTrack{ID: "42", Name: "Live Forever", Artists: []lunaSearchArtist{{Name: "Oasis"}}, Duration: 240000}}}
	groups := []lunaResultGroup{{Data: []lunaGroupDatum{row}}}

	if id, found := pickLunaTrack(groups, lyrics.TrackInfo{Title: "🎵", Artist: "Oasis"}); found {
		t.Fatalf("pickLunaTrack accepted %q for an unverifiable title", id)
	}
	if id, found := pickLunaTrack(groups, lyrics.TrackInfo{Title: "Live Forever", Artist: "???"}); found {
		t.Fatalf("pickLunaTrack accepted %q for an unverifiable artist", id)
	}
}

func TestNetEaseCandidates_WordsFromYRC(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseSearchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseYrcBody)}, nil
		}
	})

	candidates, health, err := fetchNetEaseLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchNetEaseLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].quality != timingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "NetEase", "elrc")
	if !strings.Contains(candidates[0].response.Lyrics[0].Text, "<00:10.20>Hey") {
		t.Fatalf("text = %q, want word markers", candidates[0].response.Lyrics[0].Text)
	}
}

func TestNetEaseCandidates_StripsRichJsonCreditLines(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseSearchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseLrcBody)}, nil
		}
	})

	candidates, _, err := fetchNetEaseLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchNetEaseLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want line candidate", candidates)
	}
	if strings.Contains(candidates[0].response.Lyrics[0].Text, `"t":0`) {
		t.Fatalf("text = %q, want JSON credit lines stripped", candidates[0].response.Lyrics[0].Text)
	}
}

func TestNetEaseCandidates_NoDurationMatchMeansNoLyricCall(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := newLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(neteaseSearchBody)}, nil
	})

	candidates, health, err := fetchNetEaseLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 99,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss", candidates, health, err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want search only", requests)
	}
}

func TestLunaCandidates_WordsFromPlainKRC(t *testing.T) {
	t.Parallel()

	searchBody := `{"result_groups":[{"data":[{"entity":{"track":{"id":7425699317225293865,"name":"Live Forever (Live)","artists":[{"name":"Oasis"}],"duration":280000}}}]}]}`
	detailBody := `{"lyric":{"content":"[400,4900]<0,1040,0>Beneath <1040,280,0>an <1320,240,0>empty\n","type":"krc"}}`
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.qishui.com/luna/search/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(detailBody)}, nil
		}
	})

	candidates, _, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLunaLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Soda (Luna)", "elrc")
}

func TestLunaCandidates_LineLRCWithoutWordData(t *testing.T) {
	t.Parallel()

	searchBody := `{"result_groups":[{"data":[{"entity":{"track":{"id":1,"name":"Live Forever","artists":[{"name":"Oasis"}],"duration":280000}}}]}]}`
	detailBody := `{"lyric":{"content":"[00:04.00]Beneath an empty\n","type":"lrc"}}`
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.qishui.com/luna/search/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(detailBody)}, nil
		}
	})

	candidates, _, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLunaLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want one line candidate", candidates)
	}
}

func TestLyricifyCandidates_ISRCResolveThenFetch(t *testing.T) {
	t.Parallel()

	resolveBody := `{"credits_id":"x","isrc":"GBQCP1400011","match_status":"matched"}`
	lyricsBody := `{"text":"[type:LyricifyLines]\n[9731,14858]The club isn't the best place\n","offset":0,"isInstrumental":false}`
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.credits.fm/v1/resolve/track"):
			// resolve is a POST
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(resolveBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(lyricsBody)}, nil
		}
	})

	candidates, _, err := fetchLyricifyLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricifyLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want one line candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Lyricify", "elrc")
}

func TestLyricifyCandidates_MissingIsrcIsForbidden(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "api.credits.fm/v1/resolve/track") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBZZZ0000000","match_status":"matched"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 403, Body: []byte(`{"status":403}`)}, nil
	})

	candidates, health, err := fetchLyricifyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Rare", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v, want clean miss on missing ISRC", candidates, err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy (a missing ISRC is a clean miss)", health)
	}
}
