package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func stubSource(id string, calls *[]string, result func() ([]lyricsCandidate, lookupHealth, error)) lyricsSource {
	return lyricsSource{
		ID:             id,
		Display:        id,
		Hosts:          []string{id + ".example.test"},
		DefaultEnabled: true,
		Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			*calls = append(*calls, id)
			return result()
		},
	}
}

func lrcCandidate(word bool) lyricsCandidate {
	text := "[00:01.00]line text"
	if word {
		text = "[00:01.00]<00:01.00>line<00:01.50>text"
	}
	return newLyricsCandidate(lyrics.LyricsText{Text: text}, "stub", "elrc")
}

func ttmlCandidate(quality string) lyricsCandidate {
	return newLyricsCandidate(lyrics.LyricsText{Text: "<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"" + quality + "\">x</tt>"}, "two", "ttml")
}

func TestLyricsProvider_StageOneWalksFirstThreeImplementedSources(t *testing.T) {
	t.Parallel()

	var calls []string
	sources := []lyricsSource{
		stubSource("one", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return nil, lookupHealthy, nil
		}),
		stubSource("two", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return []lyricsCandidate{ttmlCandidate("Word")}, lookupHealthy, nil
		}),
		stubSource("three", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return []lyricsCandidate{ttmlCandidate("Line")}, lookupHealthy, nil
		}),
		stubSource("four", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return []lyricsCandidate{ttmlCandidate("Syllable")}, lookupHealthy, nil
		}),
	}

	provider := newLyricsProviderWithSources(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{}, nil
	}, newFakeLyricsCache(), time.Now, sources, func(lyrics.TrackInfo) error { return nil })

	result, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Album: "Album", Duration: 100,
	}})
	if err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if want := []string{"one", "two", "three"}; !equalStrings(calls, want) {
		t.Fatalf("walked sources = %v, want %v (stage one stops after three)", calls, want)
	}
	assertLyricsSource(t, result.Source, "two", "ttml")
}

func TestLyricsProvider_StageOneStopsEarlyOnSyllable(t *testing.T) {
	t.Parallel()

	var calls []string
	sources := []lyricsSource{
		stubSource("first", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return []lyricsCandidate{lrcCandidate(true)}, lookupHealthy, nil
		}),
		stubSource("second", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return []lyricsCandidate{ttmlCandidate("Syllable")}, lookupHealthy, nil
		}),
		stubSource("third", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return nil, lookupHealthy, nil
		}),
	}
	provider := newLyricsProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, func(lyrics.TrackInfo) error { return nil })

	if _, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}}); err != nil {
		t.Fatalf("GetLyrics() error = %v, want nil", err)
	}
	if want := []string{"first", "second"}; !equalStrings(calls, want) {
		t.Fatalf("walked sources = %v, want %v (syllable result ends the walk)", calls, want)
	}
}

func TestLyricsProvider_MergesSourceErrorsIntoJoinedLookupError(t *testing.T) {
	t.Parallel()

	var calls []string
	sources := []lyricsSource{
		stubSource("one", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return nil, lookupDegraded, errors.New("one down")
		}),
		stubSource("two", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return nil, lookupDegraded, errors.New("two down")
		}),
	}
	provider := newLyricsProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, func(lyrics.TrackInfo) error { return nil })

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
	sources := []lyricsSource{
		stubSource("uncertain", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return nil, lookupUncertain, nil
		}),
		stubSource("line", &calls, func() ([]lyricsCandidate, lookupHealth, error) {
			return []lyricsCandidate{lrcCandidate(false)}, lookupHealthy, nil
		}),
	}
	provider := newLyricsProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })

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
	provider := newLyricsProviderWithSources(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		return &host.HTTPResponse{StatusCode: 200}, nil
	}, newFakeLyricsCache(), time.Now, []lyricsSource{
		stubSource("one", &([]string{}), func() ([]lyricsCandidate, lookupHealth, error) { return nil, lookupHealthy, nil }),
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
