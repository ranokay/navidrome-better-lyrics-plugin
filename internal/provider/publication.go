package provider

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// Publication is the live host publication metadata for one plugin invocation.
// An empty Generation means the host did not negotiate publication support and
// the provider must keep its legacy direct-write behavior.
type Publication struct {
	Generation string
	Token      string
	CallID     string
	Kind       string
}

// Publisher is the optional host publication transport. Admit and Commit use
// the host's reserved KV protocol; Current reads the host-owned committed
// discovery snapshot. Hosts without it report Handled=false on admission and
// the provider falls back to legacy writes.
type Publisher interface {
	Admit(trackKey string, request []byte) ([]byte, error)
	Commit(trackKey string, request []byte) error
	Current(trackKey string) ([]byte, bool, error)
	Active() (string, bool, error)
	// Probe reports whether the live host handles reserved publication keys.
	// It never reads persisted generation state.
	Probe() (bool, error)
}

const (
	maxPublicationWrites = 64
	maxPublicationBytes  = 16 << 20
)

var errPublicationSuperseded = errors.New("publication superseded")

// Discovery is the additive enhanced-response discovery state.
type Discovery struct {
	Revision string
	Complete bool
}

type admissionWire struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	RequestID  string `json:"requestId"`
	Generation string `json:"generation,omitempty"`
	Track      string `json:"track,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Token      string `json:"token,omitempty"`
}

type admissionResponseWire struct {
	Handled    bool   `json:"handled"`
	Accepted   bool   `json:"accepted"`
	RequestID  string `json:"requestId,omitempty"`
	Generation string `json:"generation,omitempty"`
	Token      string `json:"token,omitempty"`
	State      string `json:"state,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type publicationWriteWire struct {
	Key   string `json:"key"`
	Value []byte `json:"value"`
	TTL   int64  `json:"ttl"`
}

type commitWire struct {
	Version           int                    `json:"version"`
	Generation        string                 `json:"generation"`
	Token             string                 `json:"token"`
	Track             string                 `json:"track"`
	Scope             string                 `json:"scope"`
	Revision          string                 `json:"revision"`
	SnapshotDigest    string                 `json:"snapshotDigest"`
	SnapshotExpiresAt int64                  `json:"snapshotExpiresAt,omitempty"`
	Complete          bool                   `json:"complete"`
	AwaitTail         bool                   `json:"awaitTail,omitempty"`
	Writes            []publicationWriteWire `json:"writes"`
}

type discoveryStateWire struct {
	Handled    bool   `json:"handled"`
	Generation string `json:"generation,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Complete   bool   `json:"complete,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
}

type pendingPublication struct {
	generation string
	token      string
	trackKey   string
	scope      string
	kind       string
	writes     []publicationWriteWire
	bytes      int
	legacy     bool
	active     bool
	failed     bool
	minted     bool
	// denied marks a scope-less publication that must not write directly;
	// its host-minted token is released at terminal completion when the track
	// key is known.
	denied bool
}

func publicationTrackKey(track lyrics.TrackInfo) (string, bool) {
	return lyricsTrackKey(track)
}

// publicationScope hashes the configuration/credential identity. It is never
// exposed on the wire.
func (p *Provider) publicationScope(track lyrics.TrackInfo, sources []source.Source) (string, bool) {
	trackKey, ok := publicationTrackKey(track)
	if !ok || len(sources) == 0 {
		return "", false
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", lyricsCacheGeneration, strings.Join(source.IDs(sources), "|"), p.lyricsCredentialsFingerprint())))
	return fmt.Sprintf("%x:%s", digest[:16], trackKey), true
}

// beginPublication admits the current operation before it can write. Refresh
// tokens are minted by the host and arrive in the request; background tasks
// reuse the token carried in their payload.
func (p *Provider) beginPublication(track lyrics.TrackInfo, meta Publication, kind string) error {
	if p.publisher == nil || strings.TrimSpace(meta.Generation) == "" {
		p.publication = &pendingPublication{legacy: true}
		return nil
	}
	sources := p.activeSources()
	trackKey, _ := publicationTrackKey(track)
	scope, ok := p.publicationScope(track, sources)
	if !ok {
		p.publication = &pendingPublication{
			generation: meta.Generation, token: meta.Token, trackKey: trackKey,
			kind: kind, legacy: true, denied: true,
		}
		return nil
	}
	pub := &pendingPublication{
		generation: meta.Generation,
		token:      meta.Token,
		trackKey:   trackKey,
		scope:      scope,
		kind:       kind,
	}
	if pub.token == "" {
		request, err := json.Marshal(admissionWire{
			Version: 1, Action: "admit", RequestID: meta.CallID,
			Generation: meta.Generation, Track: trackKey, Scope: scope, Kind: kind,
		})
		if err != nil {
			return err
		}
		response, err := p.publisher.Admit(trackKey, request)
		if err != nil {
			// A negotiated publication call must not fall through to
			// unconditional writes on denial, timeout or transport failure.
			return fmt.Errorf("%w: %v", errPublicationSuperseded, err)
		}
		var admitted admissionResponseWire
		if err := json.Unmarshal(response, &admitted); err != nil {
			return err
		}
		if !admitted.Handled {
			// Genuinely unsupported host.
			pub.legacy = true
			p.publication = pub
			return nil
		}
		if !admitted.Accepted || admitted.RequestID != meta.CallID || admitted.Token == "" {
			return fmt.Errorf("%w: %s", errPublicationSuperseded, admitted.Reason)
		}
		pub.token = admitted.Token
		pub.minted = true
	}
	pub.active = true
	p.publication = pub
	return nil
}

// resumePublication re-validates a task-carried token before any provider I/O.
func (p *Provider) resumePublication(track lyrics.TrackInfo, meta Publication, kind string) error {
	if p.publisher == nil || meta.Generation == "" || meta.Token == "" {
		p.publication = &pendingPublication{legacy: true}
		return nil
	}
	sources := p.activeSources()
	scope, ok := p.publicationScope(track, sources)
	if !ok {
		// A tagged task whose scope can no longer be computed must not fall
		// back to unadmitted direct writes.
		return errPublicationSuperseded
	}
	trackKey, _ := publicationTrackKey(track)
	request, err := json.Marshal(admissionWire{
		Version: 1, Action: "resume", RequestID: meta.CallID,
		Generation: meta.Generation, Track: trackKey, Scope: scope, Kind: kind, Token: meta.Token,
	})
	if err != nil {
		return err
	}
	response, err := p.publisher.Admit(trackKey, request)
	if err != nil {
		return err
	}
	var admitted admissionResponseWire
	if err := json.Unmarshal(response, &admitted); err != nil {
		return err
	}
	if !admitted.Handled {
		p.publication = &pendingPublication{legacy: true}
		return nil
	}
	if !admitted.Accepted || admitted.Token != meta.Token || admitted.Generation != meta.Generation {
		return errPublicationSuperseded
	}
	p.publication = &pendingPublication{
		generation: meta.Generation, token: meta.Token, trackKey: trackKey,
		scope: scope, kind: kind, active: true,
	}
	return nil
}

func (p *Provider) clearPublication() {
	if p.publication == nil {
		return
	}
	p.releasePublication()
	p.publication = nil
}

func (p *Provider) releasePublication() {
	pub := p.publication
	if pub == nil || p.publisher == nil || pub.trackKey == "" ||
		(!pub.active && (pub.generation == "" || pub.token == "")) {
		return
	}
	request, err := json.Marshal(admissionWire{
		Version: 1, Action: "release", Generation: pub.generation,
		Track: pub.trackKey, Scope: pub.scope,
		Kind: pub.kind, Token: pub.token,
	})
	if err == nil {
		_, _ = p.publisher.Admit(pub.trackKey, request)
	}
}

// storeDiscoveryBytes buffers a result write for the current publication. It
// falls back to a direct write only for genuinely unsupported hosts and for
// non-discovery control data that never reaches this path.
func (p *Provider) storeDiscoveryBytes(key string, value []byte, ttl int64) bool {
	pub := p.publication
	if pub == nil || pub.legacy || pub.failed || pub.denied {
		if pub != nil && (pub.failed || pub.denied) {
			return false
		}
		if err := p.cache.SetWithTTL(key, value, ttl); err != nil {
			p.logf(fmt.Sprintf("lyrics cache write failed: %v", p.safeDiagnosticError(err)))
			return false
		}
		return true
	}
	if len(pub.writes) >= maxPublicationWrites || pub.bytes+len(value) > maxPublicationBytes {
		p.logf("lyrics publication skipped: batch bound exceeded")
		return false
	}
	pub.writes = append(pub.writes, publicationWriteWire{Key: key, Value: append([]byte(nil), value...), TTL: ttl})
	pub.bytes += len(value)
	return true
}

func publicationDigest(writes []publicationWriteWire, trackKey string, complete bool) string {
	hash := sha256.New()
	for _, write := range writes {
		hash.Write([]byte(write.Key))
		hash.Write([]byte{0})
		hash.Write(write.Value)
		hash.Write([]byte{0})
	}
	hash.Write([]byte(trackKey))
	if complete {
		hash.Write([]byte{1})
	} else {
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func publicationRevision(generation, trackKey, digest string) string {
	sum := sha256.Sum256([]byte("navidrome-better-lyrics/publication/v1\x00" + generation + "\x00" + trackKey + "\x00" + digest))
	return fmt.Sprintf("%x", sum[:16])
}

// commitPublication publishes the buffered snapshot. A nil result means there
// was nothing to publish.
func (p *Provider) commitPublication(complete, awaitTail bool, snapshotExpiresAt int64) (Discovery, error) {
	pub := p.publication
	if pub == nil || !pub.active || len(pub.writes) == 0 {
		return Discovery{}, nil
	}
	digest := publicationDigest(pub.writes, pub.trackKey, complete)
	revision := publicationRevision(pub.generation, pub.trackKey, digest)
	request, err := json.Marshal(commitWire{
		Version: 1, Generation: pub.generation, Token: pub.token,
		Track: pub.trackKey, Scope: pub.scope, Revision: revision,
		SnapshotDigest: digest, SnapshotExpiresAt: snapshotExpiresAt,
		Complete: complete, AwaitTail: awaitTail, Writes: pub.writes,
	})
	if err != nil {
		return Discovery{}, err
	}
	if err := p.publisher.Commit(pub.trackKey, request); err != nil {
		pub.failed = true
		p.releasePublication()
		return Discovery{}, err
	}
	p.publication = nil
	return Discovery{Revision: revision, Complete: complete}, nil
}

// currentState reads the host-owned committed snapshot. It is only meaningful
// on publication-capable hosts.
func (p *Provider) currentState(track lyrics.TrackInfo) (Discovery, bool) {
	if p.publisher == nil || p.pubMeta.Generation == "" {
		return Discovery{}, false
	}
	trackKey, ok := publicationTrackKey(track)
	if !ok {
		return Discovery{}, false
	}
	scope, ok := p.publicationScope(track, p.activeSources())
	if !ok {
		return Discovery{}, false
	}
	raw, exists, err := p.publisher.Current(trackKey)
	if err != nil || !exists {
		return Discovery{}, false
	}
	var state discoveryStateWire
	if json.Unmarshal(raw, &state) != nil || !state.Handled || state.Revision == "" ||
		state.Generation != p.pubMeta.Generation || state.Scope != scope ||
		(state.ExpiresAt > 0 && state.ExpiresAt <= p.now().Unix()) {
		return Discovery{}, false
	}
	return Discovery{Revision: state.Revision, Complete: state.Complete}, true
}

// pendingSnapshotExpiry returns the dependency deadline for a commit that
// actually has buffered writes. Read-only responses have no state claim to
// bound.
func (p *Provider) pendingSnapshotExpiry(track lyrics.TrackInfo, sources []source.Source) int64 {
	if p.publication == nil || len(p.publication.writes) == 0 {
		return 0
	}
	return p.snapshotExpiry(track, sources)
}

// snapshotExpiry bounds a published revision by every cached dependency that
// fed the response, not only the writes in this batch.
func (p *Provider) snapshotExpiry(track lyrics.TrackInfo, sources []source.Source) int64 {
	now := p.now().Unix()
	expiry := int64(0)
	consider := func(expiresAt int64) {
		if expiresAt <= 0 {
			expiry = now
			return
		}
		if expiry == 0 || expiresAt < expiry {
			expiry = expiresAt
		}
	}
	if key, ok := lyricsWinnerCacheKey(track, source.IDs(sources)); ok {
		if envelope, found := p.readCachedEnvelope(key); found {
			consider(envelope.ExpiresAt)
		}
	}
	if len(sources) > 0 {
		if envelope, found := p.readCachedEnvelope(p.doneMarkerKey(track, sources)); found {
			consider(envelope.ExpiresAt)
		}
	}
	for _, src := range sources {
		if key, ok := lyricsCacheKeyForSource(track, src.ID); ok {
			if envelope, found := p.readCachedEnvelope(key); found {
				consider(envelope.ExpiresAt)
			}
		}
		if key, ok := p.sourceMissKey(track, src.ID); ok {
			if envelope, found := p.readCachedEnvelope(key); found {
				consider(envelope.ExpiresAt)
			}
		}
		if key, ok := lyricsVariantCacheKey(track, src.ID); ok {
			raw, found, err := p.cache.Get(key)
			if err != nil {
				p.logf(fmt.Sprintf("lyrics variant cache read failed: %v", p.safeDiagnosticError(err)))
			} else if found {
				p.considerBundleExpiry(raw, consider)
			}
		}
	}
	// A bundle written by this commit is not visible in the cache yet. Its
	// batch TTL is the bundle maximum, so per-entry lifetimes must be carried
	// explicitly.
	if p.publication != nil {
		for _, write := range p.publication.writes {
			if strings.HasPrefix(write.Key, lyricsVariantCacheKeyPrefix) {
				p.considerBundleExpiry(write.Value, consider)
			}
		}
	}
	if expiry != 0 && expiry < now {
		return now
	}
	return expiry
}

func (p *Provider) considerBundleExpiry(raw []byte, consider func(int64)) {
	var bundle cachedSourceBundle
	if json.Unmarshal(raw, &bundle) != nil {
		return
	}
	now := p.now().Unix()
	considered := false
	for _, entry := range bundle.Candidates {
		if entry.ExpiresAt > 0 && entry.ExpiresAt <= now {
			continue
		}
		consider(entry.ExpiresAt)
		considered = true
	}
	if !considered && len(bundle.Candidates) == 0 {
		consider(bundle.ExpiresAt)
	}
}

// controlMerge caches whether the live host applies the reserved
// @navidrome/publication/control:<key> merge. Active reports the host's
// seeded publication generation, a capability signal rather than a live
// admission. The host merges the cooldown scalar and reduces failure rows;
// a transient merge failure additionally writes the append-only observation
// fallback, which the reader consults while its marker is live.
func (p *Provider) controlMerge() (maxMerger, bool) {
	p.mergeMu.Lock()
	defer p.mergeMu.Unlock()
	if p.mergeChecked {
		return p.merger, p.merger != nil
	}
	p.mergeChecked = true
	merger, ok := p.publisher.(maxMerger)
	if !ok {
		return nil, false
	}
	reporter, ok := p.publisher.(capabilityReporter)
	if !ok {
		return nil, false
	}
	capable, err := reporter.ControlMerge()
	if err != nil || !capable {
		return nil, false
	}
	p.merger = merger
	return merger, true
}

// enqueuePublication captures the generation/token that a background task may
// resume. A task never re-reads the live active key at execution to adopt a
// newer generation.
func (p *Provider) captureEnqueuePublication() Publication {
	// Ordinary sweeps never inherit the foreground token: the child admits
	// only after the parent publication is terminal, so it cannot replay the
	// parent's phase or run against pre-commit coverage.
	if p.pubMeta.Generation != "" {
		return Publication{Generation: p.pubMeta.Generation, Kind: "sweep"}
	}
	if p.publisher != nil {
		if generation, ok, err := p.publisher.Active(); err == nil && ok {
			return Publication{Generation: generation, Kind: "sweep"}
		}
	}
	return Publication{}
}
