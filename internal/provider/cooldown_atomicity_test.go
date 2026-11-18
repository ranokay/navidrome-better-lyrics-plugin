package provider

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

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
