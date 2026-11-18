package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func emptySources(ids ...string) []source.Source {
	sources := make([]source.Source, 0, len(ids))
	for _, id := range ids {
		sources = append(sources, source.Source{
			ID: id, Display: id, Hosts: []string{id + ".example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Healthy, nil
			},
		})
	}
	return sources
}

func TestRefreshTailStartsAfterForegroundCommit(t *testing.T) {
	t.Parallel()
	now := time.Unix(50_000, 0)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	var events []string
	var tail sweepTaskPayload
	enqueued := false
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, emptySources("one", "two", "three", "four", "five"), nil)
	missKey, ok := p.sourceMissKey(track, "four")
	if !ok {
		t.Fatal("source miss key unavailable")
	}
	miss, err := json.Marshal(cachedLyricsEnvelope{ExpiresAt: now.Add(120 * time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(missKey, miss, 120); err != nil {
		t.Fatal(err)
	}
	p.enqueueSweepSources = func(track lyrics.TrackInfo, ids []string, stalled int) error {
		enqueued = true
		events = append(events, "enqueue")
		tail = sweepTaskPayload{Track: track, Sources: ids, Publication: p.enqueueMeta, Refresh: p.enqueueRefresh}
		return nil
	}
	publisher := &fakePublisher{
		handled: true, accepted: true, token: "token",
		onCommit: func(commit commitWire) {
			events = append(events, "commit")
			if !commit.AwaitTail {
				t.Error("foreground commit did not await the refresh tail")
			}
			if commit.SnapshotExpiresAt != now.Add(120*time.Second).Unix() {
				t.Errorf("snapshot expiry = %d, want the reused dependency deadline", commit.SnapshotExpiresAt)
			}
		},
	}
	p.publisher = publisher
	_, err = p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: track},
		Publication{Generation: "generation", CallID: "refresh", Token: "token", Kind: "refresh"}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !enqueued || len(tail.Sources) != 2 || tail.Publication.Token != "token" || tail.Publication.Kind != "tail" {
		t.Fatalf("tail payload = %#v, want two deferred sources carrying the refresh token", tail)
	}
	if len(events) != 2 || events[0] != "commit" || events[1] != "enqueue" {
		t.Fatalf("events = %v, want the foreground commit before the tail enqueue", events)
	}
}

func TestSecondRefreshReplacesQueuedTailMarker(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	enqueued := false
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, emptySources("one", "two", "three", "four", "five"), nil)
	p.enqueueSweepSources = func(lyrics.TrackInfo, []string, int) error {
		enqueued = true
		return nil
	}
	p.publisher = &fakePublisher{handled: true, accepted: true, token: "token"}
	marker, ok := p.refreshTailMarkerKey(track)
	if !ok {
		t.Fatal("refresh tail marker key unavailable")
	}
	if err := p.storeSweepMarker(marker, 1); err != nil {
		t.Fatal(err)
	}
	cache := p.cache.(*fakeLyricsCache)
	if cache.ttlsForKey[marker] != 1 {
		t.Fatalf("stale marker ttl = %d, want 1", cache.ttlsForKey[marker])
	}
	if _, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: track},
		Publication{Generation: "generation", CallID: "refresh", Token: "token", Kind: "refresh"}, true,
	); err != nil {
		t.Fatal(err)
	}
	if !enqueued {
		t.Fatal("a second refresh skipped replacing the stale tail marker")
	}
	if cache.ttlsForKey[marker] != sweepQueuedMarkerTTL {
		t.Fatalf("tail marker ttl = %d, want the queued replacement %d", cache.ttlsForKey[marker], sweepQueuedMarkerTTL)
	}
}

func TestZeroWriteRefreshReleasesAdmission(t *testing.T) {
	t.Parallel()
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, nil, nil)
	p.sources = []source.Source{}
	publisher := &fakePublisher{handled: true, accepted: true, token: "token"}
	p.publisher = publisher
	if _, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}},
		Publication{Generation: "generation", CallID: "refresh", Token: "token", Kind: "refresh"}, true,
	); err != nil {
		t.Fatal(err)
	}
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the terminal zero-write refresh released", publisher.releases)
	}
}

func TestTerminalRefreshTaskReleasesAdmission(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
		Publication: Publication{Generation: "generation", Token: "token", Kind: "tail"},
		Refresh:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		{ID: "one", Display: "One", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
			}},
	}, nil)
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	status, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "tail", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if status == "stale publication" {
		t.Fatalf("tail task status = %q", status)
	}
	if len(publisher.commits) != 1 || publisher.releases != 1 {
		t.Fatalf("commits=%d releases=%d, want the terminal tail committed and released", len(publisher.commits), publisher.releases)
	}
}

func TestDeferredTailKeepsAdmissionUntilContinuationFinishes(t *testing.T) {
	t.Parallel()
	var continuation sweepTaskPayload
	enqueued := false
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, []source.Source{
		variantSource("one", "[00:01.00]one", "lrc"),
		variantSource("two", "[00:01.00]two", "lrc"),
	}, nil)
	p.enqueueSweepSources = func(track lyrics.TrackInfo, ids []string, stalled int) error {
		enqueued = true
		continuation = sweepTaskPayload{Track: track, Sources: ids, Publication: p.enqueueMeta, Refresh: p.enqueueRefresh}
		return nil
	}
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: p.cache}
	p.publisher = publisher
	p.sweepBudget = -time.Second
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
		Publication: Publication{Generation: "generation", Token: "token", Kind: "tail"},
		Refresh:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "tail", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if !enqueued || publisher.releases != 0 {
		t.Fatalf("enqueued=%v releases=%d, want the continuation to keep the admission", enqueued, publisher.releases)
	}
	p.sweepBudget = 24 * time.Second
	continuationPayload, err := json.Marshal(continuation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "tail-2", Payload: continuationPayload}); err != nil {
		t.Fatal(err)
	}
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the terminal continuation released", publisher.releases)
	}
}

func TestRefreshTailEnqueueFailureLogsAndReleases(t *testing.T) {
	t.Parallel()
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, emptySources("one", "two", "three", "four", "five"), nil)
	var logs []string
	p.logf = func(message string) { logs = append(logs, message) }
	p.enqueueSweepSources = func(lyrics.TrackInfo, []string, int) error {
		return errors.New("queue unavailable")
	}
	publisher := &fakePublisher{handled: true, accepted: true, token: "token"}
	p.publisher = publisher
	if _, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}},
		Publication{Generation: "generation", CallID: "refresh", Token: "token", Kind: "refresh"}, true,
	); err != nil {
		t.Fatal(err)
	}
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the failed tail admission released", publisher.releases)
	}
	found := false
	for _, message := range logs {
		if strings.Contains(message, "refresh tail enqueue failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("logs = %v, want the tail enqueue failure diagnosed", logs)
	}
}

func TestRetainedTaskProbeFailureFencesAndLogs(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(sweepTaskPayload{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatal(err)
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]one", "lrc"),
	}, nil)
	var logs []string
	p.logf = func(message string) { logs = append(logs, message) }
	p.publisher = &fakePublisher{handled: true, probeError: errors.New("kv unavailable")}
	status, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "old", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if status != "unadmitted task" {
		t.Fatalf("status = %q, want the probe failure fenced", status)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "publication probe failed") {
		t.Fatalf("logs = %v, want the probe failure diagnosed", logs)
	}
	if key, ok := lyricsCacheKeyForSource(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, "one"); ok {
		if _, exists, err := cache.Get(key); err != nil || exists {
			t.Fatalf("probe failure wrote source data: %v, %v", exists, err)
		}
	}
}

func TestRetainedPreUpgradeTaskDoesNotWriteOnCapableHost(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(sweepTaskPayload{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatal(err)
	}
	capacity := true
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", probe: &capacity}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]one", "lrc"),
	}, nil)
	p.publisher = publisher
	status, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "old", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if status != "unadmitted task" {
		t.Fatalf("status = %q, want the retained task fenced", status)
	}
	if sourceKey, ok := lyricsCacheKeyForSource(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, "one"); ok {
		if _, exists, err := cache.Get(sourceKey); err != nil || exists {
			t.Fatalf("unadmitted source entry = %v, %v; want no provider write", exists, err)
		}
	}
	if len(publisher.commits) != 0 {
		t.Fatal("an unadmitted retained task committed")
	}
}

func TestRetainedPreUpgradeTaskStaysCompatibleWithOldHosts(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(sweepTaskPayload{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err != nil {
		t.Fatal(err)
	}
	capacity := false
	publisher := &fakePublisher{handled: false, probe: &capacity}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]one", "lrc"),
	}, nil)
	p.publisher = publisher
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	status, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "old", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if status == "unadmitted task" {
		t.Fatal("an old host was treated as publication-capable")
	}
	key, _ := lyricsCacheKeyForSource(track, "one")
	if _, exists, err := cache.Get(key); err != nil || !exists {
		t.Fatalf("old-host sweep source entry = %v, %v; want the legacy direct write", exists, err)
	}
}

func TestStaleTaskCannotStompNewerMarker(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]one", "lrc"),
	}, nil)
	publisher := &fakePublisher{
		handled: true, accepted: true, token: "new", rejectTokens: map[string]bool{"old": true},
	}
	p.publisher = publisher
	markerKey, ok := lyricsTrackKey(track)
	if !ok {
		t.Fatal("track key unavailable")
	}
	marker := p.sweepPendingMarkerKey(track, markerKey)
	if err := p.storeSweepMarker(marker, 3600); err != nil {
		t.Fatal(err)
	}
	before, exists, err := cache.Get(marker)
	if err != nil || !exists {
		t.Fatal(err)
	}
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       track,
		Publication: Publication{Generation: "generation", Token: "old", Kind: "tail"},
		Refresh:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "stale", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if status != "stale publication" {
		t.Fatalf("status = %q, want the stale task rejected", status)
	}
	after, exists, err := cache.Get(marker)
	if err != nil || !exists || string(after) != string(before) {
		t.Fatalf("marker after stale task = %q, %v; want the newer marker preserved", after, err)
	}
	if len(publisher.commits) != 0 {
		t.Fatal("a stale task committed")
	}
}

func TestClassicLookupRevalidatesCachedDataAfterAdmission(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]unused", "lrc"),
	}, nil)
	oldCandidate := lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]old"}, "one", "lrc")
	newCandidate := lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]refreshed"}, "one", "lrc")
	p.cacheSourceCandidate(track, "one", oldCandidate)

	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	publisher.onAdmit = func(admission admissionWire) {
		if admission.Action == "admit" {
			// The refresh committed and released while this classic lookup
			// was paused between its cached read and its admission.
			p.cacheSourceCandidate(track, "one", newCandidate)
		}
	}
	p.publisher = publisher
	response, err := p.GetLyricsPublished(
		lyrics.GetLyricsRequest{Track: track},
		Publication{Generation: "generation", CallID: "classic"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Lyrics) == 0 || response.Lyrics[0].Text != newCandidate.Response.Lyrics[0].Text {
		t.Fatalf("classic response = %#v, want the post-refresh candidate", response.Lyrics)
	}
	if len(publisher.commits) != 1 {
		t.Fatalf("commits = %d, want one", len(publisher.commits))
	}
	var batch commitWire
	if err := json.Unmarshal(publisher.commits[0], &batch); err != nil {
		t.Fatal(err)
	}
	for _, write := range batch.Writes {
		if strings.Contains(string(write.Value), "[00:01.00]old") {
			t.Fatal("the classic publication captured pre-refresh cached data")
		}
	}
}

func TestBufferedCleanMissCompletesFirstSweepPass(t *testing.T) {
	t.Parallel()
	calls := map[string]int{}
	sources := []source.Source{
		{ID: "positive", Display: "positive", Hosts: []string{"positive.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				calls["positive"]++
				return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
			}},
		{ID: "empty", Display: "empty", Hosts: []string{"empty.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				calls["empty"]++
				return nil, source.Healthy, nil
			}},
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, sources, nil)
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
		Publication: Publication{Generation: "generation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "pass", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(publisher.commits) != 1 {
		t.Fatalf("commits = %d, want one", len(publisher.commits))
	}
	var batch commitWire
	if err := json.Unmarshal(publisher.commits[0], &batch); err != nil {
		t.Fatal(err)
	}
	if !batch.Complete {
		t.Fatal("a mixed positive/clean-miss sweep did not commit completion")
	}
	doneKey := p.doneMarkerKey(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, sources)
	found := false
	for _, write := range batch.Writes {
		if write.Key == doneKey {
			found = true
		}
	}
	if !found {
		t.Fatal("the done marker did not commit with the buffered miss")
	}
	if _, exists, err := cache.Get(doneKey); err != nil || !exists {
		t.Fatalf("done marker = %v, %v; want the committed marker", exists, err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "pass-2", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if calls["empty"] != 1 {
		t.Fatalf("clean-miss fetches = %d, want the committed miss reused on the next pass", calls["empty"])
	}
}

func TestFailedBufferedMissCannotClaimCompletion(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"rejected", "quota-limited"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sources := []source.Source{
				{ID: "positive", Display: "positive", Hosts: []string{"positive.example.test"}, DefaultEnabled: true,
					Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
						return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
					}},
				{ID: "empty", Display: "empty", Hosts: []string{"empty.example.test"}, DefaultEnabled: true,
					Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
						return nil, source.Healthy, nil
					}},
			}
			cache := newFakeLyricsCache()
			p := newTestProviderWithSources(nil, cache, time.Now, sources, nil)
			p.publisher = &fakePublisher{handled: true, accepted: true, token: "token", commitErr: errors.New(name), cache: cache}
			payload, err := json.Marshal(sweepTaskPayload{
				Track:       lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
				Publication: Publication{Generation: "generation", Token: "token"},
			})
			if err != nil {
				t.Fatal(err)
			}
			status, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "pass", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			if status == "" {
				t.Fatal("rejected publication reported success")
			}
			doneKey := p.doneMarkerKey(lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, sources)
			if _, exists, err := cache.Get(doneKey); err != nil || exists {
				t.Fatalf("done marker after rejected publication = %v, %v", exists, err)
			}
		})
	}
}

func TestClassicLookupDoesNotPublishDataClearedAfterAdmission(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]unused", "lrc"),
	}, nil)
	p.cacheSourceCandidate(track, "one", lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]obsolete"}, "one", "lrc"))
	sourceKey, ok := lyricsCacheKeyForSource(track, "one")
	if !ok {
		t.Fatal("source key unavailable")
	}

	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	publisher.onAdmit = func(admission admissionWire) {
		if admission.Action != "admit" {
			return
		}
		// The refresh committed a clean miss while this classic lookup was
		// paused between its cached read and admission.
		miss, err := json.Marshal(cachedLyricsEnvelope{
			Generation: lyricsCacheGeneration,
			ExpiresAt:  time.Now().Add(time.Minute).Unix(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := cache.SetWithTTL(sourceKey, miss, 60); err != nil {
			t.Fatal(err)
		}
	}
	p.publisher = publisher
	response, err := p.GetLyricsPublished(
		lyrics.GetLyricsRequest{Track: track},
		Publication{Generation: "generation", CallID: "classic"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range response.Lyrics {
		if strings.Contains(text.Text, "obsolete") {
			t.Fatal("the classic lookup served data cleared by the refresh")
		}
	}
	for _, commit := range publisher.commits {
		if strings.Contains(string(commit), "obsolete") {
			t.Fatal("the classic lookup published data cleared by the refresh")
		}
	}
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the ownerless classic admission released", publisher.releases)
	}
}

func TestForegroundRefusedWriteCannotClaimCompletion(t *testing.T) {
	t.Parallel()
	big := lyric.NewCandidate(lyrics.LyricsText{Text: strings.Repeat("x", MaxCacheEntryBytes+1)}, "big", "lrc")
	sources := []source.Source{
		{ID: "big", Display: "big", Hosts: []string{"big.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return []lyric.Candidate{big}, source.Healthy, nil
			}},
		variantSource("small", "[00:01.00]small", "lrc"),
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, sources, nil)
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	response, err := p.GetLyricsV2Published(lyrics.GetLyricsRequest{Track: track}, Publication{Generation: "generation", CallID: "call"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.commits) != 1 {
		t.Fatalf("commits = %d, want one", len(publisher.commits))
	}
	var batch commitWire
	if err := json.Unmarshal(publisher.commits[0], &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Complete || response.Discovery.Complete {
		t.Fatal("a refused source write claimed complete foreground coverage")
	}
	if key, ok := lyricsCacheKeyForSource(track, "big"); ok {
		if _, exists, err := cache.Get(key); err != nil || exists {
			t.Fatalf("refused source write = %v, %v; want no entry", exists, err)
		}
	}
}

func TestClassicCommitCarriesDependencyExpiry(t *testing.T) {
	t.Parallel()
	now := time.Unix(60_000, 0)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{
		variantSource("one", "[00:01.00]unused", "lrc"),
	}, nil)
	sourceKey, ok := lyricsCacheKeyForSource(track, "one")
	if !ok {
		t.Fatal("source key unavailable")
	}
	value, err := json.Marshal(cachedLyricsEnvelope{
		Generation:  lyricsCacheGeneration,
		Credentials: p.sourceCredentialsFingerprint("one"),
		Quality:     lyric.TimingLine,
		Response:    lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: "[00:01.00]cached"}}},
		ExpiresAt:   now.Add(120 * time.Second).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(sourceKey, value, 120); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	if _, err := p.GetLyricsPublished(lyrics.GetLyricsRequest{Track: track}, Publication{Generation: "generation", CallID: "classic"}); err != nil {
		t.Fatal(err)
	}
	if len(publisher.commits) != 1 {
		t.Fatalf("commits = %d, want one", len(publisher.commits))
	}
	var batch commitWire
	if err := json.Unmarshal(publisher.commits[0], &batch); err != nil {
		t.Fatal(err)
	}
	if batch.SnapshotExpiresAt != now.Add(120*time.Second).Unix() {
		t.Fatalf("snapshot expiry = %d, want the reused dependency deadline", batch.SnapshotExpiresAt)
	}
}

func TestNonRefreshSweepZeroWriteReleasesAdmission(t *testing.T) {
	t.Parallel()
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true},
	}, nil)
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
		Publication: Publication{Generation: "generation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "sweep", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the zero-write sweep released", publisher.releases)
	}
}

func TestFailedSweepCommitDropsContinuationAndMarkers(t *testing.T) {
	t.Parallel()
	current := time.Unix(70_000, 0)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	sources := []source.Source{
		{ID: "first", Display: "first", Hosts: []string{"first.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				current = current.Add(2 * time.Second)
				return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
			}},
		{ID: "second", Display: "second", Hosts: []string{"second.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
			}},
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, func() time.Time { return current }, sources, nil)
	p.sweepBudget = time.Second
	enqueued := false
	p.enqueueSweepSources = func(lyrics.TrackInfo, []string, int) error {
		enqueued = true
		return nil
	}
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", commitErr: errors.New("host rejected"), cache: cache}
	p.publisher = publisher
	tailMarker, ok := p.refreshTailMarkerKey(track)
	if !ok {
		t.Fatal("tail marker key unavailable")
	}
	if err := p.storeSweepMarker(tailMarker, 1800); err != nil {
		t.Fatal(err)
	}
	pendingKey, ok := lyricsTrackKey(track)
	if !ok {
		t.Fatal("track key unavailable")
	}
	pendingMarker := p.sweepPendingMarkerKey(track, pendingKey)
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       track,
		Publication: Publication{Generation: "generation", Token: "token", Kind: "tail"},
		Refresh:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "tail", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if enqueued {
		t.Fatal("a failed commit queued a continuation on its released token")
	}
	if cache.ttlsForKey[pendingMarker] != 1 {
		t.Fatalf("pending marker ttl = %d, want the failure released", cache.ttlsForKey[pendingMarker])
	}
	if cache.ttlsForKey[tailMarker] != 1 {
		t.Fatalf("tail marker ttl = %d, want the failure released", cache.ttlsForKey[tailMarker])
	}
}

func TestAllPositiveRefreshClearsStaleTailMarker(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]one", "lrc"),
	}, nil)
	p.cacheSourceCandidate(track, "one", lrcCandidate(false))
	marker, ok := p.refreshTailMarkerKey(track)
	if !ok {
		t.Fatal("tail marker key unavailable")
	}
	if err := p.storeSweepMarker(marker, 1800); err != nil {
		t.Fatal(err)
	}
	p.publisher = &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	if _, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: track},
		Publication{Generation: "generation", CallID: "refresh", Token: "token", Kind: "refresh"}, true,
	); err != nil {
		t.Fatal(err)
	}
	if cache.ttlsForKey[marker] != 1 {
		t.Fatalf("tail marker ttl = %d, want a tail-less refresh to retire it", cache.ttlsForKey[marker])
	}
}

func TestRefreshCachedPositiveRevalidatesAfterAdmission(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]unused", "lrc"),
	}, nil)
	old := lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]old"}, "one", "lrc")
	fresh := lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]<00:01.00>fresh"}, "one", "elrc")
	p.cacheSourceCandidate(track, "one", old)
	p.preparePublication(Publication{Generation: "generation", CallID: "refresh"}, "refresh")
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	publisher.onAdmit = func(admission admissionWire) {
		if admission.Action == "admit" {
			p.cacheSourceCandidate(track, "one", fresh)
		}
	}
	p.publisher = publisher
	candidate, _, _, _, err := p.resolveLyrics(lyrics.GetLyricsRequest{Track: track}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Response.Lyrics) == 0 || candidate.Response.Lyrics[0].Text != fresh.Response.Lyrics[0].Text {
		t.Fatalf("refresh candidate = %#v, want the post-admission positive", candidate.Response.Lyrics)
	}
	for _, write := range p.publication.writes {
		if strings.Contains(string(write.Value), "[00:01.00]old") {
			t.Fatal("refresh published a positive captured before admission")
		}
	}
}

func TestMintedTokenlessSweepZeroWriteReleasesAdmission(t *testing.T) {
	t.Parallel()
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true},
	}, nil)
	publisher := &fakePublisher{handled: true, accepted: true, token: "minted", cache: cache}
	p.publisher = publisher
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
		Publication: Publication{Generation: "generation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "sweep", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the minted zero-write admission released", publisher.releases)
	}
}

func TestDeniedPublicationRefusesDiscoveryWrites(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{}, nil)
	p.publisher = &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.preparePublication(Publication{Generation: "generation", CallID: "call"}, "normal")
	if err := p.ensurePublication(track); err != nil {
		t.Fatal(err)
	}
	if p.publication == nil || !p.publication.denied {
		t.Fatalf("publication = %#v, want a denied scope", p.publication)
	}
	key, ok := lyricsCacheKeyForSource(track, "one")
	if !ok {
		t.Fatal("source key unavailable")
	}
	if p.storeCandidateBytes(key, []byte("value"), false, 60) {
		t.Fatal("a denied publication wrote directly")
	}
	if _, exists, err := cache.Get(key); err != nil || exists {
		t.Fatalf("denied source write = %v, %v", exists, err)
	}
}

func TestRefreshHandsOffSweepWithoutParentToken(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	sources := []source.Source{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Degraded, errors.New("upstream down")
			}},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, sources, nil)
	enqueued := false
	p.enqueueSweep = func(lyrics.TrackInfo) error {
		enqueued = true
		return nil
	}
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	_, _ = p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: track},
		Publication{Generation: "generation", CallID: "refresh", Token: "token", Kind: "refresh"}, true,
	)
	if !enqueued {
		t.Fatal("the failed refresh did not queue a full sweep")
	}
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the terminal parent released", publisher.releases)
	}
	if p.enqueueMeta.Token != "" || p.enqueueMeta.Generation != "generation" {
		t.Fatalf("deferred sweep publication = %#v, want a generation-only child", p.enqueueMeta)
	}
}

func TestFailedSweepEnqueueDoesNotHoldAdmission(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		variantSource("one", "[00:01.00]one", "lrc"),
	}, nil)
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	p.preparePublication(Publication{Generation: "generation", Token: "token"}, "normal")
	if err := p.ensurePublication(track); err != nil {
		t.Fatal(err)
	}
	p.enqueueSweep = func(lyrics.TrackInfo) error { return errors.New("queue unavailable") }
	if !p.maybeEnqueueSweep(track) {
		t.Fatal("an active parent did not defer the sweep")
	}
	p.flushDeferredSweep()
	if p.enqueueMeta.Token != "" {
		t.Fatal("a failed enqueue left a live token in enqueueMeta")
	}
	if p.publication == nil || !p.publication.active {
		t.Fatal("the admitted publication disappeared")
	}
	// The zero-write terminal release must now be able to clear the admission.
	p.clearPublication()
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the ownerless admission released", publisher.releases)
	}
}

func TestStaleTaskClearsItsOwnOrphanedMarker(t *testing.T) {
	t.Parallel()
	for _, refresh := range []bool{false, true} {
		name := "sweep"
		if refresh {
			name = "tail"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			cache := newFakeLyricsCache()
			p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
				variantSource("one", "[00:01.00]one", "lrc"),
			}, nil)
			publisher := &fakePublisher{
				handled: true, accepted: true, token: "new", rejectTokens: map[string]bool{"old": true},
			}
			p.publisher = publisher
			markerID := "old-marker-" + name
			var markerKey string
			if refresh {
				markerKey, _ = p.refreshTailMarkerKey(track)
			} else {
				if trackKey, ok := lyricsTrackKey(track); ok {
					markerKey = p.sweepPendingMarkerKey(track, trackKey)
				}
			}
			if markerKey == "" {
				t.Fatal("marker key unavailable")
			}
			if err := p.storeOwnedSweepMarker(markerKey, 1800, markerID); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(sweepTaskPayload{
				Track:       track,
				Publication: Publication{Generation: "generation", Token: "old", Kind: name},
				Refresh:     refresh,
				MarkerID:    markerID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "stale", Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if p.sweepMarkerActive(markerKey) {
				t.Fatal("a stale task left its own 30-minute marker active")
			}
			enqueued := false
			p.enqueueSweep = func(lyrics.TrackInfo) error {
				enqueued = true
				return nil
			}
			if !p.maybeEnqueueSweep(track) || !enqueued {
				t.Fatal("a replacement sweep was suppressed by the orphaned marker")
			}
		})
	}
}

func TestForegroundHandoffEnqueuesSweepAfterCommit(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	var events []string
	cache := newFakeLyricsCache()
	sources := []source.Source{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
			}},
		{ID: "two", Display: "two", Hosts: []string{"two.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
			}},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, sources, nil)
	p.enqueueSweep = func(lyrics.TrackInfo) error {
		events = append(events, "enqueue")
		return nil
	}
	publisher := &fakePublisher{
		handled: true, accepted: true, token: "token", cache: cache,
		onCommit: func(commitWire) { events = append(events, "commit") },
	}
	p.publisher = publisher
	if _, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: track},
		Publication{Generation: "generation", CallID: "call"}, false,
	); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != "commit" || events[1] != "enqueue" {
		t.Fatalf("events = %v, want the foreground commit before the sweep enqueue", events)
	}
}

func TestScopedMarkerOwnershipProtectsNewerReplacement(t *testing.T) {
	t.Parallel()
	for _, refresh := range []bool{false, true} {
		name := "sweep"
		if refresh {
			name = "tail"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
			cache := listableCache{newFakeLyricsCache()}
			p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{
				variantSource("one", "[00:01.00]one", "lrc"),
			}, nil)
			p.publisher = &fakePublisher{
				handled: true, accepted: true, token: "new", rejectTokens: map[string]bool{"old": true},
			}
			var base string
			if refresh {
				base, _ = p.refreshTailMarkerKey(track)
			} else if trackKey, ok := lyricsTrackKey(track); ok {
				base = p.sweepPendingMarkerKey(track, trackKey)
			}
			if base == "" {
				t.Fatal("marker base unavailable")
			}
			oldKey := p.markerKey(base, "old")
			newKey := p.markerKey(base, "new")
			if err := p.storeOwnedSweepMarker(oldKey, 1800, "old"); err != nil {
				t.Fatal(err)
			}
			if err := p.storeOwnedSweepMarker(newKey, 1800, "new"); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(sweepTaskPayload{
				Track:       track,
				Publication: Publication{Generation: "generation", Token: "old", Kind: name},
				Refresh:     refresh,
				MarkerID:    "old",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "stale", Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if p.sweepMarkerActive(oldKey) {
				t.Fatal("a stale task left its own scoped marker active")
			}
			if !p.sweepMarkerActive(newKey) {
				t.Fatal("a stale task cleared the newer owner's scoped marker")
			}
		})
	}
}

func TestSupersededDuringIOOnlyClearsItsOwnMarker(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	cache := listableCache{newFakeLyricsCache()}
	var base string
	var p *Provider
	p = newTestProviderWithSources(nil, cache, time.Now, []source.Source{
		{ID: "one", Display: "one", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				if err := p.storeOwnedSweepMarker(p.markerKey(base, "new"), 1800, "new"); err != nil {
					t.Error(err)
				}
				return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
			}},
	}, nil)
	p.publisher = &fakePublisher{
		handled: true, accepted: true, token: "old", commitErr: errors.New("superseded"),
	}
	trackKey, _ := lyricsTrackKey(track)
	base = p.sweepPendingMarkerKey(track, trackKey)
	oldKey := p.markerKey(base, "old")
	newKey := p.markerKey(base, "new")
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       track,
		Publication: Publication{Generation: "generation", Kind: "sweep"},
		MarkerID:    "old",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "task", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if p.sweepMarkerActive(oldKey) {
		t.Fatal("a superseded task left its scoped marker active")
	}
	if !p.sweepMarkerActive(newKey) {
		t.Fatal("a superseded task cleared the newer marker")
	}
}
