package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func TestForegroundBudgetsEachSourceAndTheEntireLookup(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	current := start
	var timeouts []int32
	var deadlines []time.Duration
	sources := make([]source.Source, 0, 3)
	for _, id := range []string{"one", "two", "three"} {
		sources = append(sources, source.Source{ID: id, Display: id,
			Fetch: func(rt source.Runtime, _ lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, source.Health, error) {
				deadlines = append(deadlines, deadline.Sub(start))
				_, err := rt.Do(id, "GET", "https://example.test", nil, nil, deadline)
				return nil, source.Degraded, err
			},
		})
	}
	provider := newTestProviderWithSources(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		timeouts = append(timeouts, request.TimeoutMs)
		current = current.Add(time.Duration(request.TimeoutMs) * time.Millisecond)
		return nil, errors.New("upstream timeout")
	}, newFakeLyricsCache(), func() time.Time { return current }, sources, nil)
	_, err := provider.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err == nil {
		t.Fatal("GetLyrics() error = nil, want transport failure")
	}
	if fmt.Sprint(timeouts) != "[750 750 500]" {
		t.Fatalf("HTTP timeouts = %v, want separate 750ms budgets capped by 2s total", timeouts)
	}
	if fmt.Sprint(deadlines) != "[750ms 1.5s 2s]" || current.Sub(start) != 2*time.Second {
		t.Fatalf("source deadlines = %v, elapsed = %s; want the next source to start after 750ms and total 2s", deadlines, current.Sub(start))
	}
}

func TestOutageBackoffIsSharedAcrossTracksInstancesAndSweeps(t *testing.T) {
	t.Parallel()

	current := time.Unix(2000, 0)
	cache := newFakeLyricsCache()
	brokenCalls, healthyCalls := 0, 0
	outage := true
	sources := []source.Source{
		{ID: "broken", Display: "broken", Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			brokenCalls++
			if outage {
				return nil, source.Degraded, errors.New("malformed upstream response")
			}
			return nil, source.Healthy, nil
		}},
		{ID: "healthy", Display: "healthy", Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			healthyCalls++
			return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
		}},
	}
	newProvider := func() *Provider {
		return newTestProviderWithSources(nil, cache, func() time.Time { return current }, sources, nil)
	}
	lookup := func(title string) *Provider {
		t.Helper()
		p := newProvider()
		result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: title, Artist: "Artist"}})
		if err != nil || len(result.Lyrics) == 0 {
			t.Fatalf("GetLyrics(%s) = %v, %v; want healthy sibling lyrics", title, result, err)
		}
		return p
	}
	lookup("first")
	second := lookup("second")
	track := lyrics.TrackInfo{Title: "sweep", Artist: "Artist"}
	second.runSweep(track)
	if brokenCalls != 1 || healthyCalls != 3 {
		t.Fatalf("fetches broken=%d healthy=%d; want failed source skipped across instances/tracks/sweep", brokenCalls, healthyCalls)
	}
	if second.sweepMarkerActive(second.doneMarkerKey(track, sources)) {
		t.Fatal("backoff-only skip became completed discovery coverage")
	}
	current = current.Add(5 * time.Second)
	third := lookup("retry")
	if brokenCalls != 2 || third.sourceFailureBackoff("broken") != 10*time.Second {
		t.Fatalf("retry calls=%d backoff=%s; want a retry after 5s and escalation to 10s", brokenCalls, third.sourceFailureBackoff("broken"))
	}
	current = current.Add(10 * time.Second)
	outage = false
	lookup("recovered")
	outage = true
	last := lookup("new outage")
	if brokenCalls != 4 || last.sourceFailureBackoff("broken") != 5*time.Second {
		t.Fatalf("recovery did not reset backoff: calls=%d backoff=%s", brokenCalls, last.sourceFailureBackoff("broken"))
	}
}

func TestForegroundTimeoutDoesNotSuppressTheLongerBackgroundAttempt(t *testing.T) {
	t.Parallel()

	current := time.Unix(1500, 0)
	slowCalls := 0
	sources := []source.Source{
		{ID: "slow", Display: "slow", Fetch: func(rt source.Runtime, _ lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, source.Health, error) {
			slowCalls++
			response, err := rt.Do("slow", "GET", "https://example.test", nil, nil, deadline)
			if err != nil {
				return nil, source.Degraded, err
			}
			return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: string(response.Body)}, "slow", "elrc")}, source.Healthy, nil
		}},
		variantSource("fast", "[00:01.00]fast line", "lrc"),
	}
	p := newTestProviderWithSources(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if request.TimeoutMs < 1000 {
			current = current.Add(time.Duration(request.TimeoutMs) * time.Millisecond)
			return nil, errors.New("request timed out")
		}
		current = current.Add(time.Second)
		return &host.HTTPResponse{StatusCode: 200, Body: []byte("[00:01.00]<00:01.00>slow words")}, nil
	}, newFakeLyricsCache(), func() time.Time { return current }, sources, nil)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	first, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatal(err)
	}
	assertLyricsSource(t, first.Source, "fast", "lrc")
	if p.sourceFailureBackoff("slow") != 0 {
		t.Fatal("a foreground budget timeout was mistaken for a provider outage")
	}
	p.runSweep(track)
	result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil || slowCalls != 2 {
		t.Fatalf("background retry error=%v calls=%d; want a successful longer attempt", err, slowCalls)
	}
	assertLyricsSource(t, result.Source, "slow", "elrc")
}

func TestCredentialChangesRefreshPositiveSourceCoverageDuringSweep(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ sourceID, key string }{
		{"better-lyrics", "betterLyricsApiKey"},
		{"better-lyrics-legato", "betterLyricsApiKey"},
		{"better-lyrics-portato", "betterLyricsApiKey"},
		{"musixmatch", "musixmatchUserToken"},
	} {
		t.Run(tc.sourceID, func(t *testing.T) {
			config := map[string]string{}
			calls, siblingCalls, enqueued := 0, 0, 0
			sources := []source.Source{
				{ID: tc.sourceID, Display: tc.sourceID, Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
					calls++
					return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: fmt.Sprintf("[00:01.00]version %d", calls)}, "dynamic provider", "lrc")}, source.Healthy, nil
				}},
				sweepSource("independent", &siblingCalls, true, nil),
			}
			p := newTestProviderWithDeps(nil, newFakeLyricsCache(), time.Now, func(key string) (string, bool) {
				value, ok := config[key]
				return value, ok
			}, func(lyrics.TrackInfo) error { enqueued++; return nil })
			p.sources = sources
			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			p.runSweep(track)
			// Changing an unrelated credential must not invalidate this source.
			otherKey := "musixmatchUserToken"
			if tc.key == otherKey {
				otherKey = "betterLyricsApiKey"
			}
			config[otherKey] = "unrelated"
			p.runSweep(track)
			if calls != 1 {
				t.Fatal("unrelated credential change invalidated positive source coverage")
			}
			config[tc.key] = "new credential"
			result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
			if err != nil || calls != 1 || enqueued != 1 {
				t.Fatalf("cached display after credential change: error=%v calls=%d enqueues=%d", err, calls, enqueued)
			}
			if text, _ := winnerText(t, result); text != "[00:01.00]version 1" {
				t.Fatalf("cached display = %q; want immediately available old result", text)
			}
			p.runSweep(track)
			if calls != 2 || siblingCalls != 1 {
				t.Fatalf("sweep fetches=%d independent=%d; want relevant source refreshed only", calls, siblingCalls)
			}
			result, err = p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
			if err != nil {
				t.Fatal(err)
			}
			if text, _ := winnerText(t, result); text != "[00:01.00]version 2" {
				t.Fatalf("refreshed display = %q, want newly authorized source result", text)
			}
		})
	}
}

func TestSourceMissAndSweepCompletionExpireAfterFiveMinutes(t *testing.T) {
	t.Parallel()

	current := time.Unix(3000, 0)
	calls, enqueued := 0, 0
	cache := newFakeLyricsCache() // Deliberately retains bytes after host TTL.
	p := newTestProviderWithSources(nil, cache, func() time.Time { return current }, []source.Source{
		sweepSource("missing", &calls, false, nil),
	}, func(lyrics.TrackInfo) error { enqueued++; return nil })
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	p.runSweep(track)
	if p.maybeEnqueueSweep(track) {
		t.Fatal("fresh completed miss unexpectedly scheduled a sweep")
	}
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	current = current.Add(5 * time.Minute)
	if !p.maybeEnqueueSweep(track) || enqueued != 1 {
		t.Fatal("expired five-minute done marker suppressed retry")
	}
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("source fetches = %d, want the expired source miss and winner refetched", calls)
	}
}

func TestPositiveWinnerDoesNotOutliveSiblingMissCoverage(t *testing.T) {
	t.Parallel()

	current := time.Unix(3500, 0)
	calls, enqueued := 0, 0
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), func() time.Time { return current }, []source.Source{
		sweepSource("missing", &calls, false, nil),
		variantSource("timed", "[00:01.00]cached line", "lrc"),
	}, func(lyrics.TrackInfo) error { enqueued++; return nil })
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	if enqueued != 0 {
		t.Fatal("a fully covered foreground lookup unnecessarily scheduled discovery")
	}
	current = current.Add(5 * time.Minute)
	result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil || enqueued != 1 || calls != 1 {
		t.Fatalf("expired sibling coverage: err=%v enqueued=%d foreground fetches=%d", err, enqueued, calls)
	}
	assertLyricsSource(t, result.Source, "timed", "lrc")
	p.runSweep(track)
	if calls != 2 {
		t.Fatalf("miss refreshes = %d, want the expired sibling retried by the sweep", calls)
	}
}

func TestVariantsDeduplicateDynamicProviderIdentity(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	sources := []source.Source{{ID: "one", Display: "Catalog alias"}, {ID: "two", Display: "Second alias"}}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)
	winner := lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]winner"}, "Actual upstream", "lrc")
	p.cacheSourceCandidate(track, "one", winner)
	p.cacheSourceCandidate(track, "two", lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]alternative"}, "Other upstream", "lrc"))
	result := p.responsesWithVariants(track, sources, winner)
	if got := variantProviders(t, result); !equalStrings(got, []string{"Actual upstream", "Other upstream"}) {
		t.Fatalf("variant providers = %v; want one entry per actual provider", got)
	}
}

func TestFailureBackoffRemainsBoundedAndFailsOpen(t *testing.T) {
	t.Parallel()

	current := time.Unix(4000, 0)
	p := newTestProviderWithDependencies(nil, newFakeLyricsCache(), func() time.Time { return current })
	for _, expected := range []time.Duration{5, 10, 20, 40, 60, 60} {
		p.rememberSourceFailure("outage", errors.New("upstream failed"))
		got := p.sourceFailureBackoff("outage")
		if got != expected*time.Second {
			t.Fatalf("backoff = %s, want %s", got, expected*time.Second)
		}
		current = current.Add(got)
	}
	cache := newFakeLyricsCache()
	cache.setError = errors.New("KV unavailable")
	calls := 0
	p = newTestProviderWithSources(nil, cache, func() time.Time { return current }, []source.Source{
		sweepSource("unavailable", &calls, false, errors.New("upstream failed")),
	}, nil)
	for range 2 {
		_, _ = p.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	}
	if calls != 2 {
		t.Fatalf("fetches with failed KV = %d; want 2 because failed persistence cannot disable a source", calls)
	}
}

func TestRateLimitsAndBudgetExhaustionKeepTheirOwnRetryPolicy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		make func(*fakeLyricsCache) *Provider
	}{
		{"budget", func(cache *fakeLyricsCache) *Provider {
			return newTestProviderWithSources(nil, cache, time.Now, []source.Source{
				{ID: "better-lyrics", Display: "Better Lyrics", Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
					return nil, source.Degraded, fmt.Errorf("no next hop: %w", source.ErrLookupBudgetExhausted)
				}},
			}, nil)
		}},
		{"rate limit", func(cache *fakeLyricsCache) *Provider {
			return providerForCatalogWithCache(func(host.HTTPRequest) (*host.HTTPResponse, error) {
				return &host.HTTPResponse{StatusCode: 429, Headers: map[string]string{"Retry-After": "37"}}, nil
			}, cache, time.Now, "better-lyrics")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newFakeLyricsCache()
			p := tc.make(cache)
			_, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
			if err == nil {
				t.Fatal("GetLyrics() error = nil; want retryable failure")
			}
			if tc.name == "budget" && !errors.Is(err, source.ErrLookupBudgetExhausted) {
				t.Fatalf("lookup error lost budget identity: %v", err)
			}
			if _, exists, _ := cache.Get(sourceFailureKey("better-lyrics")); exists {
				t.Fatal("lookup budget or provider Retry-After acquired a separate outage backoff")
			}
		})
	}
}

func TestSweepQueueCreationFailureExplainsRecovery(t *testing.T) {
	t.Parallel()

	var log string
	p := New(Deps{
		CreateQueue: func(string, int32) error { return errors.New("task worker unavailable") },
		Logf:        func(message string) { log = message },
	})
	if err := p.OnInit(); err != nil {
		t.Fatalf("OnInit() error = %v; want foreground lookup to remain available", err)
	}
	if !strings.Contains(log, "background source discovery unavailable") || !strings.Contains(log, "reload") {
		t.Fatalf("queue creation diagnostic = %q; want the missing feature and recovery action", log)
	}
}
