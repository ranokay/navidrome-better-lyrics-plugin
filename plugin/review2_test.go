package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// Codex round 2: live-fixture wire-shape fixes and sweep/lookup semantics.

func TestNetEaseCandidates_MatchesLiveDTDuration(t *testing.T) {
	t.Parallel()

	// The live NetEase cloudsearch payload stores duration as `dt` (ms).
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		switch {
		case strings.Contains(request.URL, "cloudsearch/pc"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":17822787,"name":"Live Forever","dt":280000}]}}`)}, nil
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
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":"7425699317225293865","duration":130032}}}]}]}`)}, nil
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

func TestMusixmatchCandidates_TrackLengthDuration(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_length":10}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":404}},"track.subtitles.get":{"header":{"status_code":200},"body":{"subtitle_list":[{"subtitle_body":"[00:01.00]line"}]}}}}}}`)}, nil
		}
	})

	candidates, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatchLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want the line candidate matched via track_length", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Musixmatch", "lrc")
}

func TestSweep_MissesMarkDoneCovered(t *testing.T) {
	t.Parallel()

	// A source that answers "no lyrics here" is covered sweep work: without
	// counting misses the done marker could never be written for catalogs whose
	// sources cleanly miss, so every lookup kept re-enqueueing the sweep.
	cache := newFakeLyricsCache()
	one, two := new(int), new(int)
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{
		sweepSource("one", one, false, nil),
		sweepSource("two", two, false, nil),
	}, func(lyrics.TrackInfo) error { return nil })

	provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if _, exists, _ := cache.Get(sweepDoneMarkerKey(track, provider.activeSources())); !exists {
		t.Fatal("clean source misses are covered sweep work: the done marker must be recorded")
	}
}

func TestSweep_RefusedWritesStayUncovered(t *testing.T) {
	t.Parallel()

	// Candidates that could not be stored (quota-refused writes) are not
	// covered work; marking the sweep done would freeze the track without any
	// cached outcome (Codex, "M marks cached-only-after-real-store").
	cache := newFakeLyricsCache()
	cache.setError = errors.New("kv at quota")
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{
		sweepSource("only", new(int), true, nil),
	}, func(lyrics.TrackInfo) error { return nil })

	provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if _, exists, _ := cache.Get(sweepDoneMarkerKey(track, provider.activeSources())); exists {
		t.Fatal("a refused cache write is not sweep coverage: no done marker may be written")
	}
}

func TestGetLyrics_CachedMissStillSchedulesIncompleteSweep(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	enqueued := 0
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("one", new(int), false, nil),
		sweepSource("two", new(int), false, nil),
		sweepSource("three", new(int), false, nil),
		sweepSource("tail", new(int), false, nil),
	}, func(lyrics.TrackInfo) error {
		enqueued++
		return nil
	})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}
	if enqueued != 1 {
		t.Fatalf("enqueue count = %d, want 1 (stage one scheduled the sweep)", enqueued)
	}

	// Simulate the dedupe marker expiring after a short sweep so the next
	// lookup can retry: the negative winner must not block it (Codex).
	trackKey, _ := lyricsTrackKey(track)
	markerKey := sweepMarkerPrefix + trackKey
	if _, ok, _ := cache.Get(markerKey); !ok {
		t.Fatal("expected the dedupe marker from the first sweep")
	}
	cache.mu.Lock()
	delete(cache.values, markerKey)
	cache.mu.Unlock()

	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("second GetLyrics() error = %v, want nil", err)
	}
	if enqueued != 2 {
		t.Fatalf("enqueue count = %d, want 2 (cached misses must still retry incomplete sweeps)", enqueued)
	}
}

func TestGetLyrics_TieReRankRespectsConfiguredOrder(t *testing.T) {
	t.Parallel()

	// An earlier configured source failed during stage one and a later source
	// supplied the winner; the sweep then cached an equal-quality result for
	// the earlier source. The tie must resolve to configured order.
	earlierFailed := true
	earlier := lyricsSource{
		ID: "earlier", Display: "earlier", Hosts: []string{"earlier.example.test"}, DefaultEnabled: true,
		Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			if earlierFailed {
				return nil, lookupDegraded, errors.New("earlier down")
			}
			return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]earlier line"}, "earlier", "lrc")}, lookupHealthy, nil
		},
	}
	sources := []lyricsSource{
		earlier,
		stubFetchedCandidate("later", "later", "lrc", "[00:01.00]later line"),
	}
	cache := newFakeLyricsCache()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })

	first, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, first.Source, "later", "lrc")

	// The sweep caches an equal-quality candidate for the earlier source.
	if !provider.cacheSourceCandidate(track, "earlier", newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]earlier line"}, "earlier", "lrc")) {
		t.Fatal("expected the earlier-source candidate to be stored")
	}

	reread := newLyricsProviderWithSourcesAt(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	reRanked, err := reread.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("re-read GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, reRanked.Source, "earlier", "lrc")
}

func TestGetLyrics_AllDisabledSkipsScheduling(t *testing.T) {
	t.Parallel()

	// An injected empty catalog must serve nothing without network or queue
	// traffic.
	emptyInjected := newLyricsProviderWithSourcesAt(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		t.Errorf("unexpected network request with an empty catalog: %q", request.URL)
		return nil, nil
	}, newFakeLyricsCache(), time.Now, []lyricsSource{}, func(lyrics.TrackInfo) error {
		t.Error("unexpected sweep enqueue with an empty catalog")
		return nil
	})

	result, err := emptyInjected.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 0 {
		t.Fatalf("lyrics = %#v, want empty", result.Lyrics)
	}
}

func TestGetLyrics_ConfigWithEverySourceDisabledSkipsScheduling(t *testing.T) {
	t.Parallel()

	// The documented all-disabled configuration saves every catalog source
	// with its toggle off, resolving to an empty source order — the lookup
	// must return without filling the task queue with unschedulable sweeps.
	selections := make([]sourceSelection, 0, len(allSources))
	for _, source := range allSources {
		selections = append(selections, sourceSelection{ID: source.ID, Enabled: false})
	}
	raw, err := json.Marshal(selections)
	if err != nil {
		t.Fatalf("marshal selections: %v", err)
	}

	enqueued := 0
	provider := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		t.Errorf("unexpected network request with every source disabled: %q", request.URL)
		return nil, nil
	}, newFakeLyricsCache(), time.Now, func(key string) (string, bool) {
		if key == "sources" {
			return string(raw), true
		}
		return "", false
	}, func(lyrics.TrackInfo) error {
		enqueued++
		return nil
	})

	resolve := readProviderConfig(provider.configLookup)
	if len(resolve.Sources) != 0 {
		t.Fatalf("resolved sources = %s, want an all-disabled empty order", sourceIDs(resolve.Sources))
	}
	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}}); err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if enqueued != 0 {
		t.Fatalf("enqueues = %d, want 0 (the all-disabled opt-out must not fill the task queue)", enqueued)
	}
}

func TestLiveSearchFixturesDecodeWithRealWireNames(t *testing.T) {
	t.Parallel()

	// Pin the captured wire shapes: the live payloads disagree with earlier
	// assumptions (NetEase `dt`, Musixmatch `track_length`, quoted Luna ids)
	// and each mismatch silently neutralized the source.
	netease := jsonUnmarshal[neteaseSearchEnvelope](t, "testdata/netease/search.json")
	song, ok := pickNetEaseSong(netease.Result.Songs, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 277})
	if !ok {
		t.Fatalf("pickNetEaseSong() over the live wire missed; songs = %#v", netease.Result.Songs[:1])
	}
	if song.ID != 17822787 || song.Duration != 276866 {
		t.Fatalf("netease song = %#v, want id 17822787 dur 276866 from `dt`", song)
	}

	musixmatch := jsonUnmarshal[musixmatchEnvelope](t, "testdata/musixmatch/search.json")
	if len(musixmatch.Message.Body.TrackList) == 0 {
		t.Fatal("live Musixmatch fixture has no track_list")
	}
	track := musixmatch.Message.Body.TrackList[0].Track
	if track.TrackID != 360230953 || track.Duration != 234 {
		t.Fatalf("musixmatch track = %#v, want id 360230953 dur 234 from `track_length`", track)
	}

	luna := jsonUnmarshal[lunaSearchEnvelope](t, "testdata/luna/search.json")
	id, found := pickLunaTrack(luna.ResultGroups, lyrics.TrackInfo{Title: "whatever", Artist: "still", Duration: 130})
	if !found || id != "7425699317225293865" {
		t.Fatalf("pickLunaTrack() = (%q, %v); want the live quoted id", id, found)
	}
}

func jsonUnmarshal[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return value
}

func TestSweep_DoneMarkerTTLBoundedByShortestCoveredCandidate(t *testing.T) {
	t.Parallel()

	// A plain tail result lives one hour per source and five minutes as a
	// winner; the done marker must not suppress re-sweeps for 24 hours after
	// that (Codex): the marker cannot outlive the shortest cached result.
	for _, tc := range []struct {
		name   string
		text   string
		format string
		want   int64
	}{
		{name: "plain tail result bounds the marker to one hour", text: "some plain words", format: "plain", want: unsyncedLyricsCacheTTLSeconds},
		{name: "line result bounds the marker to a day", text: "[00:01.00]timed", format: "lrc", want: syncedLyricsCacheTTLSeconds},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cache := newFakeLyricsCache()
			provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{
				sweepSource("lead", new(int), false, nil),
				stubFetchedCandidate("tail", "tail", tc.format, tc.text),
			}, func(lyrics.TrackInfo) error { return nil })

			provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})

			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			key := sweepDoneMarkerKey(track, provider.activeSources())
			ttl, ok := cache.ttlsForKey[key]
			if !ok {
				t.Fatalf("done marker missing for %s coverage", tc.format)
			}
			if ttl != tc.want {
				t.Fatalf("done marker TTL = %d, want %d", ttl, tc.want)
			}
		})
	}
}

func TestSweep_ContinuationCompletionFingerprintsFullOrder(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	t.Run("uncovered siblings block the marker", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{
			sweepSource("one", new(int), false, nil),
			sweepSource("two", new(int), false, nil),
		}, func(lyrics.TrackInfo) error { return nil })

		status, _ := provider.runSweepSources(track, []string{"two"})
		if _, exists, _ := cache.Get(sweepDoneMarkerKey(track, provider.activeSources())); exists {
			t.Fatalf("continuation wrote a done marker although source one was never covered; status = %q", status)
		}
	})

	t.Run("cached siblings complete the full order", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{
			sweepSource("one", new(int), false, nil),
			sweepSource("two", new(int), false, nil),
		}, func(lyrics.TrackInfo) error { return nil })

		candidate := newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "one", "lrc")
		if !provider.cacheSourceCandidate(track, "one", candidate) {
			t.Fatal("expected source one's candidate to be stored")
		}
		provider.runSweepSources(track, []string{"two"})

		if _, exists, _ := cache.Get(sweepDoneMarkerKey(track, provider.activeSources())); !exists {
			t.Fatal("full-order coverage is satisfied by the cached sibling plus the continuation; the done marker must be recorded")
		}
	})
}

func TestGetLyrics_CoveredNegativePassSkipsSweep(t *testing.T) {
	t.Parallel()

	// When every implemented source fits in the stage-one walk and all of them
	// return none, the negative winner already records complete coverage;
	// queueing a sweep would refetch the same misses (Codex).
	cache := newFakeLyricsCache()
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("one", new(int), false, nil),
		sweepSource("two", new(int), false, nil),
	}, func(lyrics.TrackInfo) error {
		t.Error("unexpected sweep enqueue after a fully covered negative pass")
		return nil
	})

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 0 {
		t.Fatalf("lyrics = %#v, want empty", result.Lyrics)
	}
	if _, ok, _ := cache.Get(winnerKeyForTest(t, cache, provider.activeSources(), lyrics.TrackInfo{Title: "Song", Artist: "Artist"})); !ok {
		t.Fatal("expected the covered negative winner to be cached")
	}
}

func winnerKeyForTest(t *testing.T, cache lyricsCacheStore, order []lyricsSource, track lyrics.TrackInfo) string {
	t.Helper()
	key, ok := lyricsWinnerCacheKey(track, sourceIDs(order))
	if !ok {
		t.Fatal("expected a winner cache key")
	}
	return key
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

func TestLRCLibWordSeparatorsSurviveParsing(t *testing.T) {
	t.Parallel()

	// LRCLib stores spacing only in the line text; token rendering must not
	// fuse the words into "firstline" (Codex).
	const yamlDoc = "version: '1.0'\n" +
		"lines:\n" +
		"- start_ms: 1000\n" +
		"  end_ms: 2500\n" +
		"  text: first line\n" +
		"  words:\n" +
		"  - text: first\n" +
		"    start_ms: 1000\n" +
		"    end_ms: 1500\n" +
		"  - text: line\n" +
		"    start_ms: 1500\n" +
		"    end_ms: 2500\n"

	lines, err := parseLRCLibLyricsFile([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("parseLRCLibLyricsFile() error = %v, want nil", err)
	}
	rendered := enhancedLRCFromTimedLines(lines)
	if !strings.Contains(rendered, "<00:01.00>first <00:01.50>line") {
		t.Fatalf("rendered = %q, want the separator restored between the word tokens", rendered)
	}
	if strings.Contains(rendered, "firstline") {
		t.Fatalf("rendered = %q, fused the word tokens", rendered)
	}
}

func TestMusixmatchMintFailuresStayDegraded(t *testing.T) {
	t.Parallel()

	t.Run("transport failure propagates", func(t *testing.T) {
		provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return nil, errors.New("segmentation unreachable in the token.get")
		})
		_, health, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatal("transport failure converted to a healthy clean miss; want a degraded error")
		}
		if health == lookupHealthy {
			t.Fatalf("health = %v, want degraded on an operational failure", health)
		}
	})

	t.Run("http error propagates", func(t *testing.T) {
		provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 503, Body: []byte("kaput")}, nil
		})
		_, health, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatal("5xx mint converted to a healthy clean miss; want a degraded error")
		}
		if health == lookupHealthy {
			t.Fatalf("health = %v, want degraded on an operational failure", health)
		}
	})

	t.Run("token challenge stays quiet", func(t *testing.T) {
		provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 401, Body: []byte("denied")}, nil
		})
		_, health, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
		if err != nil || health != lookupHealthy {
			t.Fatalf("challenge = (%v, %v); want a healthy quiet miss", err, health)
		}
	})
}

func TestBinimumRejectsMetadataMismatch(t *testing.T) {
	t.Parallel()

	// The live search for "Live Forever"/Oasis also carries an unrelated row
	// with a matching 340 s duration (Codex); the selector must not pick it.
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

func TestMusixmatchEnvelopeStatusesClassified(t *testing.T) {
	t.Parallel()

	facade := func(searchEnvelope, macroEnvelope string) func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			switch {
			case strings.Contains(request.URL, "/ws/1.1/token.get"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
			case strings.Contains(request.URL, "/ws/1.1/track.search"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchEnvelope)}, nil
			default:
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(macroEnvelope)}, nil
			}
		}
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}

	t.Run("server envelope failures degrade instead of missing", func(t *testing.T) {
		provider := newLyricsProvider(facade(`{"message":{"header":{"status_code":500}}}`, `{}`))
		_, health, err := fetchMusixmatchLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want degraded on envelope 500", err, health)
		}
	})

	t.Run("envelope 429 activates the provider cooldown", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(facade(`{"message":{"header":{"status_code":429}}}`, `{}`), cache, time.Now, nil, nil)
		_, health, err := fetchMusixmatchLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want degraded on envelope 429", err, health)
		}
		if provider.activeCooldown(sourceGlobalCooldownKey("musixmatch")) <= 0 {
			t.Fatal("envelope 429 activated no cooldown")
		}
	})

	t.Run("envelope 404 and no-match stay quiet misses", func(t *testing.T) {
		provider := newLyricsProvider(facade(`{"message":{"header":{"status_code":404}}}`, `{}`))
		candidates, health, err := fetchMusixmatchLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err != nil || len(candidates) != 0 || health != lookupHealthy {
			t.Fatalf("result = %#v/%v/%v; want clean miss on envelope 404", candidates, health, err)
		}
	})

	t.Run("rate-limited macro payloads degrade the lookup", func(t *testing.T) {
		provider := newLyricsProvider(facade(
			`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_length":10}}]}}}`,
			`{"message":{"header":{"status_code":200},"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":429}}}}}}`))
		_, health, err := fetchMusixmatchLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want a degraded lookup when a rate-limited macro spoils the pass", err, health)
		}
	})
}
