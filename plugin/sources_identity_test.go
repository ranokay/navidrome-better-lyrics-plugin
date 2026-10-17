package main

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestBinimumRejectsMetadataMismatch(t *testing.T) {
	t.Parallel()

	// The live search for "Live Forever"/Oasis also carries an unrelated row
	// with a matching 340 s duration; the selector must not pick it.
	rcalcioResults := []binimumResult{
		{
			TrackName:  "I Could Sing of Your Love Forever (Live)",
			ArtistName: "Oasis Worship",
			AlbumName:  "Cutting Edge",
			Duration:   340,
			LyricsURL:  "https://lrc.red/s/WRONG.ttml",
		},
		{
			TrackName:  "Live Forever (Remastered)",
			ArtistName: "Oasis",
			AlbumName:  "Time Flies",
			Duration:   339,
			LyricsURL:  "https://lrc.red/s/RIGHT.ttml",
		},
	}

	best, found := pickBinimumResult(rcalcioResults, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 340})
	if !found {
		t.Fatal("pickBinimumResult() found nothing, want the metadata-matching row")
	}
	if best.LyricsURL != "https://lrc.red/s/RIGHT.ttml" {
		t.Fatalf("picked = %q %q, want the row with matching metadata", best.TrackName, best.ArtistName)
	}

	// Nothing matches metadata at all: from a mismatched-only catalog there is
	// no candidate, even though durations line up.
	if _, found := pickBinimumResult(rcalcioResults[:1], lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 340}); found {
		t.Fatal("duration-only metadata match accepted an unrelated song")
	}
}

func TestNetEaseCandidates_RequireMetadataMatch(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[
				{"id":111,"name":"I Could Sing of Your Love Forever","ar":[{"name":"Oasis Worship"}],"dt":280000},
				{"id":222,"name":"Live Forever (Live)","ar":[{"name":"Oasis"}],"dt":340000}
			]}}`)}, nil
		case strings.Contains(request.URL, "lyric/v1"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lrc":{"lyric":"[00:11.93]Maybe\n"},"yrc":{}}`)}, nil
		default:
			t.Errorf("unexpected request %q", request.URL)
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
	})

	candidates, _, err := fetchNetEaseLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 340}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchNetEaseLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the metadata-matching candidate only (the unrelated duration-only row must be rejected)", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "NetEase", "lrc")

	provider = newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":111,"name":"Wrong Song","ar":[{"name":"Somebody"}],"dt":340000}]}}`)}, nil
	})
	if candidates, health, err := fetchNetEaseLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 340}, time.Now().Add(stageOneLookupBudget)); err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want a clean miss when no row matches the metadata", candidates, health, err)
	}
}

func TestLunaCandidates_GateMatchesByMetadata(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		switch {
		case strings.Contains(request.URL, lunaSearchEndpoint):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[
				{"entity":{"track":{"id":"11","name":"Different Song","artists":[{"name":"Elsewhere"}],"duration":130032}}},
				{"entity":{"track":{"id":"22","name":"liveforeverOasis","artists":[{"name":"又山"}],"duration":130032}}}
			]}]}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"[00:04.00]Beneath an empty","type":"lrc"}}`)}, nil
		}
	})

	candidates, health, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 130}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLunaLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v, want the duration-tied unrelated row rejected and the UGC row accepted", candidates, health)
	}

	provider = newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, lunaSearchEndpoint):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":"11","name":"Different Song","artists":[{"name":"Elsewhere"}],"duration":130032}}},{"entity":{"track":{"id":"33","name":"Live Forever (Live)","artists":[{"name":"Oasis"}],"duration":130032}}}]}]}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"[00:04.00]Beneath an empty","type":"lrc"}}`)}, nil
		}
	})
	candidates, _, err = fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 130}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v, want the live-titled Oasis row accepted over the unrelated row", candidates, err)
	}
	if candidates[0].quality != timingLine {
		t.Fatalf("quality = %v, want the timed line from the metadata-matched row", candidates[0].quality)
	}

	provider = newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":"11","name":"Different Song","artists":[{"name":"Elsewhere"}],"duration":130032}}}]}]}`)}, nil
	})
	candidates, _, err = fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 130}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v, want a clean miss when no row matches the metadata", candidates, err)
	}
}

func TestLunaBeatMatchesArtistAliases(t *testing.T) {
	t.Parallel()

	// The hub index lists several aliases per artist (`赵雷`, `趙雷`, `Lei Zhao`);
	// joining them into one string never matches a track tagged with a single
	// alias.
	index := []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"e","title":"阿刁","artists":["赵雷","趙雷","Lei Zhao"],"album":"剃刀","path":"lyrics/cjk.ttml"}]}`)
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: index}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt xmlns:itunes="urn:itunes"><p begin="1" end="2"><span begin="1" end="2">stub line</span></p></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now, "luna-beat")

	for _, alias := range []string{"Lei Zhao", "趙雷"} {
		if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "阿刁", Artist: alias, Album: "剃刀"}, time.Now().Add(stageOneLookupBudget)); err != nil {
			t.Fatalf("fetchLunaBeatLyricsCandidates(artist %q) error = %v, want nil", alias, err)
		}
	}
}

func TestLunaBeatRejectsUnrelatedArtistSubstrings(t *testing.T) {
	t.Parallel()

	// Complete-name comparison only: "blackpink|" must never alias-match a
	// "Pink" request.
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"x","title":"Pink Song","artists":["BLACKPINK"],"album":"A","path":"lyrics/x.ttml"}]}`)}, nil
		default:
			t.Fatal("a title-only match with an unrelated artist must not fetch TTML")
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
	}, newFakeLyricsCache(), time.Now, "luna-beat")

	_, health, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "Pink Song", Artist: "Pink"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || health != lookupHealthy {
		t.Fatalf("result = (%v, %v); want a clean miss over the containment false positive", err, health)
	}

	collab := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"y","title":"Duet","artists":["Garfunkel"],"album":"B","path":"lyrics/y.ttml"}]}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt xmlns:itunes="urn:itunes"><p begin="1" end="2"><span begin="1" end="2">stub line</span></p></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now, "luna-beat")

	if _, _, err := collab.fetchLunaBeatLyricsCandidatesProvider(lyrics.TrackInfo{Title: "Duet", Artist: "Simon & Garfunkel"}); err != nil {
		t.Fatalf("multi-artist track match error = %v, want nil", err)
	}
}

func (p *lyricsProvider) fetchLunaBeatLyricsCandidatesProvider(track lyrics.TrackInfo) ([]lyricsCandidate, lookupHealth, error) {
	return fetchLunaBeatLyricsCandidates(p, track, time.Now().Add(stageOneLookupBudget))
}

func TestLunaBeatMatchesExactPunctuatedArtistNames(t *testing.T) {
	t.Parallel()

	// The whole collapsed name is a match option too: "AC/DC" split pieces
	// ("ac","dc") would never equal the hub alias "acdc".
	var ttmlRequest string
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"z","title":"Thunderstruck","artists":["AC/DC"],"album":"B","path":"lyrics/z.ttml"}]}`)}, nil
		default:
			ttmlRequest = request.URL
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt xmlns:itunes="urn:itunes"><p begin="1" end="2"><span begin="1" end="2">stub line</span></p></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now, "luna-beat")

	candidates, _, err := provider.fetchLunaBeatLyricsCandidatesProvider(lyrics.TrackInfo{Title: "Thunderstruck", Artist: "AC/DC"})
	if err != nil {
		t.Fatalf("exact punctuated artist error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the matched row turned into a candidate", candidates)
	}
	if !strings.Contains(ttmlRequest, "lyrics/z.ttml") {
		t.Fatalf("TTML fetch = %q, want the alias-matched lyrics file fetched", ttmlRequest)
	}
}

func TestMusixmatchRejectsUnrelatedSearchRows(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Wrong Song","artist_name":"Wrong Artist","track_length":240}}]}}}`)}, nil
		default:
			t.Fatalf("lyrics macro must not be requested for an unrelated row: %s", request.URL)
			return nil, nil
		}
	})

	candidates, health, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song A", Artist: "Artist A", Duration: 240}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want a clean miss", candidates, health, err)
	}
}

func TestBinimumMatchesWithoutDuration(t *testing.T) {
	t.Parallel()

	// Durationless library entries still match on identity.
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "binimum.org") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"results":[{"track_name":"Song","artist_name":"Artist","duration":240,"lyricsUrl":"https://lrc.red/s/X.ttml"}]}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt xmlns:itunes="urn:itunes"><p begin="1" end="2"><span begin="1" end="2">stub line</span></p></tt>`)}, nil
	})

	candidates, _, err := fetchBinimumLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchBinimumLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the identity match without a duration gate", candidates)
	}
}

func TestLunaRejectsContainingArtistNames(t *testing.T) {
	t.Parallel()

	// "BLACKPINK" must not alias-match a "Pink" request via substring
	// containment.
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":"77","name":"Song","artists":[{"name":"BLACKPINK"}],"duration":130032}}}]}]}`)}, nil
	})

	candidates, health, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Pink", Duration: 130}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want the containing artist rejected", candidates, health, err)
	}
}

func TestMusixmatchMatchesWithoutDuration(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":240}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"body":{"macro_calls":{"track.subtitles.get":{"header":{"status_code":200},"body":{"subtitle_list":[{"subtitle_body":"[00:01.00]line"}]}}}}}}`)}, nil
		}
	})

	candidates, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatchLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the identity match without a duration gate", candidates)
	}
}

func TestDurationGatesSkipWithoutDuration(t *testing.T) {
	t.Parallel()

	// Durationless tracks must still match on identity in every selector.
	netease, ok := pickNetEaseSong([]neteaseSong{
		{ID: 1, Name: "Song", Artists: []neteaseArtist{{Name: "Artist"}}, Duration: 240000},
	}, lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	if !ok || netease.ID != 1 {
		t.Fatalf("pickNetEaseSong = %#v/%v, want the durationless identity match", netease, ok)
	}

	var groups []lunaResultGroup
	groups = append(groups, lunaResultGroup{Data: []lunaGroupDatum{
		{Entity: struct {
			Track lunaSearchTrack `json:"track"`
		}{Track: lunaSearchTrack{ID: "77", Name: "Song", Artists: []lunaSearchArtist{{Name: "Artist"}}, Duration: 240000}}},
	}})
	if id, found := pickLunaTrack(groups, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}); !found || id != "77" {
		t.Fatalf("pickLunaTrack = (%q, %v), want the durationless identity match", id, found)
	}

	lrclibProvider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`[{"trackName":"Song","artistName":"Artist","duration":240,"plainLyrics":"words","syncedLyrics":"[00:01.00]words"}]`)}, nil
	})
	if candidates, _, err := fetchLRCLibLyricsCandidates(lrclibProvider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget)); err != nil || len(candidates) != 1 {
		t.Fatalf("lrclib durationless = %#v/%v, want the identity match", candidates, err)
	}

	appleProvider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "itunes.apple.com") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"results":[{"trackId":9,"trackName":"Song","artistName":"Artist","trackTimeMillis":240000}]}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttmlContent":"<tt xmlns:itunes=\"urn:itunes\"><p begin=\"1\">line</p></tt>"}`)}, nil
	})
	if candidates, _, err := fetchLyricallyLyricsCandidates(appleProvider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget)); err != nil || len(candidates) != 1 {
		t.Fatalf("lyrically durationless = %#v/%v, want the identity match", candidates, err)
	}
}

func TestAMLLSelectsByIdentityNotOrder(t *testing.T) {
	t.Parallel()

	// An unrelated first row must not win; the matching row (with album
	// preference) serves.
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "v1/lyrics/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[
				{"id":"111","musicNames":["Other Song"],"artistNames":["Other Artist"],"albumNames":["A"]},
				{"id":"222","musicNames":["Song"],"artistNames":["Artist"],"albumNames":["Wrong Album"]},
				{"id":"333","musicNames":["Song"],"artistNames":["Artist"],"albumNames":["Right Album"]}
			]}}`)}, nil
		case strings.Contains(request.URL, "v1/lyrics/get"):
			if !strings.Contains(request.URL, "id=333") {
				t.Fatalf("fetched id from the unrelated/less-preferred row: %s", request.URL)
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"lyrics":"<tt xmlns:itunes=\"urn:itunes\"><p begin=\"1\">line</p></tt>"}}`)}, nil
		default:
			t.Errorf("unexpected request %q", request.URL)
			return nil, nil
		}
	})

	candidates, _, err := fetchAMLLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Right Album"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %#v/%v, want the identity+album match served", candidates, err)
	}
}

func TestArtistMatcherRequiresCompleteNames(t *testing.T) {
	t.Parallel()

	// Prefix tolerance belongs to titles only: "Pink" must not match
	// "Pink Floyd".
	if artistTokensMatch("Pink Floyd", "Pink") {
		t.Fatal("Pink matched Pink Floyd")
	}
	if !artistTokensMatch("Pink Floyd", "Pink Floyd") {
		t.Fatal("exact artist names must match")
	}
	// Collaborator splitting still works in both directions.
	if !artistTokensMatch("Oasis", "Oasis & Someone") {
		t.Fatal("a listed collaborator must match the plain artist")
	}
	if !artistTokensMatch("Simon & Garfunkel", "Simon & Garfunkel") {
		t.Fatal("identical collaboration lists must match")
	}
	if artistTokensMatch("Oasis Worship", "Oasis") {
		t.Fatal("Oasis matched Oasis Worship")
	}
}

func TestBinimumRejectsContainingArtist(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "binimum.org") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"results":[{"track_name":"Song","artist_name":"Pink Floyd","duration":200,"lyricsUrl":"https://lrc.red/s/X.ttml"}]}`)}, nil
		}
		t.Fatalf("an unrelated artist row must not be fetched: %s", request.URL)
		return nil, nil
	})

	candidates, health, err := fetchBinimumLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Pink", Duration: 200}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want the containing artist rejected", candidates, health, err)
	}
}

func TestPunctuatedArtistNamesStillMatch(t *testing.T) {
	t.Parallel()

	// Whole-name equality must precede collaborator splitting: AC/DC and
	// Simon & Garfunkel collapse to single names and matched before the
	// shared matcher arrived.
	cases := []struct{ returned, wanted string }{
		{"AC/DC", "AC/DC"},
		{"Simon & Garfunkel", "Simon & Garfunkel"},
		{"Simon & Garfunkel", "Garfunkel"},
		// Variant punctuation: the whole-name path catches what splitting
		// would miss.
		{"ACDC", "AC/DC"},
	}
	for _, tc := range cases {
		if !artistTokensMatch(tc.returned, tc.wanted) {
			t.Fatalf("artistTokensMatch(%q, %q) = false, want true", tc.returned, tc.wanted)
		}
	}

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "v1/lyrics/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[{"id":"111","musicNames":["Thunderstruck"],"artistNames":["AC/DC"],"albumNames":["B"]}]}}`)}, nil
		case strings.Contains(request.URL, "v1/lyrics/get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"lyrics":"<tt xmlns:itunes=\"urn:itunes\"><body><p begin=\"1\">line</p></body></tt>"}}`)}, nil
		default:
			t.Errorf("unexpected request %q", request.URL)
			return nil, nil
		}
	})

	candidates, _, err := fetchAMLLyricsCandidates(provider, lyrics.TrackInfo{Title: "Thunderstruck", Artist: "AC/DC", Album: "B"}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %#v/%v, want the exact punctuated artist matched", candidates, err)
	}
}

func TestLunaBeatCandidates_MatchesCJKTitles(t *testing.T) {
	t.Parallel()

	index := []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"e","title":"阿刁","artists":["赵雷"],"album":"剃刀","path":"lyrics/cjk.ttml"}]}`)
	var ttmlRequest string
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: index}, nil
		default:
			ttmlRequest = request.URL
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt xmlns:itunes="urn:itunes"><p begin="1" end="2"><span begin="1" end="2">stub line</span></p></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now, "luna-beat")

	if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "阿刁", Artist: "赵雷", Album: "剃刀"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("fetchLunaBeatLyricsCandidates() error = %v, want nil", err)
	}
	if !strings.Contains(ttmlRequest, "lyrics/cjk.ttml") {
		t.Fatalf("TTML fetch = %q, want the CJK index row matched through unicode normalization", ttmlRequest)
	}
}
