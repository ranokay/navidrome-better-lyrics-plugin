package source

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// testLookupBudget bounds adapter fetches in tests the way the provider's
// stage-one walk bounds them in production.
const testLookupBudget = 8 * time.Second

// fakeRuntime is the source.Runtime a fetch under test runs against: scripted
// transport, in-memory store, injected clock, and recorded cooldowns. Its
// request-timeout, cooldown, and error wrapping follow the provider's.
type fakeRuntime struct {
	send func(host.HTTPRequest) (*host.HTTPResponse, error)
	// setError forces every store write to fail, like an exhausted KV store.
	store *fakeStore
	now   func() time.Time

	betterLyricsAPIKey  string
	musixmatchUserToken string
}

func newFakeRuntime(send func(host.HTTPRequest) (*host.HTTPResponse, error)) *fakeRuntime {
	return newFakeRuntimeWithCache(send, newFakeStore(), time.Now)
}

func newFakeRuntimeWithCache(send func(host.HTTPRequest) (*host.HTTPResponse, error), store *fakeStore, now func() time.Time) *fakeRuntime {
	if store == nil {
		store = newFakeStore()
	}
	if now == nil {
		now = time.Now
	}
	return &fakeRuntime{send: send, store: store, now: now}
}

func (r *fakeRuntime) Do(sourceName, method, requestURL string, body []byte, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
	timeout, err := r.requestTimeoutMilliseconds(deadline)
	if err != nil {
		return nil, err
	}
	if r.send == nil {
		return nil, fmt.Errorf("%s returned no response", sourceName)
	}
	response, err := r.send(host.HTTPRequest{
		Method:    method,
		URL:       requestURL,
		Body:      body,
		Headers:   headers,
		TimeoutMs: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", sourceName, err)
	}
	if response == nil {
		return nil, fmt.Errorf("%s returned no response", sourceName)
	}
	return response, nil
}

// requestTimeoutMilliseconds mirrors the provider's per-request bound: the
// remaining lookup budget, capped by the plugin's per-request ceiling. An
// exhausted budget returns the shared sentinel the sweep defers on.
func (r *fakeRuntime) requestTimeoutMilliseconds(deadline time.Time) (int32, error) {
	remaining := deadline.Sub(r.Now())
	if remaining <= 0 {
		return 0, ErrLookupBudgetExhausted
	}
	perRequest := 6 * time.Second
	if remaining < perRequest {
		perRequest = remaining
	}
	milliseconds := (perRequest + time.Millisecond - 1) / time.Millisecond
	if milliseconds < 1 {
		milliseconds = 1
	}
	return int32(milliseconds), nil
}

func (r *fakeRuntime) DefaultHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": "NavidromeBetterLyricsPlugin/test (+https://example.test)",
	}
}

func (r *fakeRuntime) Now() time.Time {
	return r.now()
}

func (r *fakeRuntime) Cache() Store {
	return r.store
}

func (r *fakeRuntime) BetterLyricsAPIKey() string {
	return r.betterLyricsAPIKey
}

func (r *fakeRuntime) MusixmatchUserToken() string {
	return r.musixmatchUserToken
}

func (r *fakeRuntime) Cooldown(key string) time.Duration {
	value, exists, err := r.store.Get(key)
	if err != nil || !exists {
		return 0
	}
	untilUnix, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		return 0
	}
	remaining := time.Unix(untilUnix, 0).Sub(r.Now())
	if remaining <= 0 {
		return 0
	}
	return time.Duration((remaining+time.Second-1)/time.Second) * time.Second
}

func (r *fakeRuntime) RememberCooldown(key string, retryAfter time.Duration) {
	now := r.Now()
	deadline := now.Add(retryAfter)
	untilUnix := deadline.Unix()
	if deadline.Nanosecond() != 0 {
		untilUnix++
	}
	ttlSeconds := int64((time.Unix(untilUnix, 0).Sub(now) + time.Second - 1) / time.Second)
	_ = r.store.SetWithTTL(key, []byte(strconv.FormatInt(untilUnix, 10)), ttlSeconds)
}

// fakeStore is an in-memory Store that records TTLs per key.
type fakeStore struct {
	mu         sync.Mutex
	values     map[string][]byte
	ttls       []int64
	ttlsForKey map[string]int64
	setError   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: make(map[string][]byte), ttlsForKey: make(map[string]int64)}
}

func (c *fakeStore) Get(key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, exists := c.values[key]
	return append([]byte(nil), value...), exists, nil
}

func (c *fakeStore) SetWithTTL(key string, value []byte, ttlSeconds int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setError != nil {
		return c.setError
	}
	c.values[key] = append([]byte(nil), value...)
	c.ttls = append(c.ttls, ttlSeconds)
	c.ttlsForKey[key] = ttlSeconds
	return nil
}

func (c *fakeStore) lastTTL() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ttls) == 0 {
		return 0
	}
	return c.ttls[len(c.ttls)-1]
}

// hostXMLResponse is the 200/XML response shape the source tests serve.
func hostXMLResponse(body string) *host.HTTPResponse {
	return &host.HTTPResponse{StatusCode: 200, Body: []byte(body)}
}

// base64Encode encodes fixture payloads the way the providers ship them.
func base64Encode(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func jsonUnmarshal[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return value
}

func assertLyricsSource(t *testing.T, source *lyrics.LyricsSource, wantProvider, wantFormat string) {
	t.Helper()
	if source == nil {
		t.Fatalf("GetLyrics() source = nil, want provider %q and format %q", wantProvider, wantFormat)
	}
	if source.Provider != wantProvider || source.Format != wantFormat {
		t.Fatalf(
			"GetLyrics() source = %#v, want provider %q and format %q",
			source,
			wantProvider,
			wantFormat,
		)
	}
}
