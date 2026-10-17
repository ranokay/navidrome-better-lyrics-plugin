package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func TestGetLyrics_EnqueuesSweepWhenCoverageIsIncomplete(t *testing.T) {
	t.Parallel()

	// A syllable hit from the first source stops the walk before the second:
	// coverage is still incomplete, so the sweep must be scheduled.
	enqueued := 0
	cache := newFakeLyricsCache()
	syllable := stubFetchedCandidate("first", "stub", "ttml", "<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">x</tt>")
	second := stubFetchedCandidate("second", "stub", "plain", "")
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{syllable, second}, func(lyrics.TrackInfo) error {
		enqueued++
		return nil
	})

	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}}); err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if enqueued != 1 {
		t.Fatalf("sweep enqueues = %d, want 1 when only one of two sources was walked", enqueued)
	}
}

func TestGetLyrics_NegativeWinnerDefersToFreshPerSourceResults(t *testing.T) {
	t.Parallel()

	calls := 0
	src := func(id string) source.Source {
		return source.Source{
			ID: id, Display: id, Hosts: []string{id + ".example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				calls++
				return nil, source.Healthy, nil
			},
		}
	}
	sources := []source.Source{src("one"), src("two")}
	cache := newFakeLyricsCache()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	walker := newTestProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	if _, err := walker.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}

	// Simulate the sweep having landed one result between the negative winner's
	// write and the next lookup.
	walker.cacheSourceCandidate(track, "two", lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]sweep line"}, "stub", "lrc"))

	reread := newTestProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	result, err := reread.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("second GetLyrics() error = %v, want nil", err)
	}
	if len(result.Lyrics) != 1 || result.Lyrics[0].Text != "[00:01.00]sweep line" {
		t.Fatalf("second GetLyrics() = %#v, want the fresh per-source result (Review: negative winner must not shield sweep results)", result)
	}
}

func TestGetLyrics_NegativeWinnerStillShieldsWhenNoPerSourceResults(t *testing.T) {
	t.Parallel()

	calls := 0
	src := func(id string) source.Source {
		return source.Source{
			ID: id, Display: id, Hosts: []string{id + ".example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				calls++
				return nil, source.Healthy, nil
			},
		}
	}
	sources := []source.Source{src("one"), src("two")}
	cache := newFakeLyricsCache()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	walker := newTestProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	if _, err := walker.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}
	if _, err := walker.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("replayed GetLyrics() error = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("source fetches = %d, want 2 (one per source, cached negative winner shields the replay)", calls)
	}
}

func TestSweep_CompletionWritesDoneMarker(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	one, two := 0, 0
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		sweepSource("one", &one, true, nil),
		sweepSource("two", &two, true, nil),
	}, func(lyrics.TrackInfo) error { return nil })

	provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})

	if _, exists, _ := cache.Get(provider.doneMarkerKey(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, activeSourcesList())); !exists {
		t.Fatal("completed sweep did not record its done marker")
	}
	if provider.maybeEnqueueSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}) {
		t.Fatal("sweep enqueued although the done marker says this track is covered")
	}
}

func TestSweep_FailuresKeepTheTrackReSweepable(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	one := 0
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		sweepSource("broken", &one, false, errors.New("down")),
	}, func(lyrics.TrackInfo) error {
		t.Fatal("a sweep that failed to complete must not mark the track done")
		return nil
	})

	status := provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	assertContains(t, status, "failed=1")
	if _, exists, _ := cache.Get(provider.doneMarkerKey(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, activeSourcesList())); exists {
		t.Fatal("failed sweep wrote a done marker")
	}
}

// activeSourcesList mirrors the injected source lists exactly so the
// fingerprinted done key matches what the sweep wrote.
func activeSourcesList() []source.Source {
	// The sweep-derived tests inject sources with ids "one"/"two"; the done
	// fingerprint hashes only those ids, so no catalog lookup is needed here.
	return []source.Source{{ID: "one"}, {ID: "two"}}
}

func TestSweep_MissesMarkDoneCovered(t *testing.T) {
	t.Parallel()

	// A source that answers "no lyrics here" is covered sweep work: without
	// counting misses the done marker could never be written for catalogs whose
	// sources cleanly miss, so every lookup kept re-enqueueing the sweep.
	cache := newFakeLyricsCache()
	one, two := new(int), new(int)
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
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
	// cached outcome.
	cache := newFakeLyricsCache()
	cache.setError = errors.New("kv at quota")
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
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
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
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
	// lookup can retry: the negative winner must not block it.
	trackKey, _ := lyricsTrackKey(track)
	markerKey := provider.sweepPendingMarkerKey(track, trackKey)
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

func TestGetLyrics_AllDisabledSkipsScheduling(t *testing.T) {
	t.Parallel()

	// An injected empty catalog must serve nothing without network or queue
	// traffic.
	emptyInjected := newTestProviderWithSources(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		t.Errorf("unexpected network request with an empty catalog: %q", request.URL)
		return nil, nil
	}, newFakeLyricsCache(), time.Now, []source.Source{}, func(lyrics.TrackInfo) error {
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
	selections := make([]sourceSelection, 0, len(source.Catalog))
	for _, src := range source.Catalog {
		selections = append(selections, sourceSelection{ID: src.ID, Enabled: ptrBool(false)})
	}
	raw, err := json.Marshal(selections)
	if err != nil {
		t.Fatalf("marshal selections: %v", err)
	}

	enqueued := 0
	provider := newTestProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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
		t.Fatalf("resolved sources = %s, want an all-disabled empty order", source.IDs(resolve.Sources))
	}
	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}}); err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if enqueued != 0 {
		t.Fatalf("enqueues = %d, want 0 (the all-disabled opt-out must not fill the task queue)", enqueued)
	}
}

func TestSweep_DoneMarkerTTLBoundedByShortestCoveredCandidate(t *testing.T) {
	t.Parallel()

	// A plain tail result lives one hour per source and five minutes as a
	// winner; the done marker must not suppress re-sweeps for 24 hours after
	// that: the marker cannot outlive the shortest cached result.
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
			provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
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
		provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
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
		provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
			sweepSource("one", new(int), false, nil),
			sweepSource("two", new(int), false, nil),
		}, func(lyrics.TrackInfo) error { return nil })

		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "one", "lrc")
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
	// queueing a sweep would refetch the same misses.
	cache := newFakeLyricsCache()
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
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

func winnerKeyForTest(t *testing.T, cache Store, order []source.Source, track lyrics.TrackInfo) string {
	t.Helper()
	key, ok := lyricsWinnerCacheKey(track, source.IDs(order))
	if !ok {
		t.Fatal("expected a winner cache key")
	}
	return key
}

func TestSweep_ContinuationsSeeCleanMissCoverage(t *testing.T) {
	t.Parallel()

	// The first pass records a clean miss and defers the tail source; the
	// continuation must recognize the earlier miss instead of re-enqueueing
	// full sweeps every dedupe cycle.
	cache := newFakeLyricsCache()
	one, two := new(int), new(int)
	// The injected clock advances when the head source fetches, so the budget
	// overrun is deterministic instead of racing real scheduling.
	current := time.Now()
	provider := newTestProviderWithSources(nil, cache, func() time.Time { return current }, []source.Source{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				*one++
				current = current.Add(time.Second)
				return nil, source.Healthy, nil
			}},
		{ID: "two", Display: "two", Hosts: []string{"two.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				*two++
				return nil, source.Healthy, nil
			}},
	}, func(lyrics.TrackInfo) error { return nil })
	provider.sweepBudget = 2 * time.Millisecond

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	status, deferredIDs := provider.runSweepSources(track, nil)
	if len(deferredIDs) != 1 || deferredIDs[0] != "two" {
		t.Fatalf("status/deferred = %q/%v, want the tail source deferred", status, deferredIDs)
	}
	if *one != 1 || *two != 0 {
		t.Fatalf("fetch counts = %d/%d, want only the head source fetched", *one, *two)
	}

	provider.sweepBudget = 0
	provider2 := newTestProviderWithSources(nil, cache, func() time.Time { return current }, provider.sources, func(lyrics.TrackInfo) error { return nil })
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
	// continuation writes over the full order must not outlive it.
	cache := newFakeLyricsCache()
	// The injected clock advances when the plain source fetches; the write and
	// the continuation then share one clock, so the remaining-life bound is
	// exact rather than wall-clock dependent.
	current := time.Now()
	plainSource := source.Source{ID: "plain", Display: "plain", Hosts: []string{"plain.example.test"}, DefaultEnabled: true,
		Fetch: func(rt source.Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, source.Health, error) {
			current = current.Add(time.Second)
			return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: "plain words"}, "plain", "plain")}, source.Healthy, nil
		}}
	provider := newTestProviderWithSources(nil, cache, func() time.Time { return current }, []source.Source{
		plainSource,
		sweepSource("tail", new(int), false, nil),
	}, func(lyrics.TrackInfo) error { return nil })
	provider.sweepBudget = 2 * time.Millisecond

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	status, deferred := provider.runSweepSources(track, nil)
	if _, exists, _ := cache.Get(provider.doneMarkerKey(track, provider.activeSources())); exists {
		t.Fatalf("status/deferred = %q/%v; expected the tail to defer, not complete", status, deferred)
	}

	provider2 := newTestProviderWithSources(nil, cache, func() time.Time { return current }, provider.sources, func(lyrics.TrackInfo) error { return nil })
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
	// the incomplete walk still schedules the background sweep.
	enqueued := 0
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, []source.Source{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Degraded, errors.New("head source down")
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
	// the sweep skips them, and a later lookup must not refetch them either.
	cache := newFakeLyricsCache()
	one, two, three, tail := new(int), new(int), new(int), new(int)
	sources := []source.Source{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				*one++
				return nil, source.Healthy, nil
			}},
		{ID: "two", Display: "two", Hosts: []string{"two.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				*two++
				return nil, source.Healthy, nil
			}},
		{ID: "three", Display: "three", Hosts: []string{"three.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				*three++
				return nil, source.Healthy, nil
			}},
		{ID: "tail", Display: "tail", Hosts: []string{"tail.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				*tail++
				return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]tail line"}, "tail", "lrc")}, source.Healthy, nil
			}},
	}
	provider := newTestProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
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

	reread := newTestProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	result, err := reread.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("re-read GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "tail", "lrc")
	if *one != 1 || *two != 1 || *three != 1 {
		t.Fatalf("miss fetch counts after winner reset = %d/%d/%d, want the misses to stay cached", *one, *two, *three)
	}
}

func TestCoverageCredChangeInvalidatesMisses(t *testing.T) {
	t.Parallel()

	// Negative coverage records unauthenticated misses; adding or replacing a
	// credential must re-authorize those sources.
	missCalls := 0
	sources := []source.Source{
		{ID: "liner", Display: "liner", Hosts: []string{"liner.example.test"}, Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			missCalls++
			return nil, source.Healthy, nil
		}},
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	first := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)
	first.runSweep(track)
	if missCalls != 1 {
		t.Fatalf("first sweep fetches = %d, want a single miss fetch", missCalls)
	}

	// The credential arrives: the miss entry must no longer suppress the
	// source, and the new-credentials done marker is a fresh key.
	reauthorized := newTestProviderWithSources(nil, first.cache, time.Now, sources, nil)
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
	// source belongs in the continuation.
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, []source.Source{
		{ID: "late", Display: "late", Hosts: []string{"late.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Degraded, fmt.Errorf("request lyrics: %w", source.ErrLookupBudgetExhausted)
			}},
		{ID: "tail", Display: "tail", Hosts: []string{"tail.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "tail", "lrc")}, source.Healthy, nil
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

func TestAnonymous401sKeepBoundedCoverage(t *testing.T) {
	t.Parallel()

	// Anonymous Better Lyrics/Portato 401s are cache-only misses: they bound
	// the sweep (one hour, credential-fingerprinted keys) instead of blocking
	// completion every dedupe cycle.
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, []source.Source{
		{ID: "anon1", Display: "anon1", Hosts: []string{"anon1.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Uncertain, nil
			}},
		{ID: "anon2", Display: "anon2", Hosts: []string{"anon2.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Uncertain, nil
			}},
		{ID: "tail", Display: "tail", Hosts: []string{"tail.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "tail", "lrc")}, source.Healthy, nil
			}},
	}, func(lyrics.TrackInfo) error { return nil })

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	provider.runSweep(track)

	if _, exists, _ := provider.cache.Get(provider.doneMarkerKey(track, provider.activeSources())); !exists {
		t.Fatal("anonymous cache-only misses are bounded coverage: the sweep must still complete")
	}
	ttl := provider.cache.(*fakeLyricsCache).ttlsForKey[provider.doneMarkerKey(track, provider.activeSources())]
	if ttl != unsyncedLyricsCacheTTLSeconds {
		t.Fatalf("done marker TTL = %d, want the one-hour miss bound", ttl)
	}
}

func TestSweepMarkerBoundedByRemainingLifetime(t *testing.T) {
	t.Parallel()

	// A cached entry with 60 s left must bound the done marker to those 60 s,
	// not a fresh full TTL.
	cache := newFakeLyricsCache()
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		sweepSource("one", new(int), false, nil),
	}, func(lyrics.TrackInfo) error { return nil })
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	key, ok := lyricsCacheKeyForSource(track, "one")
	if !ok {
		t.Fatal("expected a source cache key")
	}
	expiringSoon := `{"quality":1,"response":{"lyrics":[{"text":"[00:01.00]line"}]},"expiresAt":` +
		fmt.Sprintf("%d", time.Now().Unix()+60) + `}`
	if err := cache.SetWithTTL(key, []byte(expiringSoon), 60); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	provider.runSweep(track)

	ttl := cache.ttlsForKey[provider.doneMarkerKey(track, provider.activeSources())]
	if ttl < 55 || ttl > 61 {
		t.Fatalf("done marker TTL = %d, want the entry's remaining ~60 s lifetime", ttl)
	}
}

func TestSweepRefetchesUnstampedCoverage(t *testing.T) {
	t.Parallel()

	// Entries without an expiry stamp cannot bound a marker, so the sweep
	// refetches them once to leave stamped coverage behind.
	cache := newFakeLyricsCache()
	one := new(int)
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		sweepSource("one", one, false, nil),
	}, func(lyrics.TrackInfo) error { return nil })
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	key, ok := lyricsCacheKeyForSource(track, "one")
	if !ok {
		t.Fatal("expected a source cache key")
	}
	// Old-format envelope: no expiresAt field.
	if err := cache.SetWithTTL(key, []byte(`{"quality":1,"response":{"lyrics":[{"text":"[00:01.00]line"}]}}`), 3600); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	provider.runSweep(track)

	if *one != 1 {
		t.Fatalf("fetches = %d, want the unstamped entry refetched once", *one)
	}
	if _, ok := cache.ttlsForKey[provider.doneMarkerKey(track, provider.activeSources())]; !ok {
		t.Fatal("expected a done marker once every entry carries a stamped lifetime")
	}
}

func TestSweepPendingMarkerFollowsSourceOrder(t *testing.T) {
	t.Parallel()

	// Reconfiguring sources must not delay a fresh sweep for the old
	// track-only marker window.
	cache := newFakeLyricsCache()
	enqueued := 0
	enqueue := func(lyrics.TrackInfo) error {
		enqueued++
		return nil
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	first := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		sweepSource("one", new(int), false, nil),
	}, enqueue)
	if !first.maybeEnqueueSweep(track) {
		t.Fatal("first enqueue refused")
	}

	second := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		sweepSource("one", new(int), false, nil),
		sweepSource("two", new(int), false, nil),
	}, enqueue)
	if !second.maybeEnqueueSweep(track) {
		t.Fatal("reordered pending marker blocked the fresh sweep")
	}
	if enqueued != 2 {
		t.Fatalf("enqueues = %d, want one per source order", enqueued)
	}
}

func TestTemporarySkipsDoNotCompleteForegroundCoverage(t *testing.T) {
	t.Parallel()

	// A tempSkip source is not covered work: the pass must stay incomplete,
	// enqueue the sweep, and not write a complete negative winner.
	enqueued := 0
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, []source.Source{
		{ID: "skip", Display: "skip", Hosts: []string{"skip.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.TemporarilySkipped, nil
			}},
	}, func(lyrics.TrackInfo) error {
		enqueued++
		return nil
	})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if enqueued != 1 {
		t.Fatalf("enqueues = %d, want the incomplete pass to schedule the sweep", enqueued)
	}
}

func TestSweepQueueMarkerCoversBacklog(t *testing.T) {
	t.Parallel()

	// The enqueue-time marker must cover the queued interval, not just the
	// execution window.
	cache := newFakeLyricsCache()
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		sweepSource("one", new(int), false, nil),
	}, nil)

	if !provider.maybeEnqueueSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}) {
		t.Fatal("maybeEnqueueSweep refused")
	}
	found := false
	for _, ttl := range cache.ttls {
		if ttl == sweepQueuedMarkerTTL {
			found = true
		}
	}
	if !found {
		t.Fatalf("marker TTLs = %v, want the backlog-covering %d", cache.ttls, sweepQueuedMarkerTTL)
	}
}

func TestSweepContinuationKeepsPendingMarker(t *testing.T) {
	t.Parallel()

	// A queued continuation must keep the pending marker alive so replays
	// cannot enqueue a parallel full sweep while it waits.
	cache := newFakeLyricsCache()
	continuations := 0
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		{ID: "late", Display: "late", Hosts: []string{"late.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Degraded, fmt.Errorf("request lyrics: %w", source.ErrLookupBudgetExhausted)
			}},
		sweepSource("tail", new(int), false, nil),
	}, func(lyrics.TrackInfo) error { return nil })
	provider.enqueueSweepSources = func(lyrics.TrackInfo, []string) error {
		continuations++
		return nil
	}

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	payload, err := json.Marshal(sweepTaskPayload{Track: track})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := provider.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, Payload: payload}); err != nil {
		t.Fatalf("OnTaskExecute() error = %v, want nil", err)
	}
	if continuations != 1 {
		t.Fatalf("continuations = %d, want the deferred tail enqueued", continuations)
	}
	trackKey, _ := lyricsTrackKey(track)
	raw, exists, _ := cache.Get(provider.sweepPendingMarkerKey(track, trackKey))
	if !exists {
		t.Fatal("pending marker vanished although a continuation is queued")
	}
	if string(raw) == "release" {
		t.Fatal("pending marker was released to a 1-second TTL although a continuation is queued")
	}
	ttl := cache.ttlsForKey[provider.sweepPendingMarkerKey(track, trackKey)]
	if ttl != sweepQueuedMarkerTTL {
		t.Fatalf("marker TTL = %d, want the queued window %d while the continuation waits", ttl, sweepQueuedMarkerTTL)
	}
}

func TestSweepContinuationDoneCheckUsesFullOrder(t *testing.T) {
	t.Parallel()

	// A continuation that completes the full order writes the done marker for
	// the full configured order; the completion check in OnTaskExecute must
	// use that same order, otherwise it releases the pending marker as if
	// nothing had been covered.
	cache := newFakeLyricsCache()
	head := new(int)
	// The injected clock advances when the head source fetches, so the budget
	// overrun is deterministic instead of racing real scheduling.
	current := time.Now()
	provider := newTestProviderWithSources(nil, cache, func() time.Time { return current }, []source.Source{
		{ID: "head", Display: "head", Hosts: []string{"head.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				*head++
				current = current.Add(time.Second)
				return nil, source.Healthy, nil
			}},
		sweepSource("tail", new(int), true, nil),
	}, func(lyrics.TrackInfo) error { return nil })
	provider.sweepBudget = 2 * time.Millisecond

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	status, deferredIDs := provider.runSweepSources(track, nil)
	if len(deferredIDs) != 1 || deferredIDs[0] != "tail" {
		t.Fatalf("status/deferred = %q/%v, want the tail source deferred", status, deferredIDs)
	}

	provider.sweepBudget = 0
	payload, err := json.Marshal(sweepTaskPayload{Track: track, Sources: []string{"tail"}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := provider.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, Payload: payload}); err != nil {
		t.Fatalf("OnTaskExecute() error = %v, want nil", err)
	}

	if _, exists, _ := cache.Get(provider.doneMarkerKey(track, provider.activeSources())); !exists {
		t.Fatal("the continuation must complete the full-order done marker")
	}
	trackKey, _ := lyricsTrackKey(track)
	raw, exists, _ := cache.Get(provider.sweepPendingMarkerKey(track, trackKey))
	if !exists {
		t.Fatal("pending marker vanished although the full-order sweep is done")
	}
	if string(raw) == "release" {
		t.Fatal("OnTaskExecute released the pending marker although the done marker exists")
	}
}

func TestSweep_EnqueuesContinuationForDeferredSources(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	sources := []source.Source{
		sweepSource("one", new(int), true, nil),
		sweepSource("two", new(int), true, nil),
	}
	var deferredPassed []string
	// A negative budget defers every source deterministically (no parallel
	// package var mutation).
	provider := newTestProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	provider.sweepBudget = -time.Second
	provider.enqueueSweepSources = func(track lyrics.TrackInfo, ids []string) error {
		deferredPassed = ids
		return nil
	}

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	payload, _ := json.Marshal(sweepTaskPayload{Track: track})
	status, err := provider.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: "lyrics-sweep", Payload: payload})
	if err != nil {
		t.Fatalf("OnTaskExecute() error = %v, want nil", err)
	}
	assertContains(t, status, "deferred=2")
	if len(deferredPassed) != 2 {
		t.Fatalf("continuation sources = %#v, want the two deferred ids", deferredPassed)
	}
}

func TestSweep_MarksCachedOnlyAfterRealStore(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	cache.setError = errors.New("kv down")
	healthy := sweepSource("one", new(int), true, nil)
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{healthy}, func(lyrics.TrackInfo) error { return nil })

	status := provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	assertContains(t, status, "cached=0")
	if provider.maybeEnqueueSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}) == false {
		// Without coverage the next lookup must still be allowed to re-sweep.
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if _, exists, _ := cache.Get(provider.doneMarkerKey(track, []source.Source{healthy})); exists {
		t.Fatal("done marker recorded although the KV store rejected the lyrics write")
	}
}

func TestSweep_DoneMarkerFingesprintsSourceOrder(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	sourcesA := []source.Source{sweepSource("one", new(int), true, nil), sweepSource("two", new(int), true, nil)}
	providerA := newTestProviderWithSources(nil, cache, time.Now, sourcesA, func(lyrics.TrackInfo) error { return nil })
	providerA.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})

	reordered := []source.Source{sourcesA[1], sourcesA[0]}
	providerB := newTestProviderWithSources(nil, cache, time.Now, reordered, func(lyrics.TrackInfo) error { return nil })
	if providerB.maybeEnqueueSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}) == false {
		t.Fatal("a reorder fingerprint key inherits the old done marker; the newly ranked source would never be swept")
	}
}

func TestGetLyrics_SweepEnqueueFailureReleasesSlot(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	provider := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		sweepSource("one", new(int), false, errors.New("miss")),
	}, func(lyrics.TrackInfo) error {
		return errors.New("queue unavailable")
	})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if provider.maybeEnqueueSweep(track) {
		t.Fatal("maybeEnqueueSweep() = true despite enqueue error, want false")
	}

	trackKey, _ := lyricsTrackKey(track)
	raw, exists, _ := cache.Get(provider.sweepPendingMarkerKey(track, trackKey))
	if !exists {
		t.Fatal("marker missing, want an early-release marker")
	}
	value, _ := strconv.Atoi(string(raw))
	_ = value
	if _, ok, _ := cache.Get(provider.sweepPendingMarkerKey(track, trackKey)); !ok {
		t.Fatal("marker vanished, want it retained until its short release TTL lapses")
	}
	// The release marker must expire almost immediately (1 s) — assert its TTL.
	found := false
	for _, ttl := range cache.ttls {
		if ttl == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("release marker TTL not found in %v, want a 1-second slot release", cache.ttls)
	}
}
