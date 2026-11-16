package provider

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

// instanceSaltKey persists the per-installation HMAC key used for source
// instance identities. It is separate from the credential fingerprint salt so
// identity stability does not depend on cache-coverage semantics.
const instanceSaltKey = "variant-instance-salt:v1"

var instanceSaltMutex sync.Mutex

// instanceSalt returns the installation's identity salt. A random value is
// always used; if it cannot be persisted the process keeps a fresh random salt,
// so identities remain credential-separated but do not survive a restart
// (retrieval then reports unavailable). It never falls back to a public
// constant.
func (p *Provider) instanceSalt() []byte {
	instanceSaltMutex.Lock()
	defer instanceSaltMutex.Unlock()
	if len(p.instanceSaltBytes) >= 16 {
		return p.instanceSaltBytes
	}
	if raw, exists, _ := p.cache.Get(instanceSaltKey); exists && len(raw) >= 16 {
		p.instanceSaltBytes = raw
		return raw
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		p.logf("variant identity salt unavailable: secure randomness failed; originals disabled for this process")
		return nil
	}
	_ = p.cache.SetWithTTL(instanceSaltKey, salt, credentialSaltTTL)
	p.instanceSaltBytes = salt
	return salt
}

// sourceInstanceID fingerprints one configured source instance with at least
// 128 bits of keyed HMAC output. A credential or config change produces a new
// instance; the credential itself never appears in the value.
func (p *Provider) sourceInstanceID(sourceID string) string {
	if sourceID == "" {
		return ""
	}
	salt := p.instanceSalt()
	if len(salt) < 16 {
		return ""
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte("navidrome-better-lyrics/variant-instance/v1\x00"))
	mac.Write([]byte(sourceID))
	mac.Write([]byte{0})
	mac.Write([]byte(p.sourceConfigSignature(sourceID)))
	return sourceID + ":" + hex.EncodeToString(mac.Sum(nil)[:16])
}

// sourceConfigSignature isolates the config inputs that define a source
// instance. Only Better Lyrics sources consume a credential today; scoped
// instances must add their fields here so they cannot alias another instance.
func (p *Provider) sourceConfigSignature(sourceID string) string {
	config := readProviderConfig(p.configLookup)
	if sourceID == "better-lyrics" || strings.HasPrefix(sourceID, "better-lyrics-") {
		return config.BetterLyricsAPIKey
	}
	return ""
}

// assignIdentity stamps a candidate with its current source instance and
// recording address. Candidates without a stable identity stay unaddressable.
func (p *Provider) assignIdentity(sourceID string, candidate *lyric.Candidate) {
	instance := p.sourceInstanceID(sourceID)
	if instance == "" {
		candidate.Identity = nil
		return
	}
	identity := lyric.NewVariantIdentity(instance, candidate.RecordingIdentity(), candidate.Language, "", candidate.Format())
	if !identity.Valid() {
		candidate.Identity = nil
		return
	}
	candidate.Identity = &identity
}
