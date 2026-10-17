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

func TestNetEaseCandidates_MatchesLiveDTDuration(t *testing.T) {
	t.Parallel()

	// The live NetEase cloudsearch payload stores duration as `dt` (ms).
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":17822787,"name":"Live Forever","ar":[{"name":"Oasis"}],"dt":280000}]}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lrc":{"lyric":"[00:11.93]Maybe\n"},"yrc":{}}`)}, nil
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
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one after the dt-field duration decode", candidates)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want search then lyric", requests)
	}
}

func TestLunaCandidates_QuotedTrackIDs(t *testing.T) {
	t.Parallel()

	// Live Luna responses quote every track id; unmarshalling them into int64
	// used to abort the whole search decode before any candidate selection.
	var detailRequest string
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, lunaSearchEndpoint):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":"7425699317225293865","name":"liveforeverOasis","artists":[{"name":"又山"}],"duration":130032}}}]}]}`)}, nil
		default:
			detailRequest = request.URL
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"[00:04.00]Beneath an empty","type":"lrc"}}`)}, nil
		}
	})

	candidates, health, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 130,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLunaLyricsCandidates() error = %v, want nil (quoted ids must not abort the decode)", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want one line candidate sourced from the quoted id", candidates)
	}
	if !strings.Contains(detailRequest, "track_id=7425699317225293865") {
		t.Fatalf("detail request = %q, want the quoted id carried through", detailRequest)
	}
}

func TestLunaTrackID_PreservesLargeNumericIDs(t *testing.T) {
	t.Parallel()

	var numeric lunaTrackID
	if err := numeric.UnmarshalJSON([]byte(`7425699317225293865`)); err != nil {
		t.Fatalf("UnmarshalJSON(numeric) error = %v, want nil", err)
	}
	if numeric != "7425699317225293865" {
		t.Fatalf("numeric id = %q, want the exact digit string (no float64 rounding)", string(numeric))
	}

	var quoted lunaTrackID
	if err := quoted.UnmarshalJSON([]byte(`"7425699317225293865"`)); err != nil {
		t.Fatalf("UnmarshalJSON(quoted) error = %v, want nil", err)
	}
	if quoted != "7425699317225293865" {
		t.Fatalf("quoted id = %q, want the stripped value", string(quoted))
	}
}

func TestNetEaseSearchEnvelopeStatusesClassified(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}
	t.Run("search envelope 500 degrades", func(t *testing.T) {
		provider := newLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":500}`)}, nil
		})
		_, health, err := fetchNetEaseLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want degraded on envelope 500", err, health)
		}
	})

	t.Run("search envelope 429 activates cooldown", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(func(host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":429}`)}, nil
		}, cache, time.Now, nil, nil)
		_, _, err := fetchNetEaseLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatal("envelope 429 converted to a miss; want a degraded error")
		}
		if provider.activeCooldown(sourceGlobalCooldownKey("netease")) <= 0 {
			t.Fatal("envelope 429 activated no cooldown")
		}
	})

	t.Run("search envelope 404 keeps a quiet miss", func(t *testing.T) {
		provider := newLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":404}`)}, nil
		})
		_, health, err := fetchNetEaseLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err != nil || health != lookupHealthy {
			t.Fatalf("result = (%v, %v); want the 404 kept quiet", err, health)
		}
	})
}

func TestMalformedLYLPayloadStaysDegraded(t *testing.T) {
	t.Parallel()

	// The lyricify case must reach the LYL parser: a valid resolve (ISRC) and
	// a valid lyrics envelope with an invalid LYL body.
	resolveRequests, lyricsRequests := 0, 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "credits.fm"):
			resolveRequests++
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBAHS1600463"}`)}, nil
		default:
			lyricsRequests++
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"text":"not a Lyricify payload at all","offset":0}`)}, nil
		}
	})

	_, health, err := fetchLyricifyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil || health != lookupDegraded {
		t.Fatalf("result = (%v, %v); want the invalid LYL body to degrade", err, health)
	}
	if resolveRequests != 1 || lyricsRequests != 1 {
		t.Fatalf("requests = %d/%d, want the resolve and lyrics hops both reached", resolveRequests, lyricsRequests)
	}
}

func TestNetEaseLyricEnvelopeFailureDegrades(t *testing.T) {
	t.Parallel()

	// The lyric envelope carries its own code: a transient failure must not
	// fall through as cacheable coverage.
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":111,"name":"Song","ar":[{"name":"Artist"}],"dt":280000}]},"code":200}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":500}`)}, nil
		}
	})

	_, health, err := fetchNetEaseLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 280}, time.Now().Add(stageOneLookupBudget))
	if err == nil || health != lookupDegraded {
		t.Fatalf("result = (%v, %v); want degraded on the lyric envelope 500", err, health)
	}
}

func TestLunaMalformedKRCDegrades(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, lunaSearchEndpoint) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":"5","name":"Song","artists":[{"name":"Artist"}],"duration":240000}}}]}]}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"(definitely not a KRC payload","type":"krc"}}`)}, nil
	})

	_, health, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 240}, time.Now().Add(stageOneLookupBudget))
	if err == nil || health != lookupDegraded {
		t.Fatalf("result = (%v, %v); want the malformed KRC to degrade", err, health)
	}
}

func TestNetEaseMalformedYRCDegrades(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":111,"name":"Song","ar":[{"name":"Artist"}],"dt":240000}]},"code":200}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200,"lrc":{"lyric":""},"yrc":{"lyric":"(not a YRC envelope at all"}}`)}, nil
		}
	})

	_, health, err := fetchNetEaseLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 240}, time.Now().Add(stageOneLookupBudget))
	if err == nil || health != lookupDegraded {
		t.Fatalf("result = (%v, %v); want the malformed YRC to degrade", err, health)
	}
}

func TestLunaCandidates_UnknownTypeReturnsPlainCandidate(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.qishui.com/luna/search/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":1,"name":"Live Forever","artists":[{"name":"Oasis"}],"duration":280000}}}]}]}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"[00:04.00]Beneath an empty","type":"brand-new-dialect"}}`)}, nil
		}
	})

	candidates, _, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 280}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLunaLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the usable payload returned as a candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Soda (Luna)", "plain")
}

func TestLyricifyCandidates_DoNotAskForGzip(t *testing.T) {
	t.Parallel()

	var lyricEncoding, lyricContentType string
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.credits.fm/v1/resolve/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBQCP1400011"}`)}, nil
		default:
			lyricEncoding = request.Headers["Accept-Encoding"]
			lyricContentType = request.Headers["Content-Type"]
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"text":"[type:LyricifyLines]\n[1000,500]plain"}`)}, nil
		}
	})

	if _, _, err := fetchLyricifyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("fetchLyricifyLyricsCandidates() error = %v, want nil", err)
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

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.credits.fm/v1/resolve/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"isrc":"GBQCP1400011"}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"text":"[type:LyricifyLines]\n[from:AppleSyllable]\n[100,400]Hel(100,200) (200,100)lo(300,100)"}`)}, nil
		}
	})

	candidates, _, err := fetchLyricifyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricifyLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Lyricify", "elrc")
}
