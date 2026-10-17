package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
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

func TestWinnerEntriesRespectCredentialChanges(t *testing.T) {
	t.Parallel()

	// A completed winner recorded without a credential must not stay served
	// once an administrator provides one: the re-walk reaches the
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

func TestCredentialSaltFallbackStaysConsistent(t *testing.T) {
	t.Parallel()

	// When the store refuses the salt write, every fingerprint in this
	// process must share the same fallback — memoizing an unsaved random salt
	// would split fingerprints mid-process.
	cache := newFakeLyricsCache()
	cache.setError = errors.New("kv read-only")
	provider := newLyricsProviderWithDeps(nil, cache, time.Now, func(key string) (string, bool) {
		if key == "betterLyricsApiKey" {
			return "secret", true
		}
		return "", false
	}, nil)

	first := provider.lyricsCredentialsFingerprint()
	second := provider.lyricsCredentialsFingerprint()
	if first != second {
		t.Fatalf("fingerprints split across calls: %q vs %q", first, second)
	}
}

func TestGetLyrics_UpgradeCachedWinnerFromFreshSweepResults(t *testing.T) {
	t.Parallel()

	callsTwo := 0
	line := lyricsSource{
		ID: "one", Display: "line", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
		Fetch: fetchFixedCandidate("stub", "lrc", "[00:01.00]line"),
	}
	syllable := lyricsSource{
		ID: "two", Display: "two", Hosts: []string{"two.example.test"}, DefaultEnabled: true,
		Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			callsTwo++
			return []lyricsCandidate{newLyricsCandidate(lyrics.LyricsText{Text: "<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">rich</tt>"}, "stub", "ttml")}, lookupHealthy, nil
		},
	}
	cache := newFakeLyricsCache()
	now := time.Unix(5000, 0)
	sources := []lyricsSource{line, syllable}
	walkProvider := newLyricsProviderWithSourcesAt(nil, cache, func() time.Time { return now }, sources, func(lyrics.TrackInfo) error { return nil })

	// Cooldown-gate source two so the stage-one walk cannot cover it, keeping the
	// winner incomplete at line quality.
	if err := cache.SetWithTTL(sourceGlobalCooldownKey("two"), []byte(strconv.FormatInt(now.Add(time.Hour).Unix(), 10)), 3600); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	first, err := walkProvider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("first GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, first.Source, "stub", "lrc")

	// The sweep lands the syllable result for the previously skipped source.
	walkProvider.cacheSourceCandidate(track, "two", newLyricsCandidate(
		lyrics.LyricsText{Text: "<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Syllable\">rich</tt>"},
		"stub", "ttml"))

	reread := newLyricsProviderWithSourcesAt(nil, cache, func() time.Time { return now }, sources, func(lyrics.TrackInfo) error { return nil })
	result, err := reread.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("second GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "stub", "ttml")
	_ = callsTwo
}
