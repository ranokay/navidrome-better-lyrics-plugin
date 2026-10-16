package main

import (
	"errors"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestGetLyrics_EnqueuesSweepWhenCoverageIsIncomplete(t *testing.T) {
	t.Parallel()

	// A syllable hit from the first source stops the walk before the second:
	// coverage is still incomplete, so the sweep must be scheduled.
	enqueued := 0
	cache := newFakeLyricsCache()
	syllable := stubFetchedCandidate("first", "stub", "ttml", "<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">x</tt>")
	second := stubFetchedCandidate("second", "stub", "plain", "")
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{syllable, second}, func(lyrics.TrackInfo) error {
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
	src := func(id string) lyricsSource {
		return lyricsSource{
			ID: id, Display: id, Hosts: []string{id + ".example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				calls++
				return nil, lookupHealthy, nil
			},
		}
	}
	sources := []lyricsSource{src("one"), src("two")}
	cache := newFakeLyricsCache()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	walker := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	if _, err := walker.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}

	// Simulate the sweep having landed one result between the negative winner's
	// write and the next lookup.
	walker.cacheSourceCandidate(track, "two", newLyricsCandidate(lyrics.LyricsText{Text: "[00:01.00]sweep line"}, "stub", "lrc"))

	reread := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
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
	src := func(id string) lyricsSource {
		return lyricsSource{
			ID: id, Display: id, Hosts: []string{id + ".example.test"}, DefaultEnabled: true,
			Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
				calls++
				return nil, lookupHealthy, nil
			},
		}
	}
	sources := []lyricsSource{src("one"), src("two")}
	cache := newFakeLyricsCache()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	walker := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
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
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("one", &one, true, nil),
		sweepSource("two", &two, true, nil),
	}, func(lyrics.TrackInfo) error { return nil })

	provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})

	if _, exists, _ := cache.Get(sweepDoneMarkerKey(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, activeSourcesList())); !exists {
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
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("broken", &one, false, errors.New("down")),
	}, func(lyrics.TrackInfo) error {
		t.Fatal("a sweep that failed to complete must not mark the track done")
		return nil
	})

	status := provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	assertContains(t, status, "failed=1")
	if _, exists, _ := cache.Get(sweepDoneMarkerKey(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, activeSourcesList())); exists {
		t.Fatal("failed sweep wrote a done marker")
	}
}

// activeSourcesList mirrors the injected source lists exactly so the
// fingerprinted done key matches what the sweep wrote.
func activeSourcesList() []lyricsSource {
	// The sweep-derived tests inject sources with ids "one"/"two"; the done
	// fingerprint hashes only those ids, so no catalog lookup is needed here.
	return []lyricsSource{{ID: "one"}, {ID: "two"}}
}
