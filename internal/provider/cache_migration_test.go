package provider

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

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
