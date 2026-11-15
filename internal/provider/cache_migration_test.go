package provider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func TestPreviousGenerationMarkersCannotSuppressLegacyRefresh(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"completed", "pending"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Unix(100_000, 0)
			cache := newFakeLyricsCache()
			queued := 0
			order := []source.Source{stubFetchedCandidate("one", "one", "lrc", "[00:01.00]fresh")}
			p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, order, func(lyrics.TrackInfo) error { queued++; return nil })
			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			legacy := lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]legacy"}, "one", "lrc")
			raw, err := json.Marshal(cachedLyricsEnvelope{Generation: 1, Quality: legacy.Quality, Response: legacy.Response, ExpiresAt: now.Add(24 * time.Hour).Unix()})
			if err != nil {
				t.Fatal(err)
			}
			key, _ := lyricsCacheKeyForSource(track, "one")
			if err := cache.SetWithTTL(key, raw, 86400); err != nil {
				t.Fatal(err)
			}
			trackKey, _ := lyricsTrackKey(track)
			digest := sha256.Sum256([]byte(strings.Join(source.IDs(order), "|")))
			credentials := p.lyricsCredentialsFingerprint()
			// Literal pre-generation marker formats model a real persisted upgrade.
			oldKey := fmt.Sprintf("sweep-done:%x:%s:cred:%s", digest, trackKey, credentials)
			if kind == "pending" {
				oldKey = fmt.Sprintf("sweep:%s:%x:%s", credentials, digest[:8], trackKey)
			}
			if err := p.storeSweepMarker(oldKey, 86400); err != nil {
				t.Fatal(err)
			}
			result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
			if err != nil {
				t.Fatal(err)
			}
			if text, _ := winnerText(t, result); text != "[00:01.00]legacy" || queued != 1 {
				t.Fatalf("legacy content=%q queued=%d, want readable content with fresh coverage scheduled", text, queued)
			}
		})
	}
}

func TestCachedMetadataOnlyPayloadDoesNotBlockFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ format, text string }{
		{"lrc", "[ar:Artist]\n[ti:Song]\n[00:01.00]"},
		{"ttml", "<tt><head><metadata>Song</metadata></head></tt>"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			now := time.Unix(100_000, 0)
			cache := newFakeLyricsCache()
			order := []source.Source{stubFetchedCandidate("one", "one", "plain", "real lyrics")}
			p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, order, nil)
			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			raw, err := json.Marshal(cachedLyricsEnvelope{
				Generation: 1, Quality: lyric.TimingLine,
				Response: lyrics.GetLyricsResponse{
					Lyrics: []lyrics.LyricsText{{Text: tc.text}},
					Source: &lyrics.LyricsSource{Provider: "one", Format: tc.format},
				},
				ExpiresAt: now.Add(time.Hour).Unix(),
			})
			if err != nil {
				t.Fatal(err)
			}
			key, _ := lyricsCacheKeyForSource(track, "one")
			if err := cache.SetWithTTL(key, raw, 3600); err != nil {
				t.Fatal(err)
			}
			result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
			if err != nil {
				t.Fatal(err)
			}
			if text, _ := winnerText(t, result); text != "real lyrics" {
				t.Fatalf("cached metadata blocked fallback: %q", text)
			}
		})
	}
}

func TestLegacySourceMissDoesNotExtendOldHourlyCoverage(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	var calls []string
	src := stubSource("one", &calls, func() ([]lyric.Candidate, source.Health, error) {
		return nil, source.Healthy, nil
	})
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), func() time.Time { return now }, []source.Source{src}, nil)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	key, _ := p.sourceMissKey(track, src.ID)
	old, _ := json.Marshal(cachedLyricsEnvelope{ExpiresAt: now.Add(time.Hour).Unix()})
	if err := p.cache.SetWithTTL(key, old, 3600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("legacy hourly miss suppressed new lookup: %v", calls)
	}
	entry, _ := p.readCachedEnvelope(key)
	if entry.Generation != lyricsCacheGeneration || entry.ExpiresAt != now.Add(5*time.Minute).Unix() {
		t.Fatalf("new miss did not adopt current five-minute coverage: %#v", entry)
	}
}

func TestLegacyPositiveRemainsReadableButCannotBlockFreshGrading(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	var calls []string
	enqueued := 0
	src := stubSource("one", &calls, func() ([]lyric.Candidate, source.Health, error) {
		return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]fresh synchronized line"}, "one", "lrc")}, source.Healthy, nil
	})
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), func() time.Time { return now }, []source.Source{src}, func(lyrics.TrackInfo) error { enqueued++; return nil })
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	legacy := cachedLyricsEnvelope{
		Quality: lyric.TimingSyllable,
		Response: lyrics.GetLyricsResponse{
			Lyrics: []lyrics.LyricsText{{Text: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><body><p>old untimed text</p></body></tt>`}},
			Source: &lyrics.LyricsSource{Provider: "one", Format: "ttml"},
		},
		Credentials:   p.lyricsCredentialsFingerprint(),
		SweepComplete: true,
		ExpiresAt:     now.Add(24 * time.Hour).Unix(),
	}
	bytes, _ := json.Marshal(legacy)
	sourceKey, _ := lyricsCacheKeyForSource(track, src.ID)
	winnerKey, _ := lyricsWinnerCacheKey(track, []string{src.ID})
	for _, key := range []string{sourceKey, winnerKey} {
		if err := p.cache.SetWithTTL(key, bytes, 86400); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := p.readCachedWinner(track, []source.Source{src}); ok {
		t.Fatal("legacy winner must not authorize current coverage")
	}
	response, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(response.Lyrics) == 0 || !strings.Contains(response.Lyrics[0].Text, "old untimed") || enqueued != 1 {
		t.Fatalf("legacy positive should display and refresh: response=%v error=%v queued=%d", response, err, enqueued)
	}
	if candidate, _ := p.readCachedSourceCandidate(track, src.ID); candidate.Quality != lyric.TimingUnsynced {
		t.Fatal("legacy declaration must be regraded before becoming a winner")
	}
	p.runSweep(track)
	response, err = p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil || !strings.Contains(response.Lyrics[0].Text, "fresh synchronized") || len(calls) != 1 {
		t.Fatalf("fresh lower stored grade was blocked by legacy winner: response=%v error=%v calls=%v", response, err, calls)
	}
}
