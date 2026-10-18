package provider

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// cacheCredentialPepper is the fallback HMAC key used only when the KV store
// cannot hold the per-installation salt.
var cacheCredentialPepper = []byte("navidrome-better-lyrics/credential-fingerprint/v1")

// credentialSaltTTL keeps the salt effectively permanent while still riding
// the TTL API.
const credentialSaltTTL = int64(10 * 365 * 24 * time.Hour / time.Second)

// credentialSalt returns the per-installation HMAC salt, minting one on first
// use and persisting it in the plugin KV store. A public constant would let
// anyone who reads cached entries test credential guesses offline (CWE-760);
// a per-installation salt removes that verifiability. The constant
// remains only as a last resort so fingerprinting never blocks lyrics when the
// store refuses writes.

// credentialSaltMutex serializes first-use salt initialization so concurrent
// callers mint and persist one salt instead of racing.
var credentialSaltMutex sync.Mutex

func (p *Provider) credentialSalt() []byte {
	credentialSaltMutex.Lock()
	defer credentialSaltMutex.Unlock()
	if len(p.credentialSaltBytes) >= 16 {
		return p.credentialSaltBytes
	}
	if raw, exists, _ := p.cache.Get(credentialSaltKey); exists && len(raw) >= 16 {
		p.credentialSaltBytes = raw
		return raw
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return cacheCredentialPepper
	}
	if p.cache.SetWithTTL(credentialSaltKey, salt, credentialSaltTTL) != nil {
		// Keep the fallback consistent while the store refuses writes: the
		// next call retries the mint so a recovered store gets one stable
		// salt instead of splitting fingerprints mid-process.
		return cacheCredentialPepper
	}
	p.credentialSaltBytes = salt
	return salt
}

// lyricsCredentialsFingerprint distinguishes negative coverage stamped under
// different Better Lyrics API key / Musixmatch token configurations: adding or
// replacing a credential must re-authorize sources whose misses were recorded
// anonymously.
func (p *Provider) lyricsCredentialsFingerprint() string {
	config := readProviderConfig(p.configLookup)
	mac := hmac.New(sha256.New, p.credentialSalt())
	mac.Write([]byte(config.BetterLyricsAPIKey))
	mac.Write([]byte{0})
	mac.Write([]byte(config.MusixmatchUserToken))
	return fmt.Sprintf("%x", mac.Sum(nil)[:8])
}

// Positive results stay usable for display after a credential change, but only
// a result fetched with the relevant current credential covers fresh discovery.
// Unrelated credentials must not invalidate a healthy source's coverage.
func (p *Provider) sourceCredentialsFingerprint(sourceID string) string {
	config := readProviderConfig(p.configLookup)
	var credential string
	switch {
	case sourceID == "better-lyrics" || strings.HasPrefix(sourceID, "better-lyrics-"):
		credential = config.BetterLyricsAPIKey
	case sourceID == "musixmatch":
		credential = config.MusixmatchUserToken
	default:
		return ""
	}
	mac := hmac.New(sha256.New, p.credentialSalt())
	mac.Write([]byte(sourceID))
	mac.Write([]byte{0})
	mac.Write([]byte(credential))
	return fmt.Sprintf("%x", mac.Sum(nil)[:8])
}

// sourceMissKey namespaces the negative per-source entries by credential state
// so a newly configured token or API key cannot stay suppressed by an old miss.
func (p *Provider) sourceMissKey(track lyrics.TrackInfo, sourceID string) (string, bool) {
	base, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s:missing:%s", base, p.lyricsCredentialsFingerprint()), true
}
