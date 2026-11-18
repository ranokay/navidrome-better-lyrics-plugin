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

const sourceFailureObservationPrefix = "source-failure-observation:v1:"
const sourceFailureResetObservationPrefix = "source-failure-reset-observation:v1:"

func (p *Provider) validSourceFailure(state sourceFailureState) bool {
	if state.Until <= 0 || state.Failures == 0 || state.Failures > 5 {
		return false
	}
	// Expired host entries must not preserve escalation indefinitely.
	if p.now().UnixMilli()-state.Until >= sourceFailureStateTTL*1000 {
		return false
	}
	return state.Until <= p.now().Add(time.Minute).UnixMilli()
}

func (p *Provider) readSourceFailure(sourceID string) sourceFailureState {
	if lister, ok := p.cache.(storeLister); ok {
		if state, found := p.readObservedSourceFailure(sourceID, lister); found {
			return state
		}
	}
	return p.readLegacySourceFailure(sourceID)
}

func (p *Provider) readObservedSourceFailure(sourceID string, lister storeLister) (sourceFailureState, bool) {
	keys, err := lister.List(sourceFailureObservationPrefix + sourceID + ":")
	if err != nil {
		return sourceFailureState{}, false
	}
	resetAt := p.readSourceFailureReset(sourceID, lister)
	var best sourceFailureState
	found := false
	for _, key := range keys {
		value, exists, err := p.cache.Get(key)
		if err != nil || !exists {
			continue
		}
		var state sourceFailureState
		if json.Unmarshal(value, &state) != nil || !p.validSourceFailure(state) {
			continue
		}
		if !found || state.UpdatedAt > best.UpdatedAt ||
			(state.UpdatedAt == best.UpdatedAt && state.Until > best.Until) {
			best, found = state, true
		}
	}
	if !found {
		return sourceFailureState{}, false
	}
	if best.UpdatedAt < resetAt {
		return sourceFailureState{}, true
	}
	return best, true
}

func (p *Provider) readSourceFailureReset(sourceID string, lister storeLister) int64 {
	maxReset := int64(0)
	if keys, err := lister.List(sourceFailureResetObservationPrefix + sourceID + ":"); err == nil {
		for _, key := range keys {
			value, exists, err := p.cache.Get(key)
			if err != nil || !exists {
				continue
			}
			if parsed, parseErr := strconv.ParseInt(string(value), 10, 64); parseErr == nil && parsed > maxReset {
				maxReset = parsed
			}
		}
	}
	if raw, exists, err := p.cache.Get(sourceFailureResetKey(sourceID)); err == nil && exists {
		if parsed, parseErr := strconv.ParseInt(string(raw), 10, 64); parseErr == nil && parsed > maxReset {
			maxReset = parsed
		}
	}
	return maxReset
}

func (p *Provider) readLegacySourceFailure(sourceID string) sourceFailureState {
	value, exists, err := p.cache.Get(sourceFailureKey(sourceID))
	if err != nil || !exists {
		return sourceFailureState{}
	}
	var state sourceFailureState
	if json.Unmarshal(value, &state) != nil || !p.validSourceFailure(state) {
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
	// Overlapping failures can complete out of order. Keep the latest
	// observation timestamp and the longest deadline so a late write cannot
	// regress newer failure state past a recovery reset.
	state.Until = max(state.Until, p.now().Add(delay).UnixMilli())
	state.UpdatedAt = max(state.UpdatedAt, observedAt.UnixMilli())
	value, marshalErr := json.Marshal(state)
	if marshalErr == nil {
		if _, canList := p.cache.(storeLister); canList {
			// Append-only observations avoid the read-modify-write lost update:
			// concurrent failures never overwrite each other, and readers pick
			// the newest observation with the longest deadline.
			key := fmt.Sprintf("%s%s:%d:%s", sourceFailureObservationPrefix, sourceID, state.UpdatedAt, newMarkerID())
			_ = p.cache.SetWithTTL(key, value, sourceFailureStateTTL)
		}
		// Keep the legacy key for older plugin readers.
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
	if p.readSourceFailure(sourceID).Failures == 0 {
		// A healthy source with no recorded failure needs no reset marker;
		// writing one per success would grow unbounded control records.
		return
	}
	resetAt := p.now().UnixMilli()
	if !started.IsZero() {
		resetAt = started.UnixMilli()
	}
	if lister, canList := p.cache.(storeLister); canList {
		if p.readSourceFailureReset(sourceID, lister) >= resetAt {
			return
		}
		// Reset observations are append-only too: a stale success cannot
		// overwrite a newer recovery marker.
		key := fmt.Sprintf("%s%s:%d:%s", sourceFailureResetObservationPrefix, sourceID, resetAt, newMarkerID())
		_ = p.cache.SetWithTTL(key, []byte(strconv.FormatInt(resetAt, 10)), sourceFailureStateTTL)
	} else if raw, exists, err := p.cache.Get(sourceFailureResetKey(sourceID)); err == nil && exists {
		if prior, parseErr := strconv.ParseInt(string(raw), 10, 64); parseErr == nil && prior >= resetAt {
			return
		}
	}
	// Keep the legacy reset key for older plugin readers.
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
