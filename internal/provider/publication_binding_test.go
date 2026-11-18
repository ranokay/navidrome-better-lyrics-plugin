package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func publicationStateFor(t *testing.T, p *Provider, track lyrics.TrackInfo, generation, revision string, complete bool) []byte {
	t.Helper()
	scope, ok := p.publicationScope(track, p.activeSources())
	if !ok {
		t.Fatal("publication scope unavailable")
	}
	value, err := json.Marshal(discoveryStateWire{
		Handled: true, Generation: generation, Scope: scope,
		Revision: revision, Complete: complete,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPublishedRevisionOmittedWhenWinnerChangesBeforeStateRead(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	sources := []source.Source{{ID: "one", Display: "One"}}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, sources, nil)
	p.cacheWinner(track, sources, lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]old"}, "one", "lrc"), true)

	publisher := &fakePublisher{
		handled: true, accepted: true, token: "token", cache: cache,
		current: publicationStateFor(t, p, track, "generation", "revision-1", true),
	}
	publisher.onCurrent = func(string) {
		// A commit lands after the cached winner was read but before the
		// state lookup, replacing both the winner and the state revision.
		publisher.onCurrent = nil
		p.cacheWinner(track, sources, lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]new"}, "one", "lrc"), true)
		publisher.current = publicationStateFor(t, p, track, "generation", "revision-2", true)
	}
	p.publisher = publisher
	p.pubMeta = Publication{Generation: "generation"}

	_, discovery, ok := p.currentV2Response(track)
	if !ok {
		t.Fatal("cached response was not built")
	}
	if discovery.Revision != "" {
		t.Fatalf("discovery revision = %q, want no claim for an uncoupled snapshot", discovery.Revision)
	}
}

func TestPublishedRevisionOmittedWhenStateChangesDuringRead(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	sources := []source.Source{{ID: "one", Display: "One"}}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, sources, nil)
	p.cacheWinner(track, sources, lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "one", "lrc"), true)
	publisher := &fakePublisher{
		handled: true, accepted: true, token: "token", cache: cache,
		currentSeq: [][]byte{
			publicationStateFor(t, p, track, "generation", "revision-1", true),
			publicationStateFor(t, p, track, "generation", "revision-2", true),
		},
	}
	p.publisher = publisher
	p.pubMeta = Publication{Generation: "generation"}

	_, discovery, ok := p.currentV2Response(track)
	if !ok {
		t.Fatal("cached response was not built")
	}
	if discovery.Revision != "" {
		t.Fatalf("discovery revision = %q, want no claim when the state moved during the read", discovery.Revision)
	}
}

func TestPublishedCommitCannotForceRevisionOntoUncoupledResponse(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	src := source.Source{
		ID: "one", Display: "One", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{lrcCandidate(false)}, source.Healthy, nil
		},
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	// The post-commit state read is unstable: a commit lands between the two
	// state reads, so the committed revision must not be copied onto the
	// response built from the earlier view.
	publisher.onCommit = func(commitWire) {
		publisher.currentSeq = [][]byte{
			publicationStateFor(t, p, track, "generation", "revision-1", true),
			publicationStateFor(t, p, track, "generation", "revision-2", true),
		}
	}
	p.publisher = publisher

	response, err := p.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: track},
		Publication{Generation: "generation", CallID: "call"}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.commits) != 1 {
		t.Fatalf("commits = %d, want one", len(publisher.commits))
	}
	var batch commitWire
	if err := json.Unmarshal(publisher.commits[0], &batch); err != nil || batch.Revision == "" {
		t.Fatalf("commit batch = %s, want a committed revision", publisher.commits[0])
	}
	if response.Discovery.Revision != "" {
		t.Fatalf("response revision = %q, want the forced claim removed", response.Discovery.Revision)
	}
}

func TestPublishedStateRequiresGenerationAndScope(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	sources := []source.Source{{ID: "one", Display: "One"}}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, sources, nil)
	p.pubMeta = Publication{Generation: "generation"}
	publisher := &fakePublisher{handled: true, accepted: true, token: "token", cache: cache}
	p.publisher = publisher

	publisher.current = publicationStateFor(t, p, track, "generation", "revision", true)
	if _, ok := p.currentState(track); !ok {
		t.Fatal("matching generation and scope were rejected")
	}

	publisher.current = publicationStateFor(t, p, track, "other-generation", "revision", true)
	if _, ok := p.currentState(track); ok {
		t.Fatal("state from another generation was accepted")
	}

	publisher.current = publicationStateFor(t, p, track, "generation", "revision", true)
	p.sources = append(sources, source.Source{ID: "two", Display: "Two"})
	if _, ok := p.currentState(track); ok {
		t.Fatal("state written under a different source/settings scope was accepted")
	}
}

func TestPublishedSnapshotExpiryFollowsReusedDependencies(t *testing.T) {
	t.Parallel()
	now := time.Unix(10_000, 0)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	sources := []source.Source{{ID: "one", Display: "One"}, {ID: "two", Display: "Two"}}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, sources, nil)

	winnerKey, _ := lyricsWinnerCacheKey(track, source.IDs(sources))
	sourceKey, _ := lyricsCacheKeyForSource(track, "one")
	variantKey, _ := lyricsVariantCacheKey(track, "two")
	writeEnvelope := func(key string, expiresAt int64) {
		value, err := json.Marshal(cachedLyricsEnvelope{ExpiresAt: expiresAt})
		if err != nil {
			t.Fatal(err)
		}
		if err := cache.SetWithTTL(key, value, expiresAt-now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	writeEnvelope(winnerKey, now.Add(300*time.Second).Unix())
	writeEnvelope(sourceKey, now.Add(120*time.Second).Unix())
	bundle, err := json.Marshal(cachedSourceBundle{ExpiresAt: now.Add(240 * time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(variantKey, bundle, 240); err != nil {
		t.Fatal(err)
	}

	if got, want := p.snapshotExpiry(track, sources), now.Add(120*time.Second).Unix(); got != want {
		t.Fatalf("snapshot expiry = %d, want the shortest reused dependency %d", got, want)
	}

	// A legacy dependency without a stamped expiry must expire the claim now
	// rather than let the published state outlive it.
	legacy, err := json.Marshal(cachedLyricsEnvelope{
		Response: lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: "legacy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(sourceKey, legacy, 300); err != nil {
		t.Fatal(err)
	}
	if got := p.snapshotExpiry(track, sources); got != now.Unix() {
		t.Fatalf("unbounded dependency expiry = %d, want %d", got, now.Unix())
	}
}

func TestPublishedCompletionOmittedWhenCoverageExpired(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}
	src := source.Source{
		ID: "one", Display: "One", Hosts: []string{"one.example.test"}, DefaultEnabled: true,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return nil, source.Healthy, nil
		},
	}
	cache := newFakeLyricsCache()
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	// The winner remembers full coverage, but the per-source coverage row is
	// gone, so the cached view is genuinely incomplete.
	p.cacheWinner(track, []source.Source{src}, lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "one", "lrc"), true)
	p.publisher = &fakePublisher{
		handled: true, accepted: true, token: "token", cache: cache,
		current: publicationStateFor(t, p, track, "generation", "revision", true),
	}
	p.pubMeta = Publication{Generation: "generation"}

	_, discovery, ok := p.currentV2Response(track)
	if !ok {
		t.Fatal("cached response was not built")
	}
	if discovery.Revision != "" || discovery.Complete {
		t.Fatalf("discovery = %#v, want no claim when coverage expired", discovery)
	}
}
