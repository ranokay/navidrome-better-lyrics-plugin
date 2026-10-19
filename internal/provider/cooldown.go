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
	Failures uint8 `json:"failures"`
	Until    int64 `json:"until"`
}

func sourceFailureKey(sourceID string) string {
	return "source-failure:v1:" + sourceID
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
	return state
}

func (p *Provider) sourceFailureBackoff(sourceID string) time.Duration {
	state := p.readSourceFailure(sourceID)
	return max(0, time.UnixMilli(state.Until).Sub(p.now()))
}

// Ordinary failures get a short source-specific retry window. Rate limits keep
// their upstream Retry-After policy, and exhausting a lookup budget is not an
// outage. The KV record lets later songs and plugin instances skip the outage.
func (p *Provider) rememberSourceFailure(sourceID string, err error) {
	if errors.Is(err, source.ErrLookupBudgetExhausted) || source.IsRateLimitError(err) {
		return
	}
	state := p.readSourceFailure(sourceID)
	state.Failures = min(state.Failures+1, 5)
	delay := min(5*time.Second*time.Duration(1<<(state.Failures-1)), time.Minute)
	state.Until = p.now().Add(delay).UnixMilli()
	value, marshalErr := json.Marshal(state)
	if marshalErr == nil {
		// A broken store fails open; it must never disable a source.
		_ = p.cache.SetWithTTL(sourceFailureKey(sourceID), value, sourceFailureStateTTL)
	}
}

func (p *Provider) resetSourceFailure(sourceID string) {
	if state := p.readSourceFailure(sourceID); state.Failures > 0 {
		_ = p.cache.SetWithTTL(sourceFailureKey(sourceID), []byte("{}"), 1)
	}
}

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
		p.logf(fmt.Sprintf("rate-limit cooldown persistence failed: %v", p.safeDiagnosticError(err)))
	}
}

func durationSecondsCeil(duration time.Duration) int64 {
	return int64((duration + time.Second - 1) / time.Second)
}
