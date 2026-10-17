package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	if _, exists, _ := cache.Get(provider.doneMarkerKey(track, provider.activeSources())); !exists {
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
	if _, exists, _ := cache.Get(provider.doneMarkerKey(track, provider.activeSources())); exists {
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
	id, found := pickLunaTrack(luna.ResultGroups, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 130})
	if !found || id != "7425699317225293865" {
		t.Fatalf("pickLunaTrack() = (%q, %v); want the live quoted id via the collapsed metadata gate", id, found)
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
		{name: "misses bound the re-check window to one hour", text: "[00:01.00]timed", format: "lrc", want: unsyncedLyricsCacheTTLSeconds},
		{name: "fully timed coverage still spans a day", text: "[00:02.00]timed", format: "lrc2", want: syncedLyricsCacheTTLSeconds},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cache := newFakeLyricsCache()
			tailFormat := tc.format
			lead := sweepSource("lead", new(int), false, nil)
			if tc.format == "lrc2" {
				// Both sources timed: nothing expires within the day.
				lead = stubFetchedCandidate("lead", "lead", "lrc", "[00:01.00]lead timed")
				tailFormat = "lrc"
			}
			provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{
				lead,
				stubFetchedCandidate("tail", "tail", tailFormat, tc.text),
			}, func(lyrics.TrackInfo) error { return nil })

			provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})

			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			key := provider.doneMarkerKey(track, provider.activeSources())
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
		if _, exists, _ := cache.Get(provider.doneMarkerKey(track, provider.activeSources())); exists {
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

		if _, exists, _ := cache.Get(provider.doneMarkerKey(track, provider.activeSources())); !exists {
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
		if err != nil || health != lookupUncertain {
			t.Fatalf("challenge = (%v, %v); want a temporary skip, not a covered miss", err, health)
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

func TestKFamilyOffsetShiftsTimings(t *testing.T) {
	t.Parallel()

	// KRC/QRC/YRC carry a global `[offset:±ms]` tag; dropping it left every
	// line and word window unshifted (Codex).
	positive, err := parseKFamilyLyrics(wordSyncQRC, "[offset:500]\n[1000,400]word(0,400)\n")
	if err != nil {
		t.Fatalf("parseKFamilyLyrics(offset +500) error = %v, want nil", err)
	}
	if rendered := enhancedLRCFromTimedLines(positive); !strings.HasPrefix(rendered, "[00:01.50]<00:01.50>word") {
		t.Fatalf("rendered = %q, want the +500ms offset folded into the stamp", rendered)
	}

	negative, err := parseKFamilyLyrics(wordSyncQRC, "[offset:-300]\n[500,400]word(0,400)\n")
	if err != nil {
		t.Fatalf("parseKFamilyLyrics(offset -300) error = %v, want nil", err)
	}
	if rendered := enhancedLRCFromTimedLines(negative); !strings.HasPrefix(rendered, "[00:00.20]<00:00.20>word") {
		t.Fatalf("rendered = %q, want the -300ms offset folded into the stamp", rendered)
	}

	untouched, err := parseKFamilyLyrics(wordSyncQRC, "[offset:0]\n[1000,400]word(0,400)\n")
	if err != nil {
		t.Fatalf("parseKFamilyLyrics(offset 0) error = %v, want nil", err)
	}
	if rendered := enhancedLRCFromTimedLines(untouched); !strings.HasPrefix(rendered, "[00:01.00]") {
		t.Fatalf("rendered = %q, want a zero offset to leave timings alone", rendered)
	}
}

func TestPortatoQRCEnvelopeExtraction(t *testing.T) {
	t.Parallel()

	// The QQ response wraps QRC inside an XML attribute: literal newlines let
	// the bracket parser see the rows, but XML entities stay escaped and
	// entity-encoded line breaks break parsing entirely (Codex).
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "qq") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<QrcInfos>\n<LyricInfo LyricCount=\"1\">\n<Lyric_1 LyricType=\"1\" LyricContent=\"[400,400]Rock &amp; Roll(0,400)\u000a[900,300]Go(0,300)\n\"/>\n</LyricInfo>\n</QrcInfos>","provider":"qq"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	candidates, health, err := fetchPortatoLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchPortatoLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v, want one candidate from the XML envelope", candidates, health)
	}
	rendered := candidates[0].response.Lyrics[0].Text
	if strings.Contains(rendered, "&amp;") {
		t.Fatalf("rendered = %q, want XML entities decoded", rendered)
	}
	if !strings.Contains(rendered, "Rock & Roll") || !strings.Contains(rendered, "Go") {
		t.Fatalf("rendered = %q, want the escaped lyric text unescaped and both lines parsed", rendered)
	}
}

func TestLunaBeatIndexFailuresStayDegraded(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}
	facade := func(manifestStatus, indexStatus int32) func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			switch {
			case strings.Contains(request.URL, "manifest.json"):
				if manifestStatus == 0 {
					return &host.HTTPResponse{StatusCode: 404}, nil
				}
				return &host.HTTPResponse{StatusCode: manifestStatus, Body: []byte(`{"revision":"r1"}`)}, nil
			case strings.Contains(request.URL, "songs.json"):
				return &host.HTTPResponse{StatusCode: indexStatus, Body: []byte(`{"schemaVersion":6,"revision":"r1","songs":[]}`)}, nil
			default:
				t.Errorf("unexpected request %q", request.URL)
				return &host.HTTPResponse{StatusCode: 404}, nil
			}
		}
	}

	t.Run("index http failure degrades", func(t *testing.T) {
		provider := newLyricsProviderWithDeps(facade(404, 503), newFakeLyricsCache(), time.Now, nil, nil)
		_, health, err := fetchLunaBeatLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want a degraded operational failure", err, health)
		}
	})

	t.Run("index 429 activates cooldown", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(facade(404, 429), cache, time.Now, nil, nil)
		_, _, err := fetchLunaBeatLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatal("index 429 converted to a miss; want a degraded error")
		}
		if provider.activeCooldown(sourceGlobalCooldownKey("luna-beat")) <= 0 {
			t.Fatal("index 429 activated no cooldown")
		}
	})

	t.Run("valid index without a match stays a healthy miss", func(t *testing.T) {
		provider := newLyricsProviderWithDeps(facade(200, 200), newFakeLyricsCache(), time.Now, nil, nil)
		_, health, err := fetchLunaBeatLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err != nil || health != lookupHealthy {
			t.Fatalf("result = (%v, %v); want a quiet miss with an empty valid index", err, health)
		}
	})
}

func TestAMLLyricsEnvelopeStatusesClassified(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	facade := func(searchExecutor func() string) func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			switch {
			case strings.Contains(request.URL, "amll"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchExecutor())}, nil
			default:
				return &host.HTTPResponse{StatusCode: 404}, nil
			}
		}
	}

	t.Run("item envelope server status degrades", func(t *testing.T) {
		provider := newLyricsProvider(facade(func() string { return `{"status":500}` }))
		_, health, err := fetchAMLLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want degraded on envelope 500", err, health)
		}
	})

	t.Run("item envelope 429 activates cooldown", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(facade(func() string { return `{"status":429}` }), cache, time.Now, nil, nil)
		_, _, err := fetchAMLLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatal("envelope 429 converted to a miss; want a degraded error")
		}
		if provider.activeCooldown(sourceGlobalCooldownKey("amll")) <= 0 {
			t.Fatal("envelope 429 activated no cooldown")
		}
	})

	t.Run("lyrics envelope server status degrades", func(t *testing.T) {
		requests := 0
		provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			requests++
			switch {
			case strings.Contains(request.URL, "v1/lyrics/search"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":200,"data":{"items":[{"id":"1"}]}}`)}, nil
			case strings.Contains(request.URL, "v1/lyrics/get"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"status":500}`)}, nil
			default:
				t.Errorf("unexpected request %q", request.URL)
				return &host.HTTPResponse{StatusCode: 404}, nil
			}
		})
		_, _, err := fetchAMLLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatalf("lookup succeeded; want a degraded error when the lyrics envelope reports 500 (requests=%d)", requests)
		}
	})
}

func TestMusixmatchMintEnvelopeStatusesClassified(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	t.Run("mint envelope 429 keeps no cooldown creativity", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":429}}}`)}, nil
		}, cache, time.Now, nil, nil)
		_, _, err := fetchMusixmatchLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err == nil {
			t.Fatal("token.get envelope 429 converted to a miss; want an error")
		}
		if provider.activeCooldown(sourceGlobalCooldownKey("musixmatch")) <= 0 {
			t.Fatal("token.get envelope 429 activated no cooldown")
		}
	})

	t.Run("mint envelope challenge stays quiet", func(t *testing.T) {
		provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":401,"hint":"captcha"}}}`)}, nil
		})
		_, health, err := fetchMusixmatchLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err != nil || health != lookupUncertain {
			t.Fatalf("result = (%v, %v); want the challenge kept as a temporary skip", err, health)
		}
	})
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

func TestSweep_ContinuationsSeeCleanMissCoverage(t *testing.T) {
	t.Parallel()

	// The first pass records a clean miss and defers the tail source; the
	// continuation must recognize the earlier miss instead of re-enqueueing
	// full sweeps every dedupe cycle (Codex).
	cache := newFakeLyricsCache()
	one, two := new(int), new(int)
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				*one++
				time.Sleep(3 * time.Millisecond)
				return nil, lookupHealthy, nil
			}},
		{ID: "two", Display: "two", Hosts: []string{"two.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				*two++
				return nil, lookupHealthy, nil
			}},
	}, func(lyrics.TrackInfo) error { return nil })
	provider.sweepBudget = time.Millisecond

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	status, deferredIDs := provider.runSweepSources(track, nil)
	if len(deferredIDs) != 1 || deferredIDs[0] != "two" {
		t.Fatalf("status/deferred = %q/%v, want the tail source deferred", status, deferredIDs)
	}
	if *one != 1 || *two != 0 {
		t.Fatalf("fetch counts = %d/%d, want only the head source fetched", *one, *two)
	}

	provider.sweepBudget = 0
	provider2 := newLyricsProviderWithSourcesAt(nil, cache, time.Now, provider.sources, func(lyrics.TrackInfo) error { return nil })
	provider2.runSweepSources(track, []string{"two"})

	if _, exists, _ := cache.Get(provider.doneMarkerKey(track, provider.activeSources())); !exists {
		t.Fatal("the continuation must complete the full-order marker when earlier misses carried over")
	}
	if *one != 1 {
		t.Fatalf("head fetch count = %d, want the continuation to skip the cached clean miss", *one)
	}
}

func TestSweep_ContinuationFoldsCachedSiblingTTL(t *testing.T) {
	t.Parallel()

	// A plain sibling's per-source entry expires after an hour; the marker a
	// continuation writes over the full order must not outlive it (Codex).
	cache := newFakeLyricsCache()
	plainSource := lyricsSource{ID: "plain", Display: "plain", Hosts: []string{"plain.example.test"}, DefaultEnabled: true,
		Fetch: func(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
			time.Sleep(3 * time.Millisecond)
			return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "plain words"}, "plain", "plain")}, lookupHealthy, nil
		}}
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{
		plainSource,
		sweepSource("tail", new(int), false, nil),
	}, func(lyrics.TrackInfo) error { return nil })
	provider.sweepBudget = time.Millisecond

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	status, deferred := provider.runSweepSources(track, nil)
	if _, exists, _ := cache.Get(provider.doneMarkerKey(track, provider.activeSources())); exists {
		t.Fatalf("status/deferred = %q/%v; expected the tail to defer, not complete", status, deferred)
	}

	provider2 := newLyricsProviderWithSourcesAt(nil, cache, time.Now, provider.sources, func(lyrics.TrackInfo) error { return nil })
	provider2.sweepBudget = 0
	provider2.runSweepSources(track, []string{"tail"})

	ttl, ok := cache.ttlsForKey[provider2.doneMarkerKey(track, provider2.activeSources())]
	if !ok {
		t.Fatal("expected a done marker from the continuation")
	}
	if ttl != unsyncedLyricsCacheTTLSeconds {
		t.Fatalf("done marker TTL = %d, want the plain sibling's %d entry to bound the continuation marker", ttl, unsyncedLyricsCacheTTLSeconds)
	}
}

func TestGetLyrics_EnqueuesSweepOnStageOneError(t *testing.T) {
	t.Parallel()

	// Stage-one errors must not hide the tail: the join error is retained but
	// the incomplete walk still schedules the background sweep (Codex P1).
	enqueued := 0
	provider := newLyricsProviderWithSources(nil, newFakeLyricsCache(), time.Now, []lyricsSource{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				return nil, lookupDegraded, errors.New("head source down")
			}},
		sweepSource("tail", new(int), false, nil),
	}, func(lyrics.TrackInfo) error {
		enqueued++
		return nil
	})

	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}}); err == nil {
		t.Fatal("GetLyrics() error = nil, want the joined stage-one error retained")
	}
	if enqueued != 1 {
		t.Fatalf("enqueues = %d, want the sweep scheduled despite the stage-one error", enqueued)
	}
}

func TestStageOneMissesCoverTheSweepAndTheWalk(t *testing.T) {
	t.Parallel()

	// Healthy stage-one misses must persist as negative per-source entries so
	// the sweep skips them, and a later lookup must not refetch them either
	// (Codex P2).
	cache := newFakeLyricsCache()
	one, two, three, tail := new(int), new(int), new(int), new(int)
	sources := []lyricsSource{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				*one++
				return nil, lookupHealthy, nil
			}},
		{ID: "two", Display: "two", Hosts: []string{"two.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				*two++
				return nil, lookupHealthy, nil
			}},
		{ID: "three", Display: "three", Hosts: []string{"three.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				*three++
				return nil, lookupHealthy, nil
			}},
		{ID: "tail", Display: "tail", Hosts: []string{"tail.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				*tail++
				return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]tail line"}, "tail", "lrc")}, lookupHealthy, nil
			}},
	}
	provider := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	// Stage one walks the three misses and never reaches the tail.
	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}
	if *one != 1 || *two != 1 || *three != 1 || *tail != 0 {
		t.Fatalf("fetch counts = %d/%d/%d/%d, want only the head three fetched", *one, *two, *three, *tail)
	}

	// The sweep skips the persisted misses instead of refetching them.
	provider.runSweep(track)
	if *one != 1 || *two != 1 || *three != 1 {
		t.Fatalf("miss fetch counts after sweep = %d/%d/%d, want the sweep to reuse the miss entries", *one, *two, *three)
	}

	// A later lookup with no winner must also skip the miss entries.
	cache.mu.Lock()
	for key := range cache.values {
		if strings.HasPrefix(key, lyricsWinnerCacheKeyPrefix) {
			delete(cache.values, key)
		}
	}
	cache.mu.Unlock()

	reread := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	result, err := reread.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("re-read GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "tail", "lrc")
	if *one != 1 || *two != 1 || *three != 1 {
		t.Fatalf("miss fetch counts after winner reset = %d/%d/%d, want the misses to stay cached (Codex)", *one, *two, *three)
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

func TestParseRichsync_FractionalSecondOffsets(t *testing.T) {
	t.Parallel()

	// Musixmatch richsync `o` offsets are fractional seconds relative to the
	// chunk: decrypted as ms they shift words 1000x and fractional values
	// crashed the whole decode (Codex P1).
	lines, err := parseRichsync(`[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":"llo","o":0.12}]}]`)
	if err != nil {
		t.Fatalf("parseRichsync() error = %v, want nil with fractional offsets", err)
	}
	first := lines[0]
	if first.Words[1].StartMs != 12520 {
		t.Fatalf("word 2 start = %d, want 12520 (0.12s after the line start)", first.Words[1].StartMs)
	}

	lines, err = parseRichsync(`[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":1},{"c":"llo","o":1.2}]}]`)
	if err != nil {
		t.Fatalf("parseRichsync() error = %v, want nil with integral offsets", err)
	}
	if lines[0].Words[1].StartMs != 13600 {
		t.Fatalf("word 2 start = %d, want 13600 (1.2s after the line start)", lines[0].Words[1].StartMs)
	}
}

func TestCoverageCredChangeInvalidatesMisses(t *testing.T) {
	t.Parallel()

	// Negative coverage records unauthenticated misses; adding or replacing a
	// credential must re-authorize those sources (Codex).
	missCalls := 0
	sources := []lyricsSource{
		{ID: "liner", Display: "liner", Hosts: []string{"liner.example.test"}, Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			missCalls++
			return nil, lookupHealthy, nil
		}},
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	first := newLyricsProviderWithSourcesAt(nil, newFakeLyricsCache(), time.Now, sources, nil)
	first.runSweep(track)
	if missCalls != 1 {
		t.Fatalf("first sweep fetches = %d, want a single miss fetch", missCalls)
	}

	// The credential arrives: the miss entry must no longer suppress the
	// source, and the new-credentials done marker is a fresh key.
	reauthorized := newLyricsProviderWithSourcesAt(nil, first.cache, time.Now, sources, nil)
	reauthorized.configLookup = func(key string) (string, bool) {
		if key == "betterLyricsApiKey" {
			return "secret", true
		}
		return "", false
	}
	reauthorized.runSweep(track)
	if missCalls != 2 {
		t.Fatalf("fetches after the credential change = %d, want the stale miss entry to be bypassed", missCalls)
	}
}

func TestSweep_DefersDeadlineExhaustedSources(t *testing.T) {
	t.Parallel()

	// A search that finishes against the deadline leaves no budget for the
	// lyrics request; its error is budget exhaustion, not a failure — the
	// source belongs in the continuation (Greptile).
	provider := newLyricsProviderWithSourcesAt(nil, newFakeLyricsCache(), time.Now, []lyricsSource{
		{ID: "late", Display: "late", Hosts: []string{"late.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				return nil, lookupDegraded, fmt.Errorf("request lyrics: %w", errLyricsLookupBudgetExhausted)
			}},
		{ID: "tail", Display: "tail", Hosts: []string{"tail.example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "tail", "lrc")}, lookupHealthy, nil
			}},
	}, func(lyrics.TrackInfo) error { return nil })

	status, deferredIDs := provider.runSweepSources(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, nil)
	if len(deferredIDs) != 1 || deferredIDs[0] != "late" {
		t.Fatalf("status/deferred = %q/%v, want the deadline-exhausted source deferred", status, deferredIDs)
	}
	if !strings.Contains(status, "deferred=1") {
		t.Fatalf("status = %q, count the exhausted source as deferred", status)
	}
}

func TestMusixmatchTokenRejectionsStaySkippable(t *testing.T) {
	t.Parallel()

	// Expired-token rejections must not persist as clean misses: the next
	// lookup has to remint and retry, and the sweep must not mark the source
	// covered (Greptile).
	facade := func(t *testing.T, searchBody, macroBody string) func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			switch {
			case strings.Contains(request.URL, "/ws/1.1/token.get"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
			case strings.Contains(request.URL, "/ws/1.1/track.search"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
			default:
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(macroBody)}, nil
			}
		}
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}

	t.Run("rejected search token stays skippable", func(t *testing.T) {
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(facade(t, `{"message":{"header":{"status_code":401}}}`, `{}`), cache, time.Now, nil, nil)
		_, health, err := fetchMusixmatchLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err != nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want a temporary skip health", err, health)
		}
		if _, ok := provider.readCachedSourceCoverage(track, "musixmatch"); ok {
			t.Fatal("a rejected-token miss persisted into negative coverage")
		}
	})

	t.Run("rejected macro token stays skippable", func(t *testing.T) {
		search := `{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_length":10}}]}}}`
		cache := newFakeLyricsCache()
		provider := newLyricsProviderWithDeps(facade(t, search, `{"message":{"header":{"status_code":200},"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":401}}}}}}`), cache, time.Now, nil, nil)
		_, health, err := fetchMusixmatchLyricsCandidates(provider, track, time.Now().Add(stageOneLookupBudget))
		if err != nil || health == lookupHealthy {
			t.Fatalf("result = (%v, %v); want a temporary skip health", err, health)
		}
		if _, ok := provider.readCachedSourceCoverage(track, "musixmatch"); ok {
			t.Fatal("a rejected-macro miss persisted into negative coverage")
		}
	})
}

func TestLunaBeatMatchesArtistAliases(t *testing.T) {
	t.Parallel()

	// The hub index lists several aliases per artist (`赵雷`, `趙雷`, `Lei Zhao`);
	// joining them into one string never matches a track tagged with a single
	// alias (Greptile).
	index := []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"e","title":"阿刁","artists":["赵雷","趙雷","Lei Zhao"],"album":"剃刀","path":"lyrics/cjk.ttml"}]}`)
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: index}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now, "luna-beat")

	for _, alias := range []string{"Lei Zhao", "趙雷"} {
		if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "阿刁", Artist: alias, Album: "剃刀"}, time.Now().Add(stageOneLookupBudget)); err != nil {
			t.Fatalf("fetchLunaBeatLyricsCandidates(artist %q) error = %v, want nil", alias, err)
		}
	}
}

func TestMusixmatchToken429RecordsCooldown(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	provider := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 429, Headers: map[string]string{"Retry-After": "7"}}, nil
	}, cache, time.Now, nil, nil)

	_, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil {
		t.Fatal("token.get 429 converted to a miss; want an error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want the rate-limit error surfaced", err)
	}
	if cooldown := provider.activeCooldown(sourceGlobalCooldownKey("musixmatch")); cooldown < 7*time.Second || cooldown > 8*time.Second {
		t.Fatalf("cooldown = %v, want the Retry-After value honored", cooldown)
	}
}

func TestWinnerEntriesRespectCredentialChanges(t *testing.T) {
	t.Parallel()

	// A completed winner recorded without a credential must not stay served
	// once an administrator provides one (Greptile): the re-walk reaches the
	// newly authorized source instead of the cached miss.
	requests := 0
	provider := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 404}, nil
	}, newFakeLyricsCache(), time.Now, func(key string) (string, bool) { return "", false }, nil)

	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}}); err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}
	firstRound := requests

	authorized := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "lyrics-api.boidu.dev") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Word\"><p begin=\"1\" end=\"2\"><span begin=\"1\" end=\"2\">authorized line</span></p></tt>"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	}, provider.cache, time.Now, func(key string) (string, bool) {
		if key == "betterLyricsApiKey" {
			return "secret", true
		}
		return "", false
	}, nil)

	result, err := authorized.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("re-authorized GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "Better Lyrics", "ttml")
	if requests <= firstRound {
		t.Fatalf("requests = %d (first round %d), want the re-authorized lookup to hit the providers again", requests, firstRound)
	}
}
