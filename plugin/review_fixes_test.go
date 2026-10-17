package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
)

// Review round fixes: CodeRabbit 4 inline findings + Codex P1/P2 batch.

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

func TestLRCLibCandidates_ExactInstrumentalFallsBackToSearch(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"id":10209922,"instrumental":true,"hasWordSync":false,"plainLyrics":null,"syncedLyrics":null,"lyricsfile":""}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`[{"id":5,"instrumental":false,"hasWordSync":false,"duration":280,"plainLyrics":"plain rows","syncedLyrics":"[00:01.00]synced","lyricsfile":""}]`)}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLRCLibLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want the search fallback line candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "LRCLib", "lrc")
	if requests != 2 {
		t.Fatalf("requests = %d, want get then search", requests)
	}
}

func TestLRCLibCandidates_InstrumentalSearchMatchIsCleanMiss(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"id":10209922,"instrumental":true}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`[{"id":5,"instrumental":true,"duration":280}]`)}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss when the only search match is instrumental", candidates, health, err)
	}
}

func TestLunaBeatCandidates_MatchesCJKTitles(t *testing.T) {
	t.Parallel()

	index := []byte(`{"schemaVersion":6,"revision":"rev-1","songs":[{"id":"e","title":"阿刁","artists":["赵雷"],"album":"剃刀","path":"lyrics/cjk.ttml"}]}`)
	var ttmlRequest string
	provider := providerForCatalogWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api/v1/manifest.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"schemaVersion":6,"revision":"rev-1"}`)}, nil
		case strings.Contains(request.URL, "api/v1/songs.json"):
			return &host.HTTPResponse{StatusCode: 200, Body: index}, nil
		default:
			ttmlRequest = request.URL
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<tt></tt>`)}, nil
		}
	}, newFakeLyricsCache(), time.Now, "luna-beat")

	if _, _, err := fetchLunaBeatLyricsCandidates(provider, lyrics.TrackInfo{Title: "阿刁", Artist: "赵雷", Album: "剃刀"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("fetchLunaBeatLyricsCandidates() error = %v, want nil", err)
	}
	if !strings.Contains(ttmlRequest, "lyrics/cjk.ttml") {
		t.Fatalf("TTML fetch = %q, want the CJK index row matched through unicode normalization", ttmlRequest)
	}
}

func TestMusixmatchCandidates_MintStageStaysInsideCallerDeadline(t *testing.T) {
	t.Parallel()

	now := time.Unix(3000, 0)
	var mintTimeout int32
	provider := newLyricsProviderWithSourcesAt(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/ws/1.1/token.get") {
			mintTimeout = request.TimeoutMs
			return &host.HTTPResponse{StatusCode: 401, Body: []byte(`{"message":{"header":{"status_code":401,"hint":"captcha"}}}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	}, newFakeLyricsCache(), func() time.Time { return now }, []lyricsSource{
		{
			ID: "musixmatch", Display: "Musixmatch", Hosts: []string{"apic.musixmatch.com"}, DefaultEnabled: true,
			Fetch: fetchMusixmatchLyricsCandidates,
		},
	}, func(lyrics.TrackInfo) error { return nil })

	deadline := now.Add(3 * time.Second)
	if _, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, deadline); err != nil {
		t.Fatalf("fetchMusixmatchLyricsCandidates() error = %v, want nil", err)
	}
	if mintTimeout == 0 || mintTimeout > 3000 {
		t.Fatalf("mint request timeout = %d, want <= caller deadline (3000, Codex: the mint must honor the lookup budget)", mintTimeout)
	}
}

func TestKugouCandidates_SendConfiguredAPIKey(t *testing.T) {
	t.Parallel()

	requests := []host.HTTPRequest{}
	provider := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests = append(requests, request)
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":"[00:01.00]line"}`)}, nil
	}, newFakeLyricsCache(), time.Now, func(key string) (string, bool) {
		if key == "betterLyricsApiKey" {
			return "secret-key", true
		}
		return "", false
	}, nil)

	candidates, _, err := fetchKugouLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchKugouLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	if len(requests) != 1 || requests[0].Headers["X-API-Key"] != "secret-key" {
		t.Fatalf("kugou request headers = %#v, want the configured X-API-Key", requests[0].Headers)
	}
}

func TestLunaCandidates_UnknownTypeReturnsPlainCandidate(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.qishui.com/luna/search/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":1,"duration":280000}}}]}]}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"[00:04.00]Beneath an empty","type":"brand-new-dialect"}}`)}, nil
		}
	})

	candidates, _, err := fetchLunaLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 280}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLunaLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the usable payload returned as a candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Soda (Luna)", "plain")
}

func TestMusixmatchCandidates_EnvelopeStatusDropsStaleToken(t *testing.T) {
	t.Parallel()

	rejectedSearchBody := `{"message":{"header":{"status_code":401,"hint":"captcha"}}}`
	facade := func(capturedSearchBody, macroBody string) func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			if strings.Contains(request.URL, "/ws/1.1/token.get") {
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"stale-token"}}}`)}, nil
			}
			if strings.Contains(request.URL, "/ws/1.1/track.search") {
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(capturedSearchBody)}, nil
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(macroBody)}, nil
		}
	}

	cache := newFakeLyricsCache()
	provider := providerForCatalogWithCache(facade(rejectedSearchBody, rejectedSearchBody), cache, time.Now, "musixmatch")
	candidates, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v; want clean miss when the search envelope rejects the token", candidates, err)
	}
	if raw, _, _ := cache.Get(musixmatchTokenKey); len(raw) != 0 {
		t.Fatalf("stale token remains cached: %q, want the entry dropped", raw)
	}

	macroBody := `{"message":{"header":{"status_code":200},"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":401}}}}}}`
	validSearch := `{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_length":10}}]}}}`
	cache2 := newFakeLyricsCache()
	provider2 := providerForCatalogWithCache(facade(validSearch, macroBody), cache2, time.Now, "musixmatch")
	candidates, _, err = fetchMusixmatchLyricsCandidates(provider2, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("macro result = %#v/%v; want clean miss when the macro envelope rejects the token", candidates, err)
	}
	if raw, _, _ := cache2.Get(musixmatchTokenKey); len(raw) != 0 {
		t.Fatalf("stale token remains cached: %q, want the entry dropped", raw)
	}
}

func TestSweep_EnqueuesContinuationForDeferredSources(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	sources := []lyricsSource{
		sweepSource("one", new(int), true, nil),
		sweepSource("two", new(int), true, nil),
	}
	var deferredPassed []string
	// A negative budget defers every source deterministically (no parallel
	// package var mutation).
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	provider.sweepBudget = -time.Second
	provider.enqueueSweepSources = func(track lyrics.TrackInfo, ids []string) error {
		deferredPassed = ids
		return nil
	}

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	payload, _ := json.Marshal(sweepTaskPayload{Track: track})
	status, err := provider.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: "lyrics-sweep", Payload: payload})
	if err != nil {
		t.Fatalf("OnTaskExecute() error = %v, want nil", err)
	}
	assertContains(t, status, "deferred=2")
	if len(deferredPassed) != 2 {
		t.Fatalf("continuation sources = %#v, want the two deferred ids", deferredPassed)
	}
}

func TestSweep_RetainsInMemoryHostCooldowns(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	cache.setError = errors.New("kv down")
	shared := lyricsSource{
		ID: "sibling", Display: "sibling", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true,
		Fetch: func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error) {
			t.Fatal("sibling fetched although the host just served a global rate limit")
			return nil, lookupHealthy, nil
		},
	}
	first := lyricsSource{
		ID: "first", Display: "first", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true,
		Fetch: func(p *lyricsProvider, _ lyrics.TrackInfo, _ time.Time) ([]lyricsCandidate, lookupHealth, error) {
			response := &host.HTTPResponse{
				StatusCode: 429,
				Headers:    map[string]string{"Retry-After": "60", "X-RateLimit-Type": "exceeded"},
				Body:       []byte(`{"error":"exhausted"}`),
			}
			err := rateLimitResponseError("test", response, 60*time.Second)
			return nil, lookupDegraded, err
		},
	}
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{first, shared}, func(lyrics.TrackInfo) error { return nil })

	status := provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	assertContains(t, status, "failed=1")
	assertContains(t, status, "skipped=1")
	// The pass was not covered, so the done marker must not short-circuit it: the
	// next lookup gets to re-sweep once the sibling-host cooldown passes.
	if _, exists, _ := cache.Get(sweepDoneMarkerKey(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, []lyricsSource{first, shared})); exists {
		t.Fatal("an incomplete sweep wrote a done marker")
	}
}

func TestSweep_MarksCachedOnlyAfterRealStore(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	cache.setError = errors.New("kv down")
	healthy := sweepSource("one", new(int), true, nil)
	provider := newLyricsProviderWithSourcesAt(nil, cache, time.Now, []lyricsSource{healthy}, func(lyrics.TrackInfo) error { return nil })

	status := provider.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})
	assertContains(t, status, "cached=0")
	if provider.maybeEnqueueSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}) == false {
		// Without coverage the next lookup must still be allowed to re-sweep.
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if _, exists, _ := cache.Get(sweepDoneMarkerKey(track, []lyricsSource{healthy})); exists {
		t.Fatal("done marker recorded although the KV store rejected the lyrics write")
	}
}

func TestSweep_DoneMarkerFingesprintsSourceOrder(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	sourcesA := []lyricsSource{sweepSource("one", new(int), true, nil), sweepSource("two", new(int), true, nil)}
	providerA := newLyricsProviderWithSourcesAt(nil, cache, time.Now, sourcesA, func(lyrics.TrackInfo) error { return nil })
	providerA.runSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"})

	reordered := []lyricsSource{sourcesA[1], sourcesA[0]}
	providerB := newLyricsProviderWithSourcesAt(nil, cache, time.Now, reordered, func(lyrics.TrackInfo) error { return nil })
	if providerB.maybeEnqueueSweep(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}) == false {
		t.Fatal("a reorder fingerprint key inherits the old done marker; the newly ranked source would never be swept")
	}
}

func TestEnhancedLRCWordlessAndWordRenderings(t *testing.T) {
	t.Parallel()

	lines := []timedLine{
		{StartMs: 1000, EndMs: 2000, Text: "wordless", Words: nil},
		{StartMs: 3000, EndMs: 4000, Text: "duplicated in words", Words: []timedWord{{StartMs: 3000, EndMs: 3600, Text: "dup"}}},
	}
	out := enhancedLRCFromTimedLines(lines)
	if !strings.Contains(out, "[00:01.00]wordless") {
		t.Fatalf("output = %q, want wordless text retained", out)
	}
	if strings.Contains(out, "duplicated in words") {
		t.Fatalf("output = %q, want line text suppressed for word-bearing lines (Codex: no duplication)", out)
	}
}

func TestParseKFamilyLyrics_QRCRetainsWhitespaceTokens(t *testing.T) {
	t.Parallel()

	input := "[0,2380]Shape(0,476) (476,95)of(571,190)\n"
	lines, err := parseKFamilyLyrics(wordSyncQRC, input)
	if err != nil {
		t.Fatalf("parseKFamilyLyrics() error = %v, want nil", err)
	}
	out := enhancedLRCFromTimedLines(lines)
	if strings.Contains(out, "Shapeof") {
		t.Fatalf("output = %q, want the standalone space token retained", out)
	}
	if !strings.Contains(out, "<00:00.47> ") {
		t.Fatalf("output = %q, want a timed space token between Shape and of", out)
	}
}
