package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

const testInstanceSalt = "0123456789abcdef0123456789abcdef"

func seededCache(t *testing.T) *fakeLyricsCache {
	t.Helper()
	cache := newFakeLyricsCache()
	if err := cache.SetWithTTL(instanceSaltKey, []byte(testInstanceSalt), 3600); err != nil {
		t.Fatalf("seed identity salt: %v", err)
	}
	return cache
}

func originalCandidate(format, text, nativeFormat string, native []byte, recording string) lyric.Candidate {
	return lyric.NewCandidate(lyrics.LyricsText{Text: text}, "Same Provider", format).
		WithOriginal(nativeFormat, lyric.OriginalSupplied, native).
		WithRecording(recording)
}

func listingByFormat(t *testing.T, p *Provider, track lyrics.TrackInfo, format string) VariantSummary {
	t.Helper()
	for _, summary := range p.GetLyricsVariants(track) {
		if summary.Format == format {
			return summary
		}
	}
	t.Fatalf("listing has no %q variant", format)
	return VariantSummary{}
}

func TestProvider_EveryAdapterCandidateIsAddressable(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 200}
	fetches := 0
	src := source.Source{
		ID: "same", Display: "Same", Hosts: []string{"same.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			fetches++
			return []lyric.Candidate{
				originalCandidate("lrc", "[00:01.00]line A", "lrc", []byte("[00:01.00]line A"), "rec-A"),
				originalCandidate("plain", "line B", "plain", []byte("line B"), "rec-B"),
			}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)

	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("GetLyrics() error = %v", err)
	}
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatalf("GetLyricsV2() error = %v", err)
	}
	if len(v2.Entries) != 2 {
		t.Fatalf("v2 entries = %#v, want both same-provider representations", v2.Entries)
	}
	if v2.Entries[0].Identity == nil || v2.Entries[1].Identity == nil {
		t.Fatalf("v2 entries lack identities: %#v", v2.Entries)
	}
	if v2.Entries[0].Identity.Equal(*v2.Entries[1].Identity) {
		t.Fatal("same-provider variants must have distinct identities")
	}
	if !strings.HasPrefix(v2.Entries[0].Identity.Recording, "rec-A@") || !strings.HasPrefix(v2.Entries[1].Identity.Recording, "rec-B@") {
		t.Fatalf("recordings = %q/%q, want provider ids with revision digests", v2.Entries[0].Identity.Recording, v2.Entries[1].Identity.Recording)
	}

	listing := p.GetLyricsVariants(track)
	if len(listing) != 2 {
		t.Fatalf("listing = %#v, want two addressable variants", listing)
	}
	for index, summary := range listing {
		if summary.PlaybackIndex != index {
			t.Fatalf("playbackIndex = %d at %d, want the playback entry index", summary.PlaybackIndex, index)
		}
		if !summary.Identity.Equal(*v2.Entries[index].Identity) {
			t.Fatalf("listing identity %d does not match playback identity", index)
		}
	}

	lrc := listing[0].Identity
	if listing[0].Format != "lrc" {
		lrc = listing[1].Identity
	}
	document, reason := p.GetLyricsOriginal(track, lrc)
	if reason != "" || string(document.Bytes) != "[00:01.00]line A" || document.Format != "lrc" {
		t.Fatalf("lrc original = %#v/%q, want the exact lrc document", document, reason)
	}
	plain := listingByFormat(t, p, track, "plain").Identity
	document, reason = p.GetLyricsOriginal(track, plain)
	if reason != "" || string(document.Bytes) != "line B" || document.Format != "plain" {
		t.Fatalf("plain original = %#v/%q, want the exact plain document", document, reason)
	}

	// The bundle keeps both candidates even though the legacy per-source entry
	// and the v1 response carry only the winner.
	key, _ := lyricsVariantCacheKey(track, "same")
	raw, exists, _ := cache.Get(key)
	if !exists {
		t.Fatal("variant bundle was not written")
	}
	var bundle cachedSourceBundle
	if err := json.Unmarshal(raw, &bundle); err != nil || len(bundle.Candidates) != 2 {
		t.Fatalf("bundle = %#v/%v, want both candidates", bundle, err)
	}
	if ttl := cache.ttlsForKey[key]; ttl != lyricsCacheDayTTLSeconds {
		t.Fatalf("bundle TTL = %d, want the longest candidate TTL (%d)", ttl, lyricsCacheDayTTLSeconds)
	}

	// Eviction refetches the same selection and preserves exact bytes.
	for key := range cache.values {
		if strings.HasPrefix(key, lyricsOriginalCacheKeyPrefix) {
			delete(cache.values, key)
		}
	}
	before := fetches
	document, reason = p.GetLyricsOriginal(track, lrc)
	if reason != "" || string(document.Bytes) != "[00:01.00]line A" {
		t.Fatalf("refetched original = %#v/%q, want the same selection", document, reason)
	}
	if fetches != before+1 {
		t.Fatalf("fetches = %d, want one bounded refetch", fetches)
	}
}

func TestProvider_RecordingIdentityRejectsChangedContentWithoutProviderID(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	document := []byte("[00:01.00]first document")
	fetches := 0
	src := source.Source{
		ID: "no-id", Display: "No ID", Hosts: []string{"noid.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			fetches++
			return []lyric.Candidate{lyric.NewCandidate(lyrics.LyricsText{Text: string(document)}, "Same Provider", "lrc").
				WithOriginal("lrc", lyric.OriginalSupplied, document)}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	listing := p.GetLyricsVariants(track)
	if len(listing) != 1 {
		t.Fatalf("listing = %#v", listing)
	}
	identity := listing[0].Identity
	if !strings.HasPrefix(identity.Recording, "sha256:") {
		t.Fatalf("recording = %q, want a content digest", identity.Recording)
	}
	if identity.Instance != p.sourceInstanceID("no-id") {
		t.Fatal("identity must be bound to the current source instance")
	}

	for key := range cache.values {
		if strings.HasPrefix(key, lyricsOriginalCacheKeyPrefix) {
			delete(cache.values, key)
		}
	}
	document = []byte("[00:01.00]different document")
	got, reason := p.GetLyricsOriginal(track, identity)
	if reason == "" || len(got.Bytes) != 0 {
		t.Fatalf("changed content = %#v/%q, want unavailable with no replacement", got, reason)
	}
	if fetches < 2 {
		t.Fatalf("fetches = %d, want a bounded refetch attempt", fetches)
	}
}

func TestProvider_SourceInstanceUses128BitCredentialSeparatedFingerprint(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	key := "secret-a"
	config := func(name string) (string, bool) {
		if name == "betterLyricsApiKey" {
			return key, true
		}
		return "", false
	}
	p := newTestProviderWithDeps(nil, cache, time.Now, config, nil)
	first := p.sourceInstanceID("better-lyrics")
	prefix, suffix, ok := strings.Cut(first, ":")
	if !ok || prefix != "better-lyrics" || len(suffix) != 32 {
		t.Fatalf("instance = %q, want a 128-bit (32 hex) suffix", first)
	}
	if strings.Contains(first, key) {
		t.Fatal("the credential must never appear in an instance id")
	}
	if again := p.sourceInstanceID("better-lyrics"); again != first {
		t.Fatalf("instance changed without a config change: %q != %q", again, first)
	}
	key = "secret-b"
	if second := p.sourceInstanceID("better-lyrics"); second == first {
		t.Fatal("a credential change must produce a new instance")
	}
	// A different installation salt must also separate instances.
	cache2 := newFakeLyricsCache()
	_ = cache2.SetWithTTL(instanceSaltKey, []byte("fedcba9876543210fedcba9876543210"), 3600)
	p2 := newTestProviderWithDeps(nil, cache2, time.Now, config, nil)
	if p2.sourceInstanceID("better-lyrics") == p.sourceInstanceID("better-lyrics") {
		t.Fatal("different installation salts must not share instance ids")
	}
}

func TestProvider_RetrievalIsBoundToTrackPluginAndCurrentInstance(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	key := "secret-a"
	config := func(name string) (string, bool) {
		if name == "betterLyricsApiKey" {
			return key, true
		}
		return "", false
	}
	src := source.Source{
		ID: "better-lyrics", Display: "Same", Hosts: []string{"bl.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{originalCandidate("lrc", "[00:01.00]line", "lrc", []byte("[00:01.00]line"), "rec-1")}, source.Healthy, nil
		},
	}
	p := newTestProviderWithDeps(nil, cache, time.Now, config, nil)
	p.sources = []source.Source{src}
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	listing := p.GetLyricsVariants(track)
	if len(listing) != 1 {
		t.Fatalf("listing = %#v", listing)
	}
	identity := listing[0].Identity

	key = "secret-b"
	if document, reason := p.GetLyricsOriginal(track, identity); reason == "" || len(document.Bytes) != 0 {
		t.Fatalf("stale instance = %#v/%q, want unavailable", document, reason)
	}
	if summaries := p.GetLyricsVariants(track); len(summaries) != 0 {
		t.Fatalf("listing = %#v, want no addressable stale variants", summaries)
	}

	otherTrack := lyrics.TrackInfo{Title: "Other", Artist: "Artist", Duration: 200}
	if document, reason := p.GetLyricsOriginal(otherTrack, identity); reason == "" || len(document.Bytes) != 0 {
		t.Fatalf("other track = %#v/%q, want unavailable", document, reason)
	}

	p.sources = []source.Source{{ID: "other", Display: "Other", Fetch: src.Fetch}}
	if document, reason := p.GetLyricsOriginal(track, identity); reason != "source is disabled" || len(document.Bytes) != 0 {
		t.Fatalf("disabled source = %#v/%q, want explicit unavailable", document, reason)
	}

	encoded, _ := json.Marshal(identity)
	if strings.Contains(string(encoded), "secret-a") || strings.Contains(string(encoded), "secret-b") {
		t.Fatal("identity JSON must never expose credentials")
	}
}

func TestProvider_InstanceSaltFailureKeepsCredentialSeparation(t *testing.T) {
	t.Parallel()

	// A store that cannot persist the salt must still use fresh randomness; a
	// shared public fallback would make instance ids predictable across
	// installations and weaken credential separation.
	firstCache := newFakeLyricsCache()
	firstCache.setError = errors.New("store unavailable")
	first := newTestProviderWithSources(nil, firstCache, time.Now, nil, nil)
	firstID := first.sourceInstanceID("better-lyrics")
	if firstID == "" {
		t.Fatal("an unpersistable salt must still yield a per-process identity")
	}
	if first.sourceInstanceID("better-lyrics") != firstID {
		t.Fatal("the per-process salt must stay stable within the process")
	}
	secondCache := newFakeLyricsCache()
	secondCache.setError = errors.New("store unavailable")
	second := newTestProviderWithSources(nil, secondCache, time.Now, nil, nil)
	if second.sourceInstanceID("better-lyrics") == firstID {
		t.Fatal("separate processes must not share a predictable fallback instance")
	}
}

func TestProvider_BundleTruncationReportsUnavailable(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}
	small := originalCandidate("plain", "small", "plain", []byte("small"), "rec-small")
	hugeText := strings.Repeat("[00:01.00]huge line\n", (MaxCacheEntryBytes/20)+100)
	huge := lyric.NewCandidate(lyrics.LyricsText{Text: hugeText}, "Same Provider", "lrc").
		WithOriginal("lrc", lyric.OriginalSupplied, []byte(hugeText)).
		WithRecording("rec-huge")
	src := source.Source{
		ID: "bounded", Display: "Bounded", Hosts: []string{"bounded.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			// Highest quality first; the bundle keeps what fits and drops the rest.
			return []lyric.Candidate{huge, small}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatal(err)
	}
	key, _ := lyricsVariantCacheKey(track, "bounded")
	raw, exists, _ := cache.Get(key)
	if !exists {
		t.Fatal("bundle must keep the candidate that fits")
	}
	var bundle cachedSourceBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Candidates) == 0 || bundle.Candidates[0].Quality != lyric.TimingUnsynced {
		t.Fatalf("bundle = %#v, want the small representation kept", bundle.Candidates)
	}

	// Playback keeps the winner but must not expose its dropped identity; the
	// bundled small representation stays addressable.
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatal(err)
	}
	if len(v2.Entries) == 0 || v2.Entries[0].Identity != nil {
		t.Fatalf("v2 winner = %#v, want no identity for a truncated winner", v2.Entries)
	}
	foundSmall := false
	for _, entry := range v2.Entries {
		if entry.Identity != nil && strings.HasPrefix(entry.Identity.Recording, "rec-small@") {
			foundSmall = true
		}
	}
	if !foundSmall {
		t.Fatal("v2 must keep the bundled small representation addressable")
	}
	listing := p.GetLyricsVariants(track)
	if len(listing) != 1 || !strings.HasPrefix(listing[0].Identity.Recording, "rec-small@") {
		t.Fatalf("listing = %#v, want the bundled small representation", listing)
	}
	if listing[0].PlaybackIndex != -1 {
		t.Fatalf("playback index = %d, want unknown (-1) when the winner is not cacheable", listing[0].PlaybackIndex)
	}

	// The dropped selection has no address: it is reported unavailable and is
	// never remapped to the candidate that fit.
	hugeIdentity := lyric.NewVariantIdentity(p.sourceInstanceID("bounded"), huge.RecordingIdentity(), "", "main", "lrc")
	document, reason := p.GetLyricsOriginal(track, hugeIdentity)
	if reason == "" || len(document.Bytes) != 0 {
		t.Fatalf("dropped selection = %#v/%q, want explicit unavailable", document, reason)
	}
}

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
