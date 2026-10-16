package main

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

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

func TestGetLyrics_SweepEnqueueFailureReleasesSlot(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{
		sweepSource("one", new(int), false, errors.New("miss")),
	}, func(lyrics.TrackInfo) error {
		return errors.New("queue unavailable")
	})

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if provider.maybeEnqueueSweep(track) {
		t.Fatal("maybeEnqueueSweep() = true despite enqueue error, want false")
	}

	trackKey, _ := lyricsTrackKey(track)
	raw, exists, _ := cache.Get(sweepMarkerPrefix + trackKey)
	if !exists {
		t.Fatal("marker missing, want an early-release marker")
	}
	value, _ := strconv.Atoi(string(raw))
	_ = value
	if _, ok, _ := cache.Get(sweepMarkerPrefix + trackKey); !ok {
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
