package source

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

const (
	defaultRateLimitCooldown       = 30 * time.Second
	defaultUnisonRateLimitCooldown = 60 * time.Second
	maxRateLimitCooldown           = time.Hour
)

const (
	// BetterLyricsGlobalCooldownKey is the provider-wide cooldown the Better
	// Lyrics family shares; it also carries the v3 keys the legacy fetchers
	// recorded before the registry existed, so provider walks consult it.
	BetterLyricsGlobalCooldownKey = "rate-limit:better-lyrics:global:v3"
	betterLyricsQueryCooldownKey  = "rate-limit:better-lyrics:query:v3:"
	unisonGlobalCooldownKey       = "rate-limit:unison:global:v1"
)

// GlobalCooldownKey names the provider-wide rate-limit cooldown for an identity:
// a source id, or a host shared by sibling sources.
func GlobalCooldownKey(identity string) string {
	return fmt.Sprintf("rate-limit:%s:global:v1", identity)
}

type rateLimitCooldownError struct {
	global    bool
	remaining time.Duration
}

type providerCooldownError struct {
	provider  string
	remaining time.Duration
}

func (e *rateLimitCooldownError) Error() string {
	scope := "request"
	if e.global {
		scope = "API"
	}
	return fmt.Sprintf("better lyrics %s cooldown active for %s", scope, e.remaining)
}

func (e *providerCooldownError) Error() string {
	return fmt.Sprintf("%s API cooldown active for %s", e.provider, e.remaining)
}

func activeBetterLyricsCooldown(rt Runtime, requestURL string) error {
	if remaining := rt.Cooldown(BetterLyricsGlobalCooldownKey); remaining > 0 {
		return &rateLimitCooldownError{global: true, remaining: remaining}
	}
	if remaining := rt.Cooldown(rateLimitQueryKey(requestURL)); remaining > 0 {
		return &rateLimitCooldownError{remaining: remaining}
	}
	return nil
}

func activeUnisonCooldown(rt Runtime) error {
	if remaining := rt.Cooldown(unisonGlobalCooldownKey); remaining > 0 {
		return &providerCooldownError{provider: "unison", remaining: remaining}
	}
	return nil
}

// rememberRateLimit records the better-lyrics family cooldown for a throttled
// response: the provider-wide window when the body declares a global limit,
// otherwise a per-query window.
func rememberRateLimit(rt Runtime, response *host.HTTPResponse, requestURL string) time.Duration {
	retryAfter := retryAfterDuration(response.Headers, rt.Now())
	key := rateLimitQueryKey(requestURL)
	if globalRateLimit(response) {
		key = BetterLyricsGlobalCooldownKey
	}
	rt.RememberCooldown(key, retryAfter)
	return retryAfter
}

func rememberUnisonRateLimit(rt Runtime, response *host.HTTPResponse) time.Duration {
	retryAfter := retryAfterDurationWithDefault(response.Headers, rt.Now(), defaultUnisonRateLimitCooldown)
	rt.RememberCooldown(unisonGlobalCooldownKey, retryAfter)
	return retryAfter
}

// rateLimitFailure records the response's Retry-After window on the source's
// provider-wide cooldown and returns the degraded error a rate-limited lookup
// produces.
func rateLimitFailure(rt Runtime, sourceID, label string, response *host.HTTPResponse) error {
	retryAfter := retryAfterDuration(response.Headers, rt.Now())
	rt.RememberCooldown(GlobalCooldownKey(sourceID), retryAfter)
	return rateLimitResponseError(label, response, retryAfter)
}

func rateLimitQueryKey(requestURL string) string {
	digest := sha256.Sum256([]byte(requestURL))
	return fmt.Sprintf("%s%x", betterLyricsQueryCooldownKey, digest)
}
