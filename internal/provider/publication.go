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
	Version        int                    `json:"version"`
	Generation     string                 `json:"generation"`
	Token          string                 `json:"token"`
	Track          string                 `json:"track"`
	Scope          string                 `json:"scope"`
	Revision       string                 `json:"revision"`
	SnapshotDigest string                 `json:"snapshotDigest"`
	Complete       bool                   `json:"complete"`
	Writes         []publicationWriteWire `json:"writes"`
}

type discoveryStateWire struct {
	Handled  bool   `json:"handled"`
	Revision string `json:"revision,omitempty"`
	Complete bool   `json:"complete,omitempty"`
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
	scope, ok := p.publicationScope(track, sources)
	if !ok {
		p.publication = &pendingPublication{legacy: true}
		return nil
	}
	trackKey, _ := publicationTrackKey(track)
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
		p.publication = &pendingPublication{legacy: true}
		return nil
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

func (p *Provider) releasePublication() {
	if p.publication == nil || !p.publication.active || p.publisher == nil {
		return
	}
	request, err := json.Marshal(admissionWire{
		Version: 1, Action: "release", Generation: p.publication.generation,
		Track: p.publication.trackKey, Scope: p.publication.scope,
		Kind: p.publication.kind, Token: p.publication.token,
	})
	if err == nil {
		_, _ = p.publisher.Admit(p.publication.trackKey, request)
	}
}

// storeDiscoveryBytes buffers a result write for the current publication. It
// falls back to a direct write only for genuinely unsupported hosts and for
// non-discovery control data that never reaches this path.
func (p *Provider) storeDiscoveryBytes(key string, value []byte, ttl int64) bool {
	pub := p.publication
	if pub == nil || pub.legacy || pub.failed {
		if pub != nil && pub.failed {
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
func (p *Provider) commitPublication(complete bool) (Discovery, error) {
	pub := p.publication
	if pub == nil || !pub.active || len(pub.writes) == 0 {
		return Discovery{}, nil
	}
	digest := publicationDigest(pub.writes, pub.trackKey, complete)
	revision := publicationRevision(pub.generation, pub.trackKey, digest)
	request, err := json.Marshal(commitWire{
		Version: 1, Generation: pub.generation, Token: pub.token,
		Track: pub.trackKey, Scope: pub.scope, Revision: revision,
		SnapshotDigest: digest, Complete: complete, Writes: pub.writes,
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
	if p.publisher == nil {
		return Discovery{}, false
	}
	trackKey, ok := publicationTrackKey(track)
	if !ok {
		return Discovery{}, false
	}
	raw, exists, err := p.publisher.Current(trackKey)
	if err != nil || !exists {
		return Discovery{}, false
	}
	var state discoveryStateWire
	if json.Unmarshal(raw, &state) != nil || !state.Handled || state.Revision == "" {
		return Discovery{}, false
	}
	return Discovery{Revision: state.Revision, Complete: state.Complete}, true
}

// enqueuePublication captures the generation/token that a background task may
// resume. A task never re-reads the live active key at execution to adopt a
// newer generation.
func (p *Provider) captureEnqueuePublication() Publication {
	if p.publication != nil && p.publication.active {
		return Publication{
			Generation: p.publication.generation,
			Token:      p.publication.token,
			Kind:       "sweep",
		}
	}
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
