package main

import (
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func stubFetchedCandidate(id, provider, format, text string) lyricsSource {
	return lyricsSource{
		ID:             id,
		Display:        id,
		Hosts:          []string{id + ".example.test"},
		DefaultEnabled: true,
		Fetch:          fetchFixedCandidate(provider, format, text),
	}
}

func fetchFixedCandidate(provider, format, text string) func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
	return func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
		return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: text}, provider, format)}, lookupHealthy, nil
	}
}

func TestLyricsProvider_WinnerTTLFollowsTimingQuality(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format string
		text   string
		want   int64
	}{
		{name: "syllable winners live a day", format: "ttml", text: "<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">x</tt>", want: syncedLyricsCacheTTLSeconds},
		{name: "word winners live an hour", format: "elrc", text: "[00:01.00]<00:01.10>word", want: unsyncedLyricsCacheTTLSeconds},
		{name: "line winners live an hour", format: "lrc", text: "[00:01.00]line", want: unsyncedLyricsCacheTTLSeconds},
		{name: "unsynced winners live five minutes", format: "plain", text: "plain text", want: negativeLyricsCacheTTLSeconds},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cache := newFakeLyricsCache()
			sources := []lyricsSource{stubFetchedCandidate("one", "stub", test.format, test.text)}
			provider := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })

			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
				t.Fatalf("GetLyrics() error = %v, want nil", err)
			}
			if ttl := cache.winnerTTL(track, sources); ttl != test.want {
				t.Fatalf("winner TTL = %d, want %d", ttl, test.want)
			}
		})
	}
}

func TestLyricsProvider_CachedNegativeWinnerAvoidsTheWalk(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
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

	provider := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	for range 2 {
		result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
		if err != nil {
			t.Fatalf("GetLyrics() error = %v, want nil", err)
		}
		if len(result.Lyrics) != 0 {
			t.Fatalf("GetLyrics() lyrics = %#v, want empty", result.Lyrics)
		}
	}
	if calls != 2 {
		t.Fatalf("source fetch count = %d, want 2 (first lookup only)", calls)
	}

	second := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	result, err := second.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil || len(result.Lyrics) != 0 {
		t.Fatalf("second GetLyrics() = %#v, %v; want cached empty result", result, err)
	}
	if calls != 2 {
		t.Fatalf("source fetch count = %d, want still 2 (cached negative winner)", calls)
	}
}

func TestLyricsProvider_WinnerKeyChangesWhenSourceOrderChanges(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	keyA, okA := lyricsWinnerCacheKey(track, []string{"better-lyrics", "unison", "lrclib"})
	keyB, okB := lyricsWinnerCacheKey(track, []string{"lrclib", "better-lyrics", "unison"})
	if !okA || !okB {
		t.Fatalf("winner cache keys not derived: %q/%q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("winner key %q identical across source orders", keyA)
	}
}

func TestLyricsProvider_PerSourceCacheReusesResultsAcrossReorders(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	lineText := "[00:01.00]line"
	dayText := "<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">day</tt>"
	networkSources := []lyricsSource{
		stubFetchedCandidate("one", "stub", "lrc", lineText),
		stubFetchedCandidate("two", "stub", "ttml", dayText),
	}
	provider := newLyricsProviderWithSources(nil, cache, time.Now, networkSources, func(lyrics.TrackInfo) error { return nil })

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	for _, source := range networkSources {
		if _, ok := provider.readCachedSourceCandidate(track, source.ID); !ok {
			t.Fatalf("source %q result missing from per-source cache", source.ID)
		}
	}
	if result.Source == nil || result.Source.Format != "ttml" {
		t.Fatalf("first result source = %#v, want the syllable stub", result.Source)
	}

	reordered := []lyricsSource{networkSources[1], networkSources[0]}
	reread := newLyricsProviderWithSources(nil, cache, time.Now, reordered, func(lyrics.TrackInfo) error { return nil })

	result, err = reread.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("reordered GetLyrics() error = %v, want nil", err)
	}
	if result.Source == nil || result.Source.Format != "ttml" {
		t.Fatalf("reordered result = %#v, want the syllable winner from cache", result.Source)
	}
	for _, sourceID := range []string{"one", "two"} {
		key, _ := lyricsCacheKeyForSource(track, sourceID)
		if _, ok, _ := cache.Get(key); !ok {
			t.Fatalf("per-source cache entry for %q missing", sourceID)
		}
	}
}

func TestLyricsProvider_PerSourceCacheSkipsOversizedResults(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	oversized := "[00:01.00]" + string(make([]byte, 1024))
	for len(oversized) <= maxCachedLyricsEntryBytes {
		oversized += oversized
	}
	source := lyricsSource{
		ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
		Fetch: fetchFixedCandidate("stub", "lrc", oversized),
	}
	provider := newLyricsProviderWithSources(nil, cache, time.Now, []lyricsSource{source}, func(lyrics.TrackInfo) error { return nil })

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if result.Source == nil {
		t.Fatal("GetLyrics() source = nil, want oversized result still returned")
	}
	if _, ok := provider.readCachedSourceCandidate(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, "one"); ok {
		t.Fatal("oversized candidate was cached, want it skipped")
	}
}
