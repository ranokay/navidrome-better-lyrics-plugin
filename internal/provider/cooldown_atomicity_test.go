package provider

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

type listableCache struct {
	*fakeLyricsCache
}

func (c listableCache) List(prefix string) ([]string, error) {
	c.fakeLyricsCache.mu.Lock()
	defer c.fakeLyricsCache.mu.Unlock()
	keys := make([]string, 0, len(c.values))
	for key := range c.values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func TestStaleCooldownObservationCannotShortenNewerWindow(t *testing.T) {
	t.Parallel()
	now := time.Unix(30_000, 0)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	key := source.GlobalCooldownKey("one")

	p.RememberCooldown(key, time.Hour)
	now = now.Add(5 * time.Minute)
	// An older observation still finishing cannot replace the newer deadline.
	p.RememberCooldown(key, time.Minute)
	if got := p.Cooldown(key); got != 55*time.Minute {
		t.Fatalf("cooldown = %s, want exactly the newer 55m window", got)
	}

	// A genuinely newer Retry-After still extends past every observation.
	p.RememberCooldown(key, 2*time.Hour)
	if got := p.Cooldown(key); got != 2*time.Hour {
		t.Fatalf("extended cooldown = %s, want exactly two hours", got)
	}
}

func TestNewerFailureSurvivesConcurrentStaleRecovery(t *testing.T) {
	t.Parallel()
	now := time.Unix(40_000, 0)
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)

	p.rememberSourceFailure("one", now, errors.New("upstream failed"))
	now = now.Add(5 * time.Second)
	p.rememberSourceFailure("one", now, errors.New("upstream failed"))

	// A recovery that started before the newer failure completed must not
	// erase its backoff, even when its reset write lands last.
	p.resetSourceFailureBefore("one", now.Add(-time.Millisecond))
	if got := p.sourceFailureBackoff("one"); got != 10*time.Second {
		t.Fatalf("backoff after stale recovery = %s, want the newer 10s window", got)
	}

	// A recovery that started after the failure is authoritative.
	p.resetSourceFailureBefore("one", now.Add(time.Millisecond))
	if got := p.sourceFailureBackoff("one"); got != 0 {
		t.Fatalf("backoff after later recovery = %s, want it cleared", got)
	}

	// A failure observed at the same clock tick is treated as newer than the
	// recovery; a cooldown must not be shortened by millisecond granularity.
	tied := newTestProviderWithSources(nil, newFakeLyricsCache(), func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	tied.rememberSourceFailure("one", now, errors.New("upstream failed"))
	tied.resetSourceFailureBefore("one", now)
	if got := tied.sourceFailureBackoff("one"); got != 5*time.Second {
		t.Fatalf("backoff after tied recovery = %s, want the failure preserved", got)
	}
}

func TestOverlappingFailureCannotRegressNewerObservation(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(15_000)
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)

	// Failure B starts at 15s and records a newer observation.
	p.rememberSourceFailure("one", now, errors.New("upstream failed"))
	// A stale recovery that started at 10s must not erase B.
	p.resetSourceFailureBefore("one", time.UnixMilli(10_000))
	// Failure A started at 0s but completes last. Its late write must not
	// regress UpdatedAt below the recovery marker and erase B.
	now = time.UnixMilli(18_000)
	p.rememberSourceFailure("one", time.UnixMilli(0), errors.New("upstream failed"))
	if got := p.sourceFailureBackoff("one"); got != 10*time.Second {
		t.Fatalf("backoff after late overlapping failure = %s, want the newer 10s window preserved", got)
	}
}

func TestFailureObservationsSurviveLostUpdate(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(15_000)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)

	// Failure B records the newer observation.
	p.rememberSourceFailure("one", now, errors.New("upstream failed"))
	p.resetSourceFailureBefore("one", time.UnixMilli(10_000))

	// A concurrent writer that read before B appends an older observation
	// instead of overwriting B's key.
	stale, err := json.Marshal(sourceFailureState{Failures: 1, Until: 12_000, UpdatedAt: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(sourceFailureObservationPrefix+"one:0:stale", stale, 600); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(sourceFailureResetObservationPrefix+"one:5000:stale", []byte("5000"), 600); err != nil {
		t.Fatal(err)
	}
	now = time.UnixMilli(18_000)
	if got := p.sourceFailureBackoff("one"); got != 2*time.Second {
		t.Fatalf("backoff after a lost-update observation = %s, want B's 2s remaining window", got)
	}
}

func TestHealthyResetWritesNoFailureObservation(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(20_000)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)

	p.resetSourceFailureBefore("one", now)
	if keys, err := cache.List(sourceFailureResetObservationPrefix + "one:"); err != nil || len(keys) != 0 {
		t.Fatalf("healthy reset observations = %v, %v; want none", keys, err)
	}
	if _, exists, err := cache.Get(sourceFailureResetKey("one")); err != nil || exists {
		t.Fatalf("healthy legacy reset = %v, %v; want none", exists, err)
	}

	p.rememberSourceFailure("one", now, errors.New("upstream failed"))
	p.resetSourceFailureBefore("one", now.Add(time.Second))
	if keys, err := cache.List(sourceFailureResetObservationPrefix + "one:"); err != nil || len(keys) != 1 {
		t.Fatalf("recovery reset observations = %v, %v; want one", keys, err)
	}
	if legacy, exists, err := cache.Get(sourceFailureKey("one")); err != nil || !exists || string(legacy) != "{}" {
		t.Fatalf("legacy failure clear = %q, %v; want an empty record", legacy, err)
	}
}

type countingCache struct {
	listableCache
	gets  int
	lists int
}

func (c *countingCache) Get(key string) ([]byte, bool, error) {
	c.gets++
	return c.listableCache.Get(key)
}

func (c *countingCache) List(prefix string) ([]string, error) {
	c.lists++
	return c.listableCache.List(prefix)
}

func TestControlMergeCooldownIsMonotonic(t *testing.T) {
	t.Parallel()
	now := time.Unix(110_000, 0)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	p.publisher = &fakePublisher{handled: true, active: "generation", control: true, cache: cache}
	key := source.GlobalCooldownKey("one")
	p.RememberCooldown(key, time.Hour)
	now = now.Add(5 * time.Minute)
	p.RememberCooldown(key, time.Minute)
	if got := p.Cooldown(key); got != 55*time.Minute {
		t.Fatalf("merged cooldown = %s, want 55m", got)
	}
	if keys, err := cache.List(key + cooldownObservationSuffix); err != nil || len(keys) != 0 {
		t.Fatalf("merged cooldown observations = %v, %v; want none", keys, err)
	}
}

func TestControlMergeFailureIsMonotonic(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(120_000)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	p.publisher = &fakePublisher{handled: true, active: "generation", control: true, cache: cache}

	p.rememberSourceFailure("one", now, errors.New("upstream failed"))
	p.resetSourceFailureBefore("one", time.UnixMilli(110_000))
	now = time.UnixMilli(123_000)
	p.rememberSourceFailure("one", time.UnixMilli(0), errors.New("upstream failed"))
	// The reset advanced the epoch to 110s, so this failure started before
	// the recovery and must not extend the surviving window.
	if got := p.sourceFailureBackoff("one"); got != 2*time.Second {
		t.Fatalf("merged backoff = %s, want the surviving 2s window", got)
	}
	now = time.UnixMilli(125_000)
	p.resetSourceFailureBefore("one", now)
	if got := p.sourceFailureBackoff("one"); got != 0 {
		t.Fatalf("backoff after recovery = %s, want it cleared", got)
	}
	now = time.UnixMilli(126_000)
	p.rememberSourceFailure("one", now, errors.New("post-recovery failure"))
	if got := p.sourceFailureBackoff("one"); got != 5*time.Second {
		t.Fatalf("backoff after recovery failure = %s, want a fresh 5s window", got)
	}
}

func TestCapableAndIndeterminateReadsAreBounded(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"capable", "indeterminate"} {
		for _, missingRow := range []bool{false, true} {
			name := mode + "/with-row"
			if missingRow {
				name = mode + "/missing-row"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				now := time.UnixMilli(130_000)
				cache := &countingCache{listableCache: listableCache{newFakeLyricsCache()}}
				p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
				publisher := &fakePublisher{
					handled: true, active: "generation", control: true,
					reduceErr: errors.New("reducer unavailable"), cache: cache.fakeLyricsCache,
				}
				if mode == "indeterminate" {
					publisher.controlErr = errors.New("kv unavailable")
				}
				p.publisher = publisher
				for i := 0; i < 1000; i++ {
					key := sourceFailureObservationPrefix + "one:" + strconv.Itoa(i)
					if err := cache.SetWithTTL(key, []byte(`{"failures":1,"until":9999999999999}`), 600); err != nil {
						t.Fatal(err)
					}
					key = source.GlobalCooldownKey("one") + cooldownObservationSuffix + strconv.Itoa(i)
					if err := cache.SetWithTTL(key, []byte("999999"), 3600); err != nil {
						t.Fatal(err)
					}
				}
				if !missingRow {
					reducer, err := json.Marshal(sourceFailureState{Failures: 1, Until: 140000, UpdatedAt: 130000})
					if err != nil {
						t.Fatal(err)
					}
					if err := cache.SetWithTTL(sourceFailureKey("one"), reducer, 600); err != nil {
						t.Fatal(err)
					}
				}
				// Repeated reducer errors must not create observations that
				// force a scan.
				p.rememberSourceFailure("one", now, errors.New("upstream failed"))
				p.rememberSourceFailure("one", now, errors.New("upstream failed"))
				cache.gets, cache.lists = 0, 0
				if got := p.sourceFailureBackoff("one"); (missingRow && got != 0) || (!missingRow && got != 10*time.Second) {
					t.Fatalf("backoff = %s, missingRow=%v", got, missingRow)
				}
				if cache.lists != 0 || cache.gets > 2 {
					t.Fatalf("failure read work = %d gets, %d lists; want at most 2 gets and no list", cache.gets, cache.lists)
				}
				cache.gets, cache.lists = 0, 0
				if got := p.Cooldown(source.GlobalCooldownKey("one")); got != 0 {
					t.Fatalf("cooldown = %s, want none", got)
				}
				if cache.lists != 0 || cache.gets != 1 {
					t.Fatalf("cooldown read work = %d gets, %d lists; want one get and no list", cache.gets, cache.lists)
				}
			})
		}
	}
}

func TestControlMergeFailurePreservesCanonicalState(t *testing.T) {
	t.Parallel()
	now := time.Unix(140_000, 0)
	cache := &countingCache{listableCache: listableCache{newFakeLyricsCache()}}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	p.publisher = &fakePublisher{
		handled: true, active: "generation", control: true,
		mergeErr: errors.New("control unavailable"), reduceErr: errors.New("control unavailable"), cache: cache,
	}
	key := source.GlobalCooldownKey("one")
	if err := cache.SetWithTTL(key, []byte(strconv.FormatInt(now.Add(time.Hour).Unix(), 10)), 3600); err != nil {
		t.Fatal(err)
	}
	row, err := json.Marshal(sourceFailureState{Failures: 1, Until: now.Add(30 * time.Second).UnixMilli(), UpdatedAt: now.UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(sourceFailureKey("one"), row, 600); err != nil {
		t.Fatal(err)
	}

	// A failed merge must not replace the longer canonical deadline with the
	// shorter one. It must not even read-modify-write the canonical key.
	cache.gets, cache.lists = 0, 0
	p.RememberCooldown(key, time.Minute)
	if cache.gets != 0 || cache.lists != 0 {
		t.Fatalf("failed merge work = %d gets, %d lists; want no canonical access", cache.gets, cache.lists)
	}
	if keys, err := cache.List(key + cooldownObservationSuffix); err != nil || len(keys) != 0 {
		t.Fatalf("cooldown observations after a failed merge = %v, %v; want none", keys, err)
	}
	if got := p.Cooldown(key); got != time.Hour {
		t.Fatalf("cooldown after a failed merge = %s, want the canonical hour", got)
	}
	p.rememberSourceFailure("one", now, errors.New("upstream failed"))
	if got := p.sourceFailureBackoff("one"); got != 30*time.Second {
		t.Fatalf("backoff after a failed reduce = %s, want the canonical 30s window", got)
	}
	p.resetSourceFailureBefore("one", now.Add(time.Second))
	if got := p.sourceFailureBackoff("one"); got != 30*time.Second {
		t.Fatalf("backoff after a failed reset = %s, want the canonical window", got)
	}
	if keys, err := cache.List(sourceFailureObservationPrefix + "one:"); err != nil || len(keys) != 0 {
		t.Fatalf("fallback observations = %v, %v; want none", keys, err)
	}
	if raw, exists, err := cache.Get(sourceFailureKey("one")); err != nil || !exists || string(raw) != string(row) {
		t.Fatalf("canonical failure row = %q, %v; want it unchanged", raw, err)
	}
}

func TestLatePreRecoveryFailureIsRejected(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(150_000)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	p.publisher = &fakePublisher{handled: true, active: "generation", control: true, cache: cache}

	p.rememberSourceFailure("one", now, errors.New("upstream failed"))
	now = time.UnixMilli(160_000)
	p.resetSourceFailureBefore("one", now)
	now = time.UnixMilli(170_000)
	p.rememberSourceFailure("one", time.UnixMilli(100_000), errors.New("late pre-recovery failure"))
	if got := p.sourceFailureBackoff("one"); got != 0 {
		t.Fatalf("late pre-recovery backoff = %s, want it rejected by the recovery epoch", got)
	}
}

func TestTokenCarryingRetainedSweepDoesNotReleaseParent(t *testing.T) {
	t.Parallel()
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, []source.Source{
		variantSource("one", "[00:01.00]one", "lrc"),
	}, nil)
	publisher := &fakePublisher{handled: true, accepted: true, token: "parent", cache: p.cache}
	p.publisher = publisher
	payload, err := json.Marshal(sweepTaskPayload{
		Track:       lyrics.TrackInfo{Title: "Song", Artist: "Artist"},
		Publication: Publication{Generation: "generation", Token: "parent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OnTaskExecute(taskworker.TaskExecuteRequest{QueueName: sweepQueueName, TaskID: "old", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if publisher.releases != 0 {
		t.Fatalf("releases = %d, want a pre-fix child to leave its parent token alone", publisher.releases)
	}
}

func TestLateFailureCannotBypassPostResetState(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(150_000)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	p.publisher = &fakePublisher{handled: true, active: "generation", control: true, cache: cache}

	p.rememberSourceFailure("one", now, errors.New("first failure"))
	now = time.UnixMilli(200_000)
	p.resetSourceFailureBefore("one", now)
	now = time.UnixMilli(300_000)
	p.rememberSourceFailure("one", now, errors.New("post-reset failure"))
	now = time.UnixMilli(301_000)
	p.rememberSourceFailure("one", time.UnixMilli(150_000), errors.New("late pre-reset failure"))
	if got := p.sourceFailureBackoff("one"); got != 4*time.Second {
		t.Fatalf("backoff after late pre-reset failure = %s, want the post-reset 4s window", got)
	}
}

func TestCooldownMergeFailureSurvivesNextInvocation(t *testing.T) {
	t.Parallel()
	now := time.Unix(150_000, 0)
	cache := listableCache{newFakeLyricsCache()}
	key := source.GlobalCooldownKey("one")
	first := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	first.publisher = &fakePublisher{handled: true, active: "generation", control: true, cache: cache}
	first.RememberCooldown(key, time.Hour)
	now = now.Add(5 * time.Minute)
	second := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	second.publisher = &fakePublisher{handled: true, active: "generation", control: true, mergeErr: errors.New("merge failed"), cache: cache}
	second.RememberCooldown(key, time.Minute)
	if got := second.Cooldown(key); got != 55*time.Minute {
		t.Fatalf("cooldown after shorter merge failure = %s, want the longer 55m window", got)
	}
}

func TestReducerFailureLeavesCanonicalRowUnchanged(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(130_000)
	cache := listableCache{newFakeLyricsCache()}
	row, err := json.Marshal(sourceFailureState{Failures: 1, Until: 131_000, UpdatedAt: 130_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(sourceFailureKey("one"), row, 600); err != nil {
		t.Fatal(err)
	}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	p.publisher = &fakePublisher{
		handled: true, active: "generation", control: true,
		reduceErr: errors.New("reducer unavailable"), cache: cache,
	}
	now = time.UnixMilli(140_000)
	p.rememberSourceFailure("one", now, errors.New("new failure"))
	if keys, err := cache.List(sourceFailureObservationPrefix + "one:"); err != nil || len(keys) != 0 {
		t.Fatalf("fallback observations = %v, %v; want none", keys, err)
	}
	if raw, exists, err := cache.Get(sourceFailureKey("one")); err != nil || !exists || string(raw) != string(row) {
		t.Fatalf("canonical row = %q, %v; want it unchanged", raw, err)
	}
}

func TestReducerFailurePreservesCommittedReset(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(150_000)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	publisher := &fakePublisher{handled: true, active: "generation", control: true, cache: cache}
	p.publisher = publisher
	p.rememberSourceFailure("one", now, errors.New("pre-reset failure"))
	now = time.UnixMilli(200_000)
	p.resetSourceFailureBefore("one", now)
	publisher.reduceErr = errors.New("reducer unavailable")
	now = time.UnixMilli(250_000)
	p.rememberSourceFailure("one", time.UnixMilli(150_000), errors.New("late pre-reset failure"))
	if got := p.sourceFailureBackoff("one"); got != 0 {
		t.Fatalf("fallback backoff after committed reset = %s, want zero", got)
	}
}

func TestOutOfOrderFailureObservationsKeepEscalatedWindow(t *testing.T) {
	t.Parallel()
	for name, control := range map[string]bool{"legacy": false, "control": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			now := time.UnixMilli(100_000)
			cache := listableCache{newFakeLyricsCache()}
			p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
			if control {
				p.publisher = &fakePublisher{
					handled: true, active: "generation", control: true, cache: cache.fakeLyricsCache,
				}
			}
			p.rememberSourceFailure("one", now, errors.New("first"))
			now = now.Add(5 * time.Second)
			p.rememberSourceFailure("one", now, errors.New("newer start completes first"))
			now = now.Add(time.Millisecond)
			// An older attempt completes last; its escalated window must survive.
			p.rememberSourceFailure("one", time.UnixMilli(99_999), errors.New("older start completes last"))
			if got := p.sourceFailureBackoff("one"); got != 20*time.Second {
				t.Fatalf("backoff after out-of-order completion = %s, want the escalated 20s window", got)
			}
		})
	}
}

func TestOlderRecoveryAdvancesResetEpoch(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(90_000)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	p.publisher = &fakePublisher{handled: true, active: "generation", control: true, cache: cache}

	p.rememberSourceFailure("one", now, errors.New("first failure"))
	now = time.UnixMilli(300_000)
	p.rememberSourceFailure("one", now, errors.New("post-reset failure"))
	// A success that started before the newer failure must still advance the
	// recovery epoch without hiding that failure.
	p.resetSourceFailureBefore("one", time.UnixMilli(250_000))
	if keys, err := cache.List(sourceFailureObservationPrefix + "one:"); err != nil || len(keys) != 0 {
		t.Fatalf("observations after a retained reset = %v, %v; want none", keys, err)
	}
	now = time.UnixMilli(310_000)
	p.rememberSourceFailure("one", time.UnixMilli(205_000), errors.New("late pre-recovery failure"))
	if got := p.sourceFailureBackoff("one"); got != 0 {
		t.Fatalf("backoff after a late pre-recovery failure = %s, want the newer window preserved", got)
	}
}

func TestReducerFailureCannotBypassCommittedReset(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(150_000)
	cache := listableCache{newFakeLyricsCache()}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	publisher := &fakePublisher{handled: true, active: "generation", control: true, cache: cache}
	p.publisher = publisher
	p.rememberSourceFailure("one", now, errors.New("pre-reset failure"))
	now = time.UnixMilli(200_000)
	p.resetSourceFailureBefore("one", now)
	now = time.UnixMilli(300_000)
	p.rememberSourceFailure("one", now, errors.New("post-reset failure"))
	publisher.reduceErr = errors.New("reducer unavailable")
	now = time.UnixMilli(301_000)
	p.rememberSourceFailure("one", time.UnixMilli(150_000), errors.New("late pre-reset failure"))
	if got := p.sourceFailureBackoff("one"); got != 4*time.Second {
		t.Fatalf("backoff during a reducer outage = %s, want the post-reset 4s window", got)
	}
}

func TestIndeterminateCapabilityDoesNotWriteLegacy(t *testing.T) {
	t.Parallel()
	now := time.Unix(140_000, 0)
	cache := &countingCache{listableCache: listableCache{newFakeLyricsCache()}}
	p := newTestProviderWithSources(nil, cache, func() time.Time { return now }, []source.Source{{ID: "one"}}, nil)
	publisher := &fakePublisher{
		handled: true, active: "generation", control: true,
		controlErr: errors.New("kv unavailable"), cache: cache,
	}
	p.publisher = publisher
	key := source.GlobalCooldownKey("one")
	if err := cache.SetWithTTL(key, []byte(strconv.FormatInt(now.Add(time.Hour).Unix(), 10)), 3600); err != nil {
		t.Fatal(err)
	}
	row, err := json.Marshal(sourceFailureState{Failures: 1, Until: now.Add(30 * time.Second).UnixMilli(), UpdatedAt: now.UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SetWithTTL(sourceFailureKey("one"), row, 600); err != nil {
		t.Fatal(err)
	}

	// Every unknown-capability path must leave the canonical records alone
	// and perform no observation-list scan.
	p.RememberCooldown(key, time.Minute)
	if keys, err := cache.List(key + cooldownObservationSuffix); err != nil || len(keys) != 0 {
		t.Fatalf("cooldown observations = %v, %v; want none", keys, err)
	}
	p.rememberSourceFailure("one", now, errors.New("upstream failed"))
	if keys, err := cache.List(sourceFailureObservationPrefix + "one:"); err != nil || len(keys) != 0 {
		t.Fatalf("failure observations = %v, %v; want none", keys, err)
	}
	if raw, exists, err := cache.Get(sourceFailureKey("one")); err != nil || !exists || string(raw) != string(row) {
		t.Fatalf("canonical row = %q, %v; want it unchanged", raw, err)
	}
	p.resetSourceFailureBefore("one", now.Add(time.Second))
	if keys, err := cache.List(sourceFailureResetObservationPrefix + "one:"); err != nil || len(keys) != 0 {
		t.Fatalf("reset observations = %v, %v; want none", keys, err)
	}
	if _, exists, err := cache.Get(sourceFailureResetKey("one")); err != nil || exists {
		t.Fatalf("legacy reset = %v, %v; want none", exists, err)
	}
	if got := p.Cooldown(key); got != time.Hour {
		t.Fatalf("cooldown with an indeterminate capability = %s, want the canonical hour", got)
	}
	if got := p.sourceFailureBackoff("one"); got != 30*time.Second {
		t.Fatalf("backoff with an indeterminate capability = %s, want the canonical window", got)
	}
	cache.gets, cache.lists = 0, 0
	p.sourceFailureBackoff("one")
	if cache.lists != 0 || cache.gets > 2 {
		t.Fatalf("indeterminate failure read = %d gets, %d lists; want at most 2 gets", cache.gets, cache.lists)
	}

	// A later successful probe must not have been cached as unsupported;
	// the capable path records no observation.
	publisher.controlErr = nil
	p.RememberCooldown(key, 2*time.Hour)
	if keys, err := cache.List(key + cooldownObservationSuffix); err != nil || len(keys) != 0 {
		t.Fatalf("cooldown observations after probe recovery = %v, %v; want none", keys, err)
	}
	if got := p.Cooldown(key); got != 2*time.Hour {
		t.Fatalf("cooldown after the probe recovered = %s, want two hours", got)
	}
}
