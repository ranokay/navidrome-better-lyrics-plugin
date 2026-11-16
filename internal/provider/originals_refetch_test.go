package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func deleteOriginalArtifacts(cache *fakeLyricsCache) {
	for key := range cache.values {
		if strings.HasPrefix(key, lyricsOriginalCacheKeyPrefix) {
			delete(cache.values, key)
		}
	}
}

func TestProvider_OriginalRefetchAppliesFailureBackoff(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	now := time.Unix(1_800_000_000, 0)
	fail := false
	fetches := 0
	src := source.Source{
		ID: "flaky", Display: "Flaky", Hosts: []string{"flaky.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			fetches++
			if fail {
				return nil, source.Degraded, errors.New("upstream unavailable")
			}
			return []lyric.Candidate{originalCandidate("lrc", "[00:01.00]line", "lrc", []byte("[00:01.00]line"), "rec-1")}, source.Healthy, nil
		},
	}
	p := newTestProviderWithDeps(nil, cache, func() time.Time { return now }, nil, nil)
	p.sources = []source.Source{src}
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	listing := p.GetLyricsVariants(track)
	if len(listing) != 1 {
		t.Fatalf("listing = %#v", listing)
	}
	identity := listing[0].Identity
	deleteOriginalArtifacts(cache)

	fail = true
	before := fetches
	if document, reason := p.GetLyricsOriginal(track, identity); reason == "" || len(document.Bytes) != 0 {
		t.Fatalf("first failed refetch = %#v/%q, want unavailable", document, reason)
	}
	if fetches != before+1 {
		t.Fatalf("fetches = %d, want one failed attempt", fetches)
	}
	if p.sourceFailureBackoff("flaky") <= 0 {
		t.Fatal("a failed retrieval must persist the source-failure backoff")
	}
	before = fetches
	if _, reason := p.GetLyricsOriginal(track, identity); reason != "source is cooling down" {
		t.Fatalf("suppressed reason = %q, want the cooling-down reason", reason)
	}
	if fetches != before {
		t.Fatal("a backed-off source must not be fetched again")
	}

	now = now.Add(6 * time.Second)
	fail = false
	document, reason := p.GetLyricsOriginal(track, identity)
	if reason != "" || string(document.Bytes) != "[00:01.00]line" {
		t.Fatalf("recovered refetch = %#v/%q, want the exact document", document, reason)
	}
	if p.sourceFailureBackoff("flaky") != 0 {
		t.Fatal("a healthy refetch must reset the failure backoff")
	}

	deleteOriginalArtifacts(cache)
	before = fetches
	if document, reason := p.GetLyricsOriginal(track, identity); reason != "" || string(document.Bytes) != "[00:01.00]line" {
		t.Fatalf("post-recovery refetch = %#v/%q", document, reason)
	}
	if fetches != before+1 {
		t.Fatal("recovery must allow another refetch without suppression")
	}
}

func TestProvider_OriginalRefetchBudgetDoesNotPersistFailure(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	budgetError := false
	fetches := 0
	src := source.Source{
		ID: "budgeted", Display: "Budgeted", Hosts: []string{"budgeted.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			fetches++
			if budgetError {
				return nil, source.Degraded, source.ErrLookupBudgetExhausted
			}
			return []lyric.Candidate{originalCandidate("lrc", "[00:01.00]line", "lrc", []byte("[00:01.00]line"), "rec-1")}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	identity := p.GetLyricsVariants(track)[0].Identity
	deleteOriginalArtifacts(cache)

	budgetError = true
	before := fetches
	if _, reason := p.GetLyricsOriginal(track, identity); reason == "" {
		t.Fatal("a budget timeout must report unavailable")
	}
	if p.sourceFailureBackoff("budgeted") != 0 {
		t.Fatal("a budget timeout must not persist an outage backoff")
	}
	if _, reason := p.GetLyricsOriginal(track, identity); reason == "" || fetches != before+2 {
		t.Fatalf("retry fetches = %d, want a fresh bounded attempt", fetches)
	}
}

func TestProvider_OriginalRefetchRespectsCooldown(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	fetches := 0
	src := source.Source{
		ID: "cooled", Display: "Cooled", Hosts: []string{"cooled.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			fetches++
			return []lyric.Candidate{originalCandidate("lrc", "[00:01.00]line", "lrc", []byte("[00:01.00]line"), "rec-1")}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	identity := p.GetLyricsVariants(track)[0].Identity
	deleteOriginalArtifacts(cache)
	p.RememberCooldown(source.GlobalCooldownKey("cooled"), time.Hour)

	before := fetches
	if _, reason := p.GetLyricsOriginal(track, identity); reason != "source is cooling down" {
		t.Fatalf("reason = %q, want the persisted cooldown", reason)
	}
	if fetches != before {
		t.Fatal("a cooled-down source must not be fetched")
	}
}

func TestProvider_BundleKeepsPerCandidateExpiry(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	now := time.Unix(1_800_000_000, 0)
	src := source.Source{
		ID: "expiring", Display: "Expiring", Hosts: []string{"expiring.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{
				originalCandidate("lrc", "[00:01.00]synced", "lrc", []byte("[00:01.00]synced"), "rec-lrc"),
				originalCandidate("plain", "plain", "plain", []byte("plain"), "rec-plain"),
			}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{src}, nil)
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	if listing := p.GetLyricsVariants(track); len(listing) != 2 {
		t.Fatalf("initial listing = %#v, want both representations", listing)
	}

	now = now.Add(2 * time.Hour)
	listing := p.GetLyricsVariants(track)
	if len(listing) != 1 || listing[0].Format != "lrc" {
		t.Fatalf("after the plain TTL = %#v, want the synchronized representation only", listing)
	}
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range v2.Entries {
		if entry.Identity != nil && entry.Identity.Format == "plain" {
			t.Fatal("an expired plain representation must not remain addressable")
		}
	}
}

func TestProvider_VariantResponseAndListingShareTheBound(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	candidates := make([]lyric.Candidate, 0, MaxListedVariants+6)
	for index := 0; index < MaxListedVariants+6; index++ {
		candidates = append(candidates, originalCandidate(
			"lrc", fmt.Sprintf("[00:01.00]line %d", index), "lrc",
			[]byte(fmt.Sprintf("[00:01.00]line %d", index)), fmt.Sprintf("rec-%d", index),
		))
	}
	src := source.Source{
		ID: "many", Display: "Many", Hosts: []string{"many.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return candidates, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatal(err)
	}
	if len(v2.Entries) != MaxListedVariants {
		t.Fatalf("v2 entries = %d, want the shared bound %d", len(v2.Entries), MaxListedVariants)
	}
	listing := p.GetLyricsVariants(track)
	if len(listing) != MaxListedVariants {
		t.Fatalf("listing = %d, want the shared bound %d", len(listing), MaxListedVariants)
	}
	for index, summary := range listing {
		if v2.Entries[index].Identity == nil || !summary.Identity.Equal(*v2.Entries[index].Identity) {
			t.Fatalf("listing identity %d does not match playback", index)
		}
	}
}

func TestProvider_ProviderRecordingRevisionRejectsChangedContent(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	document := []byte("[00:01.00]old")
	fetches := 0
	src := source.Source{
		ID: "revised", Display: "Revised", Hosts: []string{"revised.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			fetches++
			return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: string(document)}, "Same Provider", "lrc").
				WithOriginal("lrc", lyric.OriginalSupplied, document).
				WithRecording("rec-1")}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	identity := p.GetLyricsVariants(track)[0].Identity
	if !strings.HasPrefix(identity.Recording, "rec-1@") {
		t.Fatalf("recording = %q, want the provider id with a revision digest", identity.Recording)
	}
	deleteOriginalArtifacts(cache)

	document = []byte("[00:01.00]new")
	before := fetches
	got, reason := p.GetLyricsOriginal(track, identity)
	if reason == "" || len(got.Bytes) != 0 {
		t.Fatalf("changed provider document = %#v/%q, want unavailable with no replacement", got, reason)
	}
	if fetches != before+1 {
		t.Fatalf("fetches = %d, want one refetch attempt", fetches)
	}
}

func TestProvider_InstanceSaltReadErrorDoesNotOverwrite(t *testing.T) {
	t.Parallel()

	persisted := []byte("persisted-salt-0123456789abcdef")
	cache := newFakeLyricsCache()
	cache.values[instanceSaltKey] = persisted
	cache.getError = errors.New("salt read unavailable")
	p := newTestProviderWithSources(nil, cache, time.Now, nil, nil)
	first := p.sourceInstanceID("read-error")
	if first == "" {
		t.Fatal("a read-error salt must still yield a per-process identity")
	}
	if string(cache.values[instanceSaltKey]) != string(persisted) {
		t.Fatal("a salt read error must not overwrite the persisted installation salt")
	}
	if _, wrote := cache.ttlsForKey[instanceSaltKey]; wrote {
		t.Fatal("a salt read error must not write a replacement salt")
	}

	recovered := newFakeLyricsCache()
	recovered.values[instanceSaltKey] = persisted
	recoveredProvider := newTestProviderWithSources(nil, recovered, time.Now, nil, nil)
	if recoveredProvider.sourceInstanceID("read-error") == first {
		t.Fatal("a recovered persisted salt must produce a different instance than the per-process fallback")
	}
}
