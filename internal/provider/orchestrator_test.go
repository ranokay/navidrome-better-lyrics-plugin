package provider

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func stubSource(id string, calls *[]string, result func() ([]lyric.Candidate, source.Health, error)) source.Source {
	return source.Source{
		ID:             id,
		Display:        id,
		Hosts:          []string{id + ".example.test"},
		DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			*calls = append(*calls, id)
			return result()
		},
	}
}

func lrcCandidate(word bool) lyric.Candidate {
	text := "[00:01.00]line text"
	if word {
		text = "[00:01.00]<00:01.00>line<00:01.50>text"
	}
	return lyric.NewCandidate(lyrics.LyricsText{Text: text}, "stub", "elrc")
}

func ttmlCandidate(quality string) lyric.Candidate {
	return lyric.NewCandidate(lyrics.LyricsText{Text: "<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"" + quality + "\"><p begin=\"1\" end=\"2\"><span begin=\"1\" end=\"2\">x</span></p></tt>"}, "two", "ttml")
}

func TestLyricsProvider_StageOneStopsAtFirstSynchronizedSource(t *testing.T) {
	t.Parallel()

	var calls []string
	sources := []source.Source{
		stubSource("one", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return nil, source.Healthy, nil
		}),
		stubSource("two", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{ttmlCandidate("Word")}, source.Healthy, nil
		}),
		stubSource("three", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{ttmlCandidate("Line")}, source.Healthy, nil
		}),
		stubSource("four", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{ttmlCandidate("Syllable")}, source.Healthy, nil
		}),
	}

	provider := newTestProviderWithSources(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{}, nil
	}, newFakeLyricsCache(), time.Now, sources, func(lyrics.TrackInfo) error { return nil })

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 100,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if want := []string{"one", "two"}; !equalStrings(calls, want) {
		t.Fatalf("walked sources = %v, want %v (first synchronized source is ready)", calls, want)
	}
	assertLyricsSource(t, result.Source, "two", "ttml")
}

func TestLyricsProvider_StageOneDoesNotWaitToUpgradeWordTiming(t *testing.T) {
	t.Parallel()

	var calls []string
	sources := []source.Source{
		stubSource("first", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{lrcCandidate(true)}, source.Healthy, nil
		}),
		stubSource("second", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{ttmlCandidate("Syllable")}, source.Healthy, nil
		}),
		stubSource("third", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return nil, source.Healthy, nil
		}),
	}
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, func(lyrics.TrackInfo) error { return nil })

	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}}); err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if want := []string{"first"}; !equalStrings(calls, want) {
		t.Fatalf("walked sources = %v, want %v (word timing is ready)", calls, want)
	}
}

func TestLyricsProvider_MergesSourceErrorsIntoJoinedLookupError(t *testing.T) {
	t.Parallel()

	var calls []string
	sources := []source.Source{
		stubSource("one", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return nil, source.Degraded, errors.New("one down")
		}),
		stubSource("two", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return nil, source.Degraded, errors.New("two down")
		}),
	}
	provider := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, func(lyrics.TrackInfo) error { return nil })

	_, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err == nil {
		t.Fatal("GetLyrics() error = nil, want joined source errors")
	}
	if !strings.Contains(err.Error(), "one down") || !strings.Contains(err.Error(), "two down") {
		t.Fatalf("GetLyrics() error = %v, want both source errors", err)
	}
}

func TestLyricsProvider_MergesHealthFromSourceStatuses(t *testing.T) {
	t.Parallel()

	var calls []string
	cache := newFakeLyricsCache()
	sources := []source.Source{
		stubSource("uncertain", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return nil, source.Uncertain, nil
		}),
		stubSource("line", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
		}),
	}
	provider := newTestProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	assertLyricsSource(t, result.Source, "stub", "elrc")
	if ttl := cache.winnerTTL(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, sources); ttl != unsyncedLyricsCacheTTLSeconds {
		t.Fatalf("cache TTL = %d, want uncertain TTL %d", ttl, unsyncedLyricsCacheTTLSeconds)
	}
}

func TestLyricsProvider_SkipsIncompleteTrackMetadata(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := newTestProviderWithSources(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 200}, nil
	}, newFakeLyricsCache(), time.Now, []source.Source{
		stubSource("one", &([]string{}), func() ([]lyric.Candidate, source.Health, error) { return nil, source.Healthy, nil }),
	}, func(lyrics.TrackInfo) error { return nil })

	for _, track := range []lyrics.TrackInfo{
		{Artist: "Artist"},
		{Title: "Song"},
	} {
		result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: track})
		if err != nil {
			t.Fatalf("GetLyrics(%v) error = %v, want nil", track, err)
		}
		if len(result.Lyrics) != 0 {
			t.Fatalf("GetLyrics(%v) lyrics = %#v, want empty", track, result.Lyrics)
		}
	}
	if requests != 0 {
		t.Fatalf("GetLyrics() request count = %d, want 0 for incomplete metadata", requests)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
