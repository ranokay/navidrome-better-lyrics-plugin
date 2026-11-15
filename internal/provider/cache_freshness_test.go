package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func TestCachedWinnerPromotionPreservesSourceExpiry(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"promotion", "replay", "negative winner"} {
		t.Run(mode, func(t *testing.T) {
			current := time.Unix(10_000, 0)
			cache := newFakeLyricsCache()
			candidate := lyric.NewCandidate(lyrics.LyricsText{Text: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><p begin="1" end="2"><span begin="1" end="2">old</span></p></tt>`}, "one", "ttml")
			order := []source.Source{stubFetchedCandidate("one", "one", "plain", "unused")}
			p := newTestProviderWithSources(nil, cache, func() time.Time { return current }, order, nil)
			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			p.cacheSourceCandidate(track, "one", candidate)
			if mode == "replay" {
				p.cacheWinner(track, order, candidate, false)
			}
			originalExpiry := current.Add(24 * time.Hour).Unix()
			current = current.Add(23 * time.Hour)
			if mode == "negative winner" {
				p.cacheNegativeWinner(track, order, false)
			}
			if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
				t.Fatal(err)
			}
			key, _ := lyricsWinnerCacheKey(track, source.IDs(order))
			envelope, ok := p.readCachedEnvelope(key)
			if !ok || envelope.ExpiresAt != originalExpiry || cache.winnerTTL(track, order) != 3600 {
				t.Fatalf("winner expiry=%d TTL=%d; want source expiry=%d and 3600s remaining", envelope.ExpiresAt, cache.winnerTTL(track, order), originalExpiry)
			}
		})
	}
}

func TestExpiredUnderlyingSourceAllowsLowerQualityOrMiss(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"[00:01.00]corrected", ""} {
		t.Run(map[bool]string{true: "miss", false: "lower quality"}[replacement == ""], func(t *testing.T) {
			current := time.Unix(20_000, 0)
			cache := newFakeLyricsCache()
			order := []source.Source{stubFetchedCandidate("one", "one", "lrc", replacement)}
			p := newTestProviderWithSources(nil, cache, func() time.Time { return current }, order, nil)
			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			old := lyric.NewCandidate(lyrics.LyricsText{Text: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><p begin="1" end="2"><span begin="1" end="2">old</span></p></tt>`}, "one", "ttml")
			p.cacheSourceCandidate(track, "one", old)
			p.cacheWinner(track, order, old, false)
			current = current.Add(23 * time.Hour)
			if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
				t.Fatal(err)
			}
			current = current.Add(time.Hour)
			p.runSweep(track)
			result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
			if err != nil {
				t.Fatal(err)
			}
			if replacement == "" {
				if len(result.Lyrics) != 0 {
					t.Fatalf("expired winner survived fresh miss: %#v", result.Lyrics)
				}
			} else if text, _ := winnerText(t, result); text != replacement {
				t.Fatalf("winner=%q; want fresh replacement=%q", text, replacement)
			}
		})
	}
}

func TestPreviouslyRenewedWinnerDoesNotOutliveSourceCache(t *testing.T) {
	t.Parallel()
	current := time.Unix(30_000, 0)
	cache := newFakeLyricsCache()
	order := []source.Source{stubFetchedCandidate("one", "one", "plain", "fresh")}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return current }, order, nil)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	old := lyric.NewCandidate(lyrics.LyricsText{Text: "old"}, "one", "plain")
	// Older releases could renew a winner after its source had expired.
	value, err := json.Marshal(cachedLyricsEnvelope{Generation: lyricsCacheGeneration, Response: old.Response, Credentials: p.lyricsCredentialsFingerprint(), ExpiresAt: current.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := lyricsWinnerCacheKey(track, source.IDs(order))
	if err := cache.SetWithTTL(key, value, 3600); err != nil {
		t.Fatal(err)
	}
	result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := winnerText(t, result); text != "fresh" {
		t.Fatalf("winner=%q; want fresh lookup after unbounded legacy winner", text)
	}
}

func TestSourceMissReservesHeadroomForControlRecords(t *testing.T) {
	t.Parallel()
	cache := &usageAwareLyricsCache{used: lyricsCacheMaxBytes - lyricsCacheReservedBytes}
	p := newTestProviderWithDependencies(nil, cache, time.Now)
	_ = p.lyricsCredentialsFingerprint()
	before := cache.sets
	p.cacheSourceMiss(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, "one")
	if writes := cache.sets - before; writes != 0 {
		t.Fatalf("negative coverage writes=%d at headroom boundary; want 0", writes)
	}
	p.RememberCooldown(source.GlobalCooldownKey("one"), time.Minute)
	if writes := cache.sets - before; writes != 1 {
		t.Fatalf("control writes=%d; want cooldown persistence despite lyric guard", writes)
	}
}
