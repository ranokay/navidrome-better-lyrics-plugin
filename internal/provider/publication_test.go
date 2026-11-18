package provider

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

type fakePublisher struct {
	handled      bool
	accepted     bool
	token        string
	active       string
	current      []byte
	states       map[string][]byte
	currentSeq   [][]byte
	onCurrent    func(trackKey string)
	commitErr    error
	commits      [][]byte
	admits       int
	releases     int
	probe        *bool
	probeError   error
	mergeErr     error
	reduceErr    error
	control      bool
	clearOnReset bool
	rejectTokens map[string]bool
	onAdmit      func(admissionWire)
	onCommit     func(commitWire)
	cache        Store
}

func (f *fakePublisher) Admit(trackKey string, request []byte) ([]byte, error) {
	f.admits++
	if !f.handled {
		return request, nil
	}
	var admission admissionWire
	if err := json.Unmarshal(request, &admission); err != nil {
		return nil, err
	}
	if f.onAdmit != nil {
		f.onAdmit(admission)
	}
	if admission.Action == "release" {
		f.releases++
	}
	token := f.token
	if token == "" {
		token = "token"
	}
	response := admissionResponseWire{
		Handled: true, Accepted: true,
		RequestID: admission.RequestID, Generation: admission.Generation, Token: token, State: "active",
	}
	response.Accepted = f.accepted
	if f.rejectTokens[admission.Token] {
		response.Accepted = false
	}
	return json.Marshal(response)
}

func (f *fakePublisher) Commit(trackKey string, request []byte) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	f.commits = append(f.commits, append([]byte(nil), request...))
	var commit commitWire
	if err := json.Unmarshal(request, &commit); err != nil {
		return err
	}
	if f.onCommit != nil {
		f.onCommit(commit)
	}
	if f.cache != nil {
		for _, write := range commit.Writes {
			if err := f.cache.SetWithTTL(write.Key, write.Value, write.TTL); err != nil {
				return err
			}
		}
	}
	state, _ := json.Marshal(discoveryStateWire{
		Handled: true, Generation: commit.Generation, Scope: commit.Scope,
		Revision: commit.Revision, Complete: commit.Complete,
	})
	f.current = state
	if f.states == nil {
		f.states = make(map[string][]byte)
	}
	f.states[commit.Track] = state
	return nil
}

func (f *fakePublisher) Current(trackKey string) ([]byte, bool, error) {
	if f.onCurrent != nil {
		f.onCurrent(trackKey)
	}
	if len(f.currentSeq) > 0 {
		value := f.currentSeq[0]
		f.currentSeq = f.currentSeq[1:]
		return value, value != nil, nil
	}
	if f.states != nil {
		if value, exists := f.states[trackKey]; exists {
			return value, true, nil
		}
	}
	if f.current == nil {
		return nil, false, nil
	}
	return f.current, true, nil
}

func (f *fakePublisher) Active() (string, bool, error) {
	return f.active, f.active != "", nil
}

func (f *fakePublisher) ControlMerge() (bool, error) {
	return f.control, nil
}

func (f *fakePublisher) ReduceFailure(sourceID, operation string, failures, until, updatedAt, resetAt, ttlSeconds int64) error {
	if f.reduceErr != nil {
		return f.reduceErr
	}
	if f.cache == nil {
		return nil
	}
	key := sourceFailureKey(sourceID)
	state := sourceFailureState{}
	if raw, exists, err := f.cache.Get(key); err == nil && exists {
		_ = json.Unmarshal(raw, &state)
	}
	switch operation {
	case "failure":
		if updatedAt < state.ResetAt {
			return nil
		}
		state.Failures = uint8(max(int(state.Failures), int(failures)))
		state.Until = max(state.Until, until)
		state.UpdatedAt = max(state.UpdatedAt, updatedAt)
	case "reset":
		if f.clearOnReset {
			state.ResetAt = resetAt
			state.Failures, state.Until, state.UpdatedAt = 0, 0, 0
			break
		}
		if resetAt > state.ResetAt {
			state.ResetAt = resetAt
			if state.UpdatedAt < resetAt {
				state.Failures, state.Until, state.UpdatedAt = 0, 0, 0
			}
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return f.cache.SetWithTTL(key, raw, ttlSeconds)
}

func (f *fakePublisher) MergeMax(key string, value, ttlSeconds int64) error {
	if f.mergeErr != nil {
		return f.mergeErr
	}
	if f.cache == nil {
		return nil
	}
	if raw, exists, err := f.cache.Get(key); err == nil && exists {
		if prior, parseErr := strconv.ParseInt(string(raw), 10, 64); parseErr == nil && prior > value {
			value = prior
		}
	}
	return f.cache.SetWithTTL(key, []byte(strconv.FormatInt(value, 10)), ttlSeconds)
}

func (f *fakePublisher) Probe() (bool, error) {
	if f.probeError != nil {
		return false, f.probeError
	}
	if f.probe != nil {
		return *f.probe, nil
	}
	return true, nil
}

func TestPublishedLyricsBufferDiscoveryWrites(t *testing.T) {
	t.Parallel()
	cache := newFakeLyricsCache()
	src := source.Source{
		ID: "only", Display: "Only", Hosts: []string{"only.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{ttmlCandidate("Word")}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, func(lyrics.TrackInfo) error { return nil })
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	response, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}},
		Publication{Generation: "generation", CallID: "call"}, false,
	)
	if err != nil {
		t.Fatalf("published lookup error = %v", err)
	}
	if response.Discovery.Revision == "" {
		t.Fatal("published lookup returned no revision")
	}
	if len(publisher.commits) != 1 {
		t.Fatalf("publication commits = %d, want one", len(publisher.commits))
	}
	var batch commitWire
	if err := json.Unmarshal(publisher.commits[0], &batch); err != nil || len(batch.Writes) == 0 {
		t.Fatalf("commit batch = %s", publisher.commits[0])
	}
	for key := range cache.values {
		if strings.HasPrefix(key, "lyrics:v5:") {
			return
		}
	}
	t.Fatal("committed batch did not apply source writes")
}

func TestRejectedClassicPublicationWritesNothing(t *testing.T) {
	t.Parallel()
	cache := newFakeLyricsCache()
	src := source.Source{
		ID: "only", Display: "Only", Hosts: []string{"only.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{ttmlCandidate("Word")}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, func(lyrics.TrackInfo) error { return nil })
	p.publisher = &fakePublisher{handled: true, accepted: true, token: "token", commitErr: errors.New("stale admission"), cache: cache}
	_, err := p.GetLyricsPublished(
		lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}},
		Publication{Generation: "generation", CallID: "call"},
	)
	if err == nil {
		t.Fatal("rejected classic publication did not return an error")
	}
	for key := range cache.values {
		if strings.HasPrefix(key, "lyrics:v5:") {
			t.Fatalf("rejected classic publication wrote %s", key)
		}
	}
}

func TestRefreshBypassesNegativeCoverageButHonorsCooldown(t *testing.T) {
	t.Parallel()
	calls := 0
	src := source.Source{
		ID: "only", Display: "Only", Hosts: []string{"only.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			calls++
			return []lyric.Candidate{ttmlCandidate("Word")}, source.Healthy, nil
		},
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, func(lyrics.TrackInfo) error { return nil })
	p.publisher = &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	// Seed a clean negative winner and source miss through a normal published
	// call while the source is temporarily empty.
	src.Fetch = func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
		calls++
		return nil, source.Healthy, nil
	}
	p.sources = []source.Source{src}
	if _, err := p.GetLyricsV2Published(lyrics.GetLyricsRequest{Track: track}, Publication{Generation: "generation", CallID: "seed"}, false); err != nil {
		t.Fatal(err)
	}
	seedCalls := calls

	src.Fetch = func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
		calls++
		return []lyric.Candidate{ttmlCandidate("Word")}, source.Healthy, nil
	}
	p.sources = []source.Source{src}
	if _, err := p.GetLyricsV2Published(lyrics.GetLyricsRequest{Track: track}, Publication{Generation: "generation", CallID: "refresh", Token: "token", Kind: "refresh"}, true); err != nil {
		t.Fatal(err)
	}
	if calls == seedCalls {
		t.Fatal("refresh did not retry negative coverage")
	}

	p.RememberCooldown(source.GlobalCooldownKey("only"), time.Minute)
	before := calls
	p.publication = nil
	if _, err := p.GetLyricsV2Published(lyrics.GetLyricsRequest{Track: track}, Publication{Generation: "generation", CallID: "refresh2", Token: "token", Kind: "refresh"}, true); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Fatal("refresh bypassed an active provider cooldown")
	}
}

func TestRefreshTailCarriesDeferredSources(t *testing.T) {
	t.Parallel()
	var payload sweepTaskPayload
	enqueued := false
	fullSweeps := 0
	sources := make([]source.Source, 0, 5)
	for _, id := range []string{"one", "two", "three", "four", "five"} {
		sources = append(sources, source.Source{
			ID: id, Display: id, Hosts: []string{id + ".example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Healthy, nil
			},
		})
	}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, func(lyrics.TrackInfo) error {
		fullSweeps++
		return nil
	})
	p.enqueueSweepSources = func(track lyrics.TrackInfo, ids []string, stalled int) error {
		enqueued = true
		payload = sweepTaskPayload{Track: track, Sources: ids, Publication: p.enqueueMeta, Refresh: p.enqueueRefresh}
		return nil
	}
	p.publisher = &fakePublisher{handled: true, accepted: true, token: "token"}
	_, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}},
		Publication{Generation: "generation", CallID: "refresh", Token: "token", Kind: "refresh"}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !enqueued || !payload.Refresh {
		t.Fatal("refresh tail was not enqueued as a revalidation task")
	}
	if len(payload.Sources) != 2 || payload.Publication.Token != "token" {
		t.Fatalf("tail payload = %#v, want the two deferred sources and the refresh token", payload)
	}
	if fullSweeps != 0 {
		t.Fatalf("full sweeps = %d, want the refresh tail to own the deferred coverage", fullSweeps)
	}
}

func TestCompletedMarkerOverridesStaleWinnerFlag(t *testing.T) {
	t.Parallel()
	cache := newFakeLyricsCache()
	sources := []source.Source{{
		ID: "only", Display: "Only", Hosts: []string{"only.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{ttmlCandidate("Word")}, source.Healthy, nil
		},
	}}
	p := newTestProviderWithSources(nil, cache, time.Now, sources, func(lyrics.TrackInfo) error { return nil })
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	candidate := ttmlCandidate("Line")
	p.cacheWinnerUntil(track, sources, candidate, false, time.Now().Add(time.Hour).Unix(), nil)
	_ = p.storeSweepMarker(p.doneMarkerKey(track, sources), 3600)
	_, _, _, completion, err := p.resolveLyrics(lyrics.GetLyricsRequest{Track: track}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !completion {
		t.Fatal("active completion marker did not override a stale winner flag")
	}
}

func TestOriginalRefetchFailureReleasesAdmission(t *testing.T) {
	t.Parallel()
	src := source.Source{
		ID: "only", Display: "Only", Hosts: []string{"only.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return nil, source.Degraded, errors.New("upstream down")
		},
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, func(lyrics.TrackInfo) error { return nil })
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	identity := lyric.NewVariantIdentity(p.sourceInstanceID("only"), "recording", "", "main", "elrc")
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	if _, reason := p.GetLyricsOriginalPublished(track, identity, Publication{Generation: "generation", CallID: "call"}); reason != "selection unavailable" {
		t.Fatalf("reason = %q", reason)
	}
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the failed refetch admission released", publisher.releases)
	}
}

func TestPublicationDigestGolden(t *testing.T) {
	t.Parallel()
	write := publicationWriteWire{Key: "lyrics:v5:source:lrclib:track-1", Value: []byte("snapshot"), TTL: 300}
	const expected = "e949adc58a9ca6d01ab4ea216421e10af619a14e2b2946153e7cda4937b3131b"
	if got := publicationDigest([]publicationWriteWire{write}, "track-1", true); got != expected {
		t.Fatalf("publicationDigest = %s, want %s", got, expected)
	}
}

func TestNegativeCoveragePreservesCompletion(t *testing.T) {
	t.Parallel()
	src := source.Source{
		ID: "only", Display: "Only", Hosts: []string{"only.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{{Evidence: []lyric.Evidence{{Kind: "instrumental"}}}}, source.Degraded, errors.New("assertion with outage")
		},
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, func(lyrics.TrackInfo) error { return nil })
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	_ = p.storeSweepMarker(p.doneMarkerKey(track, p.activeSources()), 3600)
	p.preparePublication(Publication{}, "legacy")
	if _, _, _, completion, _ := p.resolveLyrics(lyrics.GetLyricsRequest{Track: track}, false); !completion {
		t.Fatal("completed marker did not survive the evidence-negative path")
	}
	key, _ := lyricsWinnerCacheKey(track, source.IDs(p.activeSources()))
	raw, ok := cache.values[key]
	if !ok {
		t.Fatal("negative winner was not stored")
	}
	var envelope cachedLyricsEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.SweepComplete {
		t.Fatal("negative winner stored stale incomplete coverage")
	}
}

func TestOriginalRefetchWithoutWritesReleasesAdmission(t *testing.T) {
	t.Parallel()
	src := source.Source{
		ID: "only", Display: "Only", Hosts: []string{"only.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return nil, source.Healthy, nil
		},
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, func(lyrics.TrackInfo) error { return nil })
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher
	identity := lyric.NewVariantIdentity(p.sourceInstanceID("only"), "recording", "", "main", "elrc")
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	_, _ = p.GetLyricsOriginalPublished(track, identity, Publication{Generation: "generation", CallID: "call"})
	if publisher.releases != 1 {
		t.Fatalf("releases = %d, want the zero-write admission released", publisher.releases)
	}
}
