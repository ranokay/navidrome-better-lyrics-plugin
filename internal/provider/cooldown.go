package provider

import (
	"fmt"
	"strconv"
	"time"
)

// Cooldown reports the remaining provider-wide cooldown for a key; a cache
// failure or corrupt value fails open.
func (p *Provider) Cooldown(key string) time.Duration {
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		// A cache failure must not make the lyrics provider unavailable.
		//nolint:nilerr // Cooldown enforcement intentionally fails open.
		return 0
	}

	untilUnix, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		//nolint:nilerr // Ignore a corrupt cooldown value and keep serving requests.
		return 0
	}
	remaining := time.Unix(untilUnix, 0).Sub(p.now())
	if remaining <= 0 {
		return 0
	}
	seconds := durationSecondsCeil(remaining)
	return time.Duration(seconds) * time.Second
}

// RememberCooldown persists a cooldown deadline so sibling requests and
// processes observe the same window.
func (p *Provider) RememberCooldown(key string, retryAfter time.Duration) {
	now := p.now()
	deadline := now.Add(retryAfter)
	untilUnix := deadline.Unix()
	if deadline.Nanosecond() != 0 {
		// The deadline is stored as whole Unix seconds, so round up rather than shorten Retry-After.
		untilUnix++
	}
	ttlSeconds := durationSecondsCeil(time.Unix(untilUnix, 0).Sub(now))
	if err := p.cache.SetWithTTL(key, []byte(strconv.FormatInt(untilUnix, 10)), ttlSeconds); err != nil {
		p.logf(fmt.Sprintf("rate-limit cooldown persistence failed: %v", err))
	}
}

func durationSecondsCeil(duration time.Duration) int64 {
	return int64((duration + time.Second - 1) / time.Second)
}
