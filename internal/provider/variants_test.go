package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// variantSource is a stub source that always serves one candidate, so tests can
// pin how cached per-source results are served as source-tagged variants.
func variantSource(id, text, format string) source.Source {
	return source.Source{
		ID:             id,
		Display:        id,
		Hosts:          []string{id + ".example.test"},
		DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: text}, id, format)}, source.Healthy, nil
		},
	}
}

func variantProviders(t *testing.T, result lyrics.GetLyricsResponse) []string {
	t.Helper()
	providers := make([]string, 0, len(result.Lyrics))
	for _, text := range result.Lyrics {
		if text.Source == nil {
			t.Fatalf("variant %d has no source: %#v", len(providers), text)
		}
		providers = append(providers, text.Source.Provider)
	}
	return providers
}

func TestProvider_ServesEveryCachedSourceAsAVariant(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 100}
	sources := []source.Source{
		variantSource("one", "[00:01.00]plain line", "lrc"),
		variantSource("two", "[00:01.00]<00:01.00>word line", "elrc"),
		variantSource("three", "line text", "lrc"),
	}
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}

	// The winner (word timing) leads; the rest follow in configured order.
	if got, want := variantProviders(t, result), []string{"two", "one", "three"}; !equalStrings(got, want) {
		t.Fatalf("variant providers = %v, want %v", got, want)
	}
	if !strings.Contains(result.Lyrics[0].Text, "word line") {
		t.Fatalf("first variant text = %q, want the winner's word line", result.Lyrics[0].Text)
	}
	if result.Lyrics[0].Source.Format != "elrc" || result.Lyrics[1].Source.Format != "lrc" {
		t.Fatalf("variant formats = %q, %q, want elrc, lrc", result.Lyrics[0].Source.Format, result.Lyrics[1].Source.Format)
	}
	// Older hosts read only the response-level source: it must keep naming the
	// winner.
	assertLyricsSource(t, result.Source, "two", "elrc")
}

func TestProvider_ServesVariantsFromCacheOnReplays(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 100}
	sources := []source.Source{
		variantSource("one", "[00:01.00]plain line", "lrc"),
		variantSource("two", "[00:01.00]<00:01.00>word line", "elrc"),
		variantSource("three", "line text", "lrc"),
	}
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)

	first, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	replay, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("replay GetLyrics() error = %v, want nil", err)
	}

	if got, want := variantProviders(t, replay), []string{"two", "one", "three"}; !equalStrings(got, want) {
		t.Fatalf("replay variant providers = %v, want %v", got, want)
	}
	if len(first.Lyrics) != len(replay.Lyrics) {
		t.Fatalf("replay variants = %d, want %d", len(replay.Lyrics), len(first.Lyrics))
	}
}

func TestProvider_SkipsCachedMissesWhenServingVariants(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 100}
	sources := []source.Source{
		variantSource("one", "[00:01.00]plain line", "lrc"),
		variantSource("two", "[00:01.00]<00:01.00>word line", "elrc"),
		variantSource("three", "line text", "lrc"),
	}
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)
	if !provider.cacheSourceMiss(track, "three") {
		t.Fatal("cacheSourceMiss() = false, want the miss marker to be stored")
	}

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}

	// A miss marker is coverage, not lyrics: it must not become a variant.
	if got, want := variantProviders(t, result), []string{"two", "one"}; !equalStrings(got, want) {
		t.Fatalf("variant providers = %v, want %v", got, want)
	}
}

func TestProvider_ServesTheWinnerAloneWhenNothingElseIsCached(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 100}
	sources := []source.Source{
		stubSource("one", new([]string), func() ([]lyric.Candidate, source.Health, error) {
			return nil, source.Healthy, nil
		}),
		variantSource("two", "[00:01.00]<00:01.00>word line", "elrc"),
	}
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}

	if got, want := variantProviders(t, result), []string{"two"}; !equalStrings(got, want) {
		t.Fatalf("variant providers = %v, want %v", got, want)
	}
}
