package source

import (
	"errors"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

// ErrLookupBudgetExhausted reports a Runtime call whose lookup deadline had
// already passed before the request started. The sweep treats it as deferred
// work rather than a source failure.
var ErrLookupBudgetExhausted = errors.New("lyrics lookup time budget exhausted")

// MaxCacheEntryBytes bounds one KV entry; both the provider's write guard and
// source-owned writes honor it.
const MaxCacheEntryBytes = 1 * 1024 * 1024

// Store is the plugin KV view a source may use for its own caching.
type Store interface {
	Get(key string) ([]byte, bool, error)
	SetWithTTL(key string, value []byte, ttlSeconds int64) error
}

// Runtime is the slice of the provider a source fetch may use: host HTTP with
// the lookup deadline applied, the plugin's common headers and secrets, its KV
// store, the clock, and provider-wide cooldown state.
type Runtime interface {
	Do(source, method, requestURL string, body []byte, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error)
	DefaultHeaders() map[string]string
	Now() time.Time
	Cache() Store
	BetterLyricsAPIKey() string
	MusixmatchUserToken() string
	Cooldown(key string) time.Duration
	RememberCooldown(key string, retryAfter time.Duration)
}

// Health classifies one fetch attempt for coverage accounting.
type Health uint8

const (
	Healthy Health = iota
	Uncertain
	Degraded
	// TemporarilySkipped marks a token/config rejection: not covered work,
	// not a miss, retryable as soon as the credential situation changes.
	TemporarilySkipped
)

// Merge keeps the least healthy classification of the two.
func (h Health) Merge(other Health) Health {
	if other > h {
		return other
	}
	return h
}

// Fetcher performs one source lookup and reports per-source health alongside
// its candidates.
type Fetcher func(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error)

// Source describes one upstream lyrics backend participating in the chain.
type Source struct {
	ID             string
	Display        string
	Hosts          []string
	DefaultEnabled bool
	Fetch          Fetcher
}
