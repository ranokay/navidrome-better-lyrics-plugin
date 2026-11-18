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
	var events []string
	var tail sweepTaskPayload
	enqueued := false
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, emptySources("one", "two", "three", "four", "five"), nil)
	p.enqueueSweepSources = func(track lyrics.TrackInfo, ids []string, stalled int) error {
		enqueued = true
		events = append(events, "enqueue")
		tail = sweepTaskPayload{Track: track, Sources: ids, Publication: p.enqueueMeta, Refresh: p.enqueueRefresh}
		return nil
	}
	publisher := &fakePublisher{
		handled: true, accepted: true, token: "token",
		onCommit: func(commitWire) { events = append(events, "commit") },
	}
	p.publisher = publisher
	_, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}},
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
		Publication: Publication{Generation: "generation", Token: "token"},
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
