package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

type fakePublisher struct {
	handled   bool
	accepted  bool
	token     string
	active    string
	current   []byte
	commitErr error
	commits   [][]byte
	admits    int
	cache     Store
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
	token := f.token
	if token == "" {
		token = "token"
	}
	response := admissionResponseWire{
		Handled: true, Accepted: true,
		RequestID: admission.RequestID, Generation: admission.Generation, Token: token, State: "active",
	}
	if admission.Action == "admit" {
		response.Accepted = f.accepted
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
	if f.cache != nil {
		for _, write := range commit.Writes {
			if err := f.cache.SetWithTTL(write.Key, write.Value, write.TTL); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakePublisher) Current(trackKey string) ([]byte, bool, error) {
	if f.current == nil {
		return nil, false, nil
	}
	return f.current, true, nil
}

func (f *fakePublisher) Active() (string, bool, error) {
	return f.active, f.active != "", nil
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
	sources := make([]source.Source, 0, 5)
	for _, id := range []string{"one", "two", "three", "four", "five"} {
		sources = append(sources, source.Source{
			ID: id, Display: id, Hosts: []string{id + ".example.test"}, DefaultEnabled: true,
			Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
				return nil, source.Healthy, nil
			},
		})
	}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, func(lyrics.TrackInfo) error { return nil })
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
}
