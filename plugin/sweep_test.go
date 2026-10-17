package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
)

func sweepSource(id string, calls *int, withCandidate bool, err error) lyricsSource {
	return lyricsSource{
		ID:             id,
		Display:        id,
		Hosts:          []string{id + ".example.test"},
		DefaultEnabled: true,
		Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			*calls++
			if err != nil {
				return nil, lookupDegraded, err
			}
			if !withCandidate {
				return nil, lookupHealthy, nil
			}
			return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]sweep line"}, "stub", "lrc")}, lookupHealthy, nil
		},
	}
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%q does not contain %q", haystack, needle)
	}
}

func TestSweep_CachesEveryEnabledSourceResult(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	one, two := 0, 0
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("one", &one, true, nil),
		sweepSource("two", &two, true, nil),
	}, func(lyrics.TrackInfo) error { return nil })

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	status := provider.runSweep(track)
	if one != 1 || two != 1 {
		t.Fatalf("fetch calls = %d/%d, want 1 each", one, two)
	}
	for _, id := range []string{"one", "two"} {
		if candidate, ok := provider.readCachedSourceCandidate(track, id); !ok || !candidate.present() {
			t.Fatalf("source %q candidate missing after sweep: present=%v", id, ok)
		}
	}
	assertContains(t, status, "cached=2")
}

func TestSweep_SkipsFreshEntriesAndCooledDownSources(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	calls := 0
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("fresh", &calls, true, nil),
		sweepSource("cool", &calls, true, nil),
	}, func(lyrics.TrackInfo) error { return nil })

	provider.cacheSourceCandidate(track, "fresh", newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]cached"}, "stub", "lrc"))
	deadline := time.Now().Add(10 * time.Minute)
	if err := cache.SetWithTTL(sourceGlobalCooldownKey("cool"), []byte(strconv.FormatInt(deadline.Unix(), 10)), 600); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	status := provider.runSweep(track)
	if calls != 0 {
		t.Fatalf("fetch calls = %d, want 0 (fresh entry and active cooldown)", calls)
	}
	assertContains(t, status, "skipped=2")
}

func TestSweep_ContinuesAfterSourceFailure(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	one, two := 0, 0
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("broken", &one, false, errors.New("down")),
		sweepSource("healthy", &two, true, nil),
	}, func(lyrics.TrackInfo) error { return nil })

	status := provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	if one != 1 || two != 1 {
		t.Fatalf("fetch calls = %d/%d, want sweep to continue past the failure", one, two)
	}
	if candidate, ok := provider.readCachedSourceCandidate(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, "healthy"); !ok || !candidate.present() {
		t.Fatal("healthy source result missing after sweep")
	}
	assertContains(t, status, "failed=1")
}

func TestOnInit_FailsOpenWhenQueueCreationIsUnavailable(t *testing.T) {
	t.Parallel()

	provider := newLyricsProviderWithSources(nil, newFakeLyricsCache(), time.Now, nil, func(lyrics.TrackInfo) error { return nil })
	if err := provider.OnInit(); err != nil {
		t.Fatalf("OnInit() error = %v, want nil (fail open)", err)
	}
}

func TestOnTaskExecute_IgnoresForeignQueuesAndRunsSweeps(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	calls := 0
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("one", &calls, true, nil),
	}, func(lyrics.TrackInfo) error { return nil })

	foreign, err := provider.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: "other-queue", TaskID: "t1"})
	if err != nil {
		t.Fatalf("OnTaskExecute(other) error = %v, want nil", err)
	}
	assertContains(t, foreign, "ignored")

	payload, err := json.Marshal(sweepTaskPayload{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	status, err := provider.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: "lyrics-sweep", TaskID: "t2", Payload: payload})
	if err != nil {
		t.Fatalf("OnTaskExecute(sweep) error = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
	assertContains(t, status, "cached=1")
}

func TestGetLyrics_EnqueuesSweepOncePerTrack(t *testing.T) {
	t.Parallel()

	// Four implemented sources: stage one walks three, so the tail is
	// uncovered and the sweep must fire exactly once per track regardless of
	// how often the client replays the (negative) winner.
	cache := newFakeLyricsCache()
	enqueued := 0
	one, two, three, four := 0, 0, 0, 0
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("one", &one, false, nil),
		sweepSource("two", &two, false, nil),
		sweepSource("three", &three, false, nil),
		sweepSource("four", &four, false, nil),
	}, func(lyrics.TrackInfo) error {
		enqueued++
		return nil
	})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	for range 3 {
		if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
			t.Fatalf("GetLyrics() error = %v, want nil", err)
		}
	}
	if enqueued != 1 {
		t.Fatalf("sweep enqueued %d times, want once per track", enqueued)
	}
}

func TestSweep_RetainsInMemoryHostCooldowns(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	cache.setError = errors.New("kv down")
	shared := lyricsSource{
		ID: "sibling", Display: "sibling", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true,
		Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			t.Fatal("sibling fetched although the host just served a global rate limit")
			return nil, lookupHealthy, nil
		},
	}
	first := lyricsSource{
		ID: "first", Display: "first", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true,
		Fetch: func(p *lyricsProvider, _ lyrics.TrackInfo, _ time.Time) ([]lyricsCandidate, lookupHealth, error) {
			response := &host.HTTPResponse{
				StatusCode: 429,
				Headers:    map[string]string{"Retry-After": "60", "X-RateLimit-Type": "exceeded"},
				Body:       []byte(`{"error":"exhausted"}`),
			}
			err := rateLimitResponseError("test", response, 60*time.Second)
			return nil, lookupDegraded, err
		},
	}
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{first, shared}, func(lyrics.TrackInfo) error { return nil })

	status := provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	assertContains(t, status, "failed=1")
	assertContains(t, status, "skipped=1")
	// The pass was not covered, so the done marker must not short-circuit it: the
	// next lookup gets to re-sweep once the sibling-host cooldown passes.
	if _, exists, _ := cache.Get(provider.doneMarkerKey(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, []lyricsSource{first, shared})); exists {
		t.Fatal("an incomplete sweep wrote a done marker")
	}
}

func TestStageOneSkipsSourceInCooldown(t *testing.T) {
	t.Parallel()

	callsCooled, callsHealthy := 0, 0
	cooled := lyricsSource{
		ID: "cooled", Display: "cooled", Hosts: []string{"cooled.example.test"}, DefaultEnabled: true,
		Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			callsCooled++
			return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "<tt>cooled</tt>"}, "cooled", "ttml")}, lookupHealthy, nil
		},
	}
	healthy := lyricsSource{
		ID: "healthy", Display: "healthy", Hosts: []string{"healthy.example.test"}, DefaultEnabled: true,
		Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			callsHealthy++
			return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "<tt>healthy</tt>"}, "healthy", "ttml")}, lookupHealthy, nil
		},
	}
	cache := newFakeLyricsCache()
	now := time.Unix(1000, 0)
	provider := newLyricsProviderWithSourcesAt(nil, cache, func() time.Time { return now }, []lyricsSource{cooled, healthy}, func(lyrics.TrackInfo) error { return nil })

	if err := cache.SetWithTTL(sourceGlobalCooldownKey("cooled"), []byte(strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)), 600); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "healthy", "ttml")
	if callsCooled != 0 {
		t.Fatalf("cooled source fetches = %d, want 0 (stage one honors cooldowns)", callsCooled)
	}
	if callsHealthy != 1 {
		t.Fatalf("healthy source fetches = %d, want 1", callsHealthy)
	}
}

func TestSweep_SkipsBetterLyricsFamilyOnSharedCooldown(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	calls := 0
	now := time.Unix(2000, 0)
	provider := newLyricsProviderWithSourcesAt(nil, cache, func() time.Time { return now }, []lyricsSource{
		{
			ID: "better-lyrics-portato", Display: "Better Lyrics Portato",
			Hosts:          []string{"lyrics-api.boidu.dev"},
			DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				calls++
				return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]portato line"}, "Better Lyrics Portato", "elrc")}, lookupHealthy, nil
			},
		},
	}, func(lyrics.TrackInfo) error { return nil })

	// The better-lyrics family records its shared cooldown under the v3 key.
	if err := cache.SetWithTTL(betterLyricsGlobalCooldownKey, []byte(strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)), 600); err != nil {
		t.Fatalf("seed family cooldown: %v", err)
	}

	status := provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	if calls != 0 {
		t.Fatalf("family source fetches = %d, want 0 (sweep honors the shared cooldown)", calls)
	}
	assertContains(t, status, "skipped=1")
}
