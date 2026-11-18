package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

const sourceFailureStateTTL = int64(10 * time.Minute / time.Second)

type sourceFailureState struct {
	Failures  uint8 `json:"failures"`
	Until     int64 `json:"until"`
	UpdatedAt int64 `json:"updatedAt,omitempty"`
}

func sourceFailureKey(sourceID string) string {
	return "source-failure:v1:" + sourceID
}

func sourceFailureResetKey(sourceID string) string {
	return "source-failure-reset:v1:" + sourceID
}

func (p *Provider) readSourceFailure(sourceID string) sourceFailureState {
	value, exists, err := p.cache.Get(sourceFailureKey(sourceID))
	if err != nil || !exists {
		return sourceFailureState{}
	}
	var state sourceFailureState
	if json.Unmarshal(value, &state) != nil || state.Until <= 0 || state.Failures == 0 || state.Failures > 5 {
		return sourceFailureState{}
	}
	// Expired host entries must not preserve escalation indefinitely.
	if p.now().UnixMilli()-state.Until >= sourceFailureStateTTL*1000 {
		return sourceFailureState{}
	}
	if state.Until > p.now().Add(time.Minute).UnixMilli() {
		return sourceFailureState{}
	}
	if raw, exists, err := p.cache.Get(sourceFailureResetKey(sourceID)); err == nil && exists {
		// Strictly older failures were being retried when the success
		// started. Equal observations keep the failure: a newer outage at the
		// same clock tick must not be erased by a concurrent recovery.
		if resetAt, parseErr := strconv.ParseInt(string(raw), 10, 64); parseErr == nil && state.UpdatedAt < resetAt {
			return sourceFailureState{}
		}
	}
	return state
}

func (p *Provider) sourceFailureBackoff(sourceID string) time.Duration {
	state := p.readSourceFailure(sourceID)
	return max(0, time.UnixMilli(state.Until).Sub(p.now()))
}

// Ordinary failures get a short source-specific retry window. Rate limits keep
// their upstream Retry-After policy, and exhausting a lookup budget is not an
// outage. The KV record lets later songs and plugin instances skip the outage.
func (p *Provider) rememberSourceFailure(sourceID string, started time.Time, err error) {
	if errors.Is(err, source.ErrLookupBudgetExhausted) || source.IsRateLimitError(err) {
		return
	}
	observedAt := p.now()
	if !started.IsZero() {
		observedAt = started
	}
	state := p.readSourceFailure(sourceID)
	state.Failures = min(state.Failures+1, 5)
	delay := min(5*time.Second*time.Duration(1<<(state.Failures-1)), time.Minute)
	state.Until = p.now().Add(delay).UnixMilli()
	state.UpdatedAt = observedAt.UnixMilli()
	value, marshalErr := json.Marshal(state)
	if marshalErr == nil {
		// A broken store fails open; it must never disable a source.
		_ = p.cache.SetWithTTL(sourceFailureKey(sourceID), value, sourceFailureStateTTL)
	}
}

func (p *Provider) resetSourceFailure(sourceID string) {
	p.resetSourceFailureBefore(sourceID, time.Time{})
}

// resetSourceFailureBefore clears a recorded guard only when it predates the
// operation that observed the success. A stale success cannot erase a newer
// failure backoff.
func (p *Provider) resetSourceFailureBefore(sourceID string, started time.Time) {
	resetAt := p.now().UnixMilli()
	if !started.IsZero() {
		resetAt = started.UnixMilli()
	}
	if raw, exists, err := p.cache.Get(sourceFailureResetKey(sourceID)); err == nil && exists {
		if prior, parseErr := strconv.ParseInt(string(raw), 10, 64); parseErr == nil && prior >= resetAt {
			return
		}
	}
	// The reset is a separate observation, never an overwrite of failure state:
	// a newer failure with a later UpdatedAt survives regardless of write order.
	_ = p.cache.SetWithTTL(sourceFailureResetKey(sourceID), []byte(strconv.FormatInt(resetAt, 10)), sourceFailureStateTTL)
}

// Cooldown reports the remaining provider-wide cooldown for a key; a cache
// failure or corrupt value fails open.
func (p *Provider) Cooldown(key string) time.Duration {
	untilUnix := p.cooldownDeadline(key)
	if lister, ok := p.cache.(storeLister); ok {
		if keys, err := lister.List(key + cooldownObservationSuffix); err == nil {
			for _, observation := range keys {
				if value, exists, err := p.cache.Get(observation); err == nil && exists {
					if parsed, parseErr := strconv.ParseInt(string(value), 10, 64); parseErr == nil && parsed > untilUnix {
						untilUnix = parsed
					}
				}
			}
		}
	}
	remaining := time.Unix(untilUnix, 0).Sub(p.now())
	if remaining <= 0 {
		return 0
	}
	seconds := durationSecondsCeil(remaining)
	return time.Duration(seconds) * time.Second
}

const cooldownObservationSuffix = ":observation:"

func (p *Provider) cooldownDeadline(key string) int64 {
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
	return untilUnix
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
	deadlineValue := []byte(strconv.FormatInt(untilUnix, 10))
	if _, canList := p.cache.(storeLister); canList {
		// Write the deadline-keyed observation first: if it cannot persist,
		// leaving the legacy key untouched preserves an existing newer window
		// instead of shortening it with a stale observation.
		observation := key + cooldownObservationSuffix + strconv.FormatInt(untilUnix, 10)
		if err := p.cache.SetWithTTL(observation, deadlineValue, ttlSeconds); err != nil {
			p.logf(fmt.Sprintf("rate-limit cooldown observation failed: %v", p.safeDiagnosticError(err)))
			return
		}
	}
	// Keep the legacy key for old hosts and readers.
	if err := p.cache.SetWithTTL(key, deadlineValue, ttlSeconds); err != nil {
		p.logf(fmt.Sprintf("rate-limit cooldown persistence failed: %v", p.safeDiagnosticError(err)))
	}
}

func durationSecondsCeil(duration time.Duration) int64 {
	return int64((duration + time.Second - 1) / time.Second)
}
