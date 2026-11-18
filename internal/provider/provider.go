package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

const (
	httpTimeoutMilliseconds = int32(6_000)
	// stageOneImplementedSources bounds the synchronous part of a cold lookup:
	// the first sources with an implemented fetch run here, and the background
	// sweep covers the rest without blocking the response.
	stageOneImplementedSources = 3
	stageOneLookupBudget       = 2 * time.Second
	stageOneSourceBudget       = 750 * time.Millisecond
)

type requestSender func(host.HTTPRequest) (*host.HTTPResponse, error)

// Store is the provider's view of the plugin KV store; StorageUsed feeds the
// write-size guard when the host store exposes it.
type Store interface {
	Get(key string) ([]byte, bool, error)
	SetWithTTL(key string, value []byte, ttlSeconds int64) error
}

type storeLister interface {
	List(prefix string) ([]string, error)
}

// Deps is the plugin surface the provider runs against: host HTTP, the KV
// store, the clock, config, the sweep queue, logging, and the identifying
// User-Agent. The entrypoint supplies the host-backed implementations; nil
// fields fall back to inert defaults (a transport that fails every request, a
// no-op store performing no writes).
type Deps struct {
	Send        requestSender
	Store       Store
	Publisher   Publisher
	Now         func() time.Time
	Config      ConfigLookup
	CreateQueue func(name string, concurrency int32) error
	EnqueueTask func(queue string, payload []byte) error
	Logf        func(message string)
	UserAgent   string
}

// Provider resolves lyrics across all enabled sources.
type Provider struct {
	send         requestSender
	cache        Store
	now          func() time.Time
	configLookup ConfigLookup
	logf         func(string)
	userAgent    string
	enqueueSweep func(lyrics.TrackInfo) error
	createQueue  func(name string, concurrency int32) error
	publisher    Publisher
	// publication is the current invocation's buffered writer. Every discovery
	// write goes through it; legacy hosts keep the previous direct behavior.
	publication *pendingPublication
	pubMeta     Publication
	pubKind     string
	// enqueueMeta is captured before a sweep task is enqueued and carried in
	// the payload; execution never adopts a newer generation by re-reading.
	enqueueMeta    Publication
	enqueueRefresh bool
	tailSources    []string
	tailOp         Publication
	// sources overrides the config-derived catalog for tests; nil means prod walks
	// whatever the admin configured.
	sources []source.Source
	// enqueueSweepSources re-enqueues a continuation carrying only the sources a
	// sweep had to defer when its budget ran out.
	enqueueSweepSources func(lyrics.TrackInfo, []string, int) error
	// sweepBudget overrides defaultSweepBudget (0 = default); tests shrink it to
	// force deferrals deterministically.
	sweepBudget time.Duration
	// credentialSaltBytes memoizes the per-installation HMAC salt for this
	// process so stores whose reads fail do not remint on every fingerprint.
	credentialSaltBytes []byte
	// instanceSaltBytes memoizes the identity salt; see instanceSalt.
	instanceSaltBytes []byte
}

var _ lyrics.Lyrics = (*Provider)(nil)

// New wires a provider over the host dependencies.
func New(deps Deps) *Provider {
	p := &Provider{
		send:         deps.Send,
		cache:        deps.Store,
		publisher:    deps.Publisher,
		now:          deps.Now,
		configLookup: deps.Config,
		logf:         deps.Logf,
		userAgent:    deps.UserAgent,
		createQueue:  deps.CreateQueue,
	}
	if p.cache == nil {
		p.cache = noopLyricsCache{}
	}
	if p.send == nil {
		p.send = func(host.HTTPRequest) (*host.HTTPResponse, error) {
			return nil, errors.New("no transport configured")
		}
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.configLookup == nil {
		p.configLookup = func(string) (string, bool) { return "", false }
	}
	if p.logf == nil {
		p.logf = func(string) {}
	}
	p.enqueueSweep = func(lyrics.TrackInfo) error { return nil }
	p.enqueueSweepSources = func(lyrics.TrackInfo, []string, int) error { return nil }
	if deps.EnqueueTask != nil {
		enqueue := deps.EnqueueTask
		p.enqueueSweep = func(track lyrics.TrackInfo) error {
			return enqueueSweepTask(enqueue, track, nil, 0, p.enqueueMeta, p.enqueueRefresh)
		}
		p.enqueueSweepSources = func(track lyrics.TrackInfo, sources []string, stalledPasses int) error {
			return enqueueSweepTask(enqueue, track, sources, stalledPasses, p.enqueueMeta, p.enqueueRefresh)
		}
	}
	return p
}

func enqueueSweepTask(enqueue func(queue string, payload []byte) error, track lyrics.TrackInfo, sources []string, stalledPasses int, publication Publication, refresh bool) error {
	payload, err := json.Marshal(sweepTaskPayload{Track: track, Sources: sources, StalledPasses: stalledPasses, Publication: publication, Refresh: refresh})
	if err != nil {
		return err
	}
	return enqueue(sweepQueueName, payload)
}

func (p *Provider) preparePublication(meta Publication, kind string) {
	p.publication = nil
	p.pubMeta = meta
	p.pubKind = kind
	p.enqueueMeta = Publication{}
	p.enqueueRefresh = false
	p.tailSources = nil
	p.tailOp = Publication{}
}

func (p *Provider) ensurePublication(track lyrics.TrackInfo) error {
	if p.publication != nil {
		return nil
	}
	return p.beginPublication(track, p.pubMeta, p.pubKind)
}

// GetLyrics prefers rich TTML, then synchronized provider/community results, while
// retaining plain text as the final fallback. This is the required capability
// and its response shape stays compatible with older hosts and clients. It is
// the legacy direct-write path used only when the host did not negotiate live
// publication metadata.
func (p *Provider) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	p.preparePublication(Publication{}, "legacy")
	candidate, sources, _, _, err := p.resolveLyrics(input, false)
	return p.responsesWithVariants(input.Track, sources, candidate), err
}

// GetLyricsPublished is the optional classic-capable export. It publishes
// through the host's conditional admission protocol while preserving the
// classic response shape.
func (p *Provider) GetLyricsPublished(input lyrics.GetLyricsRequest, meta Publication) (lyrics.GetLyricsResponse, error) {
	p.preparePublication(meta, "classic")
	candidate, sources, _, sweepComplete, err := p.resolveLyrics(input, false)
	if errors.Is(err, errPublicationSuperseded) {
		return lyrics.GetLyricsResponse{}, err
	}
	if _, commitErr := p.commitPublication(sweepComplete, false, p.pendingSnapshotExpiry(input.Track, sources)); commitErr != nil {
		// The classic host cache must not retain a rejected publication.
		return lyrics.GetLyricsResponse{}, commitErr
	}
	if p.publication != nil && p.publication.active && p.enqueueMeta.Token == "" {
		// A zero-write classic lookup without a queued sweep has no deferred
		// owner for its admission; release the slot instead of waiting out
		// the host TTL.
		p.releasePublication()
		p.publication = nil
	}
	if current, ok := p.currentV1Response(input.Track); ok {
		return current, err
	}
	return p.responsesWithVariants(input.Track, sources, candidate), err
}

// GetLyricsV2 is the optional playback export: the same selection plus a
// versioned identity on every entry and every cached representation from each
// source. It resolves once and does not add a second fetch.
func (p *Provider) GetLyricsV2(input lyrics.GetLyricsRequest) (V2Response, error) {
	return p.GetLyricsV2Published(input, Publication{}, false)
}

func (p *Provider) GetLyricsV2Published(input lyrics.GetLyricsRequest, meta Publication, refresh bool) (V2Response, error) {
	kind := "normal"
	if refresh {
		kind = "refresh"
	}
	p.preparePublication(meta, kind)
	if refresh {
		if err := p.ensurePublication(input.Track); err != nil {
			if current, discovery, ok := p.currentV2Response(input.Track); ok {
				current.Discovery = discovery
				return current, nil
			}
			return V2Response{}, err
		}
	}
	candidate, sources, evidence, sweepComplete, err := p.resolveLyrics(input, refresh)
	if errors.Is(err, errPublicationSuperseded) {
		if current, discovery, ok := p.currentV2Response(input.Track); ok {
			current.Discovery = discovery
			return current, nil
		}
		return V2Response{}, err
	}
	tailSources := p.tailSources
	tailOp := p.tailOp
	committedDiscovery, commitErr := p.commitPublication(
		sweepComplete, refresh && len(tailSources) > 0, p.pendingSnapshotExpiry(input.Track, sources),
	)
	if refresh && commitErr == nil {
		if len(tailSources) > 0 {
			if err := p.enqueueRefreshTailWithOp(input.Track, tailSources, tailOp); err != nil {
				p.logf(fmt.Sprintf("refresh tail enqueue failed: %v", p.safeDiagnosticError(err)))
				p.releaseOperation(input.Track, tailOp)
			}
		} else if p.enqueueMeta.Token == "" {
			// A refresh that resolved entirely from cache has no commit to
			// release its admission; release it here so foreground work is
			// not blocked for the host TTL. A queued full sweep keeps the
			// token so it can finish the uncovered sources.
			p.releasePublication()
			p.publication = nil
		}
	} else if commitErr == nil && p.publication != nil && p.publication.active && p.enqueueMeta.Token == "" {
		// A normal zero-write lookup without a queued sweep has no deferred
		// owner for its admission.
		p.releasePublication()
		p.publication = nil
	}
	discovery := committedDiscovery
	if commitErr != nil {
		if current, currentDiscovery, ok := p.currentV2Response(input.Track); ok {
			current.Discovery = currentDiscovery
			return current, nil
		}
		// Uncommitted lyrics are returned without claiming a discovery snapshot.
		discovery = Discovery{}
	} else if discovery.Revision != "" {
		if current, currentDiscovery, ok := p.currentV2Response(input.Track); ok {
			current.Discovery = currentDiscovery
			return current, err
		}
	} else if current, currentDiscovery, ok := p.currentV2Response(input.Track); ok {
		current.Discovery = currentDiscovery
		return current, err
	}
	response := p.buildV2Response(input.Track, sources, candidate)
	response.Evidence = dedupeEvidence(append(evidence, response.Evidence...))
	if discovery.Revision != "" {
		// The cached view was unavailable; bound the claim to the state this
		// invocation committed instead of assuming a later snapshot matches.
		if current, ok := p.currentState(input.Track); !ok ||
			current.Revision != discovery.Revision || current.Complete != discovery.Complete {
			discovery = Discovery{}
		}
	}
	response.Discovery = discovery
	if err != nil && len(response.Entries) == 0 && len(response.Evidence) == 0 {
		return V2Response{}, err
	}
	return response, nil
}

func (p *Provider) currentV1Response(track lyrics.TrackInfo) (lyrics.GetLyricsResponse, bool) {
	sources := p.activeSources()
	if len(sources) == 0 {
		return lyrics.GetLyricsResponse{}, false
	}
	candidate, _, ok, _ := p.readCachedWinner(track, sources)
	if !ok {
		best, _ := p.readBestFromSourceCache(track, sources)
		if !best.Present() {
			return lyrics.GetLyricsResponse{}, false
		}
		candidate = best
	}
	return p.responsesWithVariants(track, sources, candidate), true
}

func (p *Provider) currentV2Response(track lyrics.TrackInfo) (V2Response, Discovery, bool) {
	sources := p.activeSources()
	if len(sources) == 0 {
		return V2Response{}, Discovery{}, false
	}
	beforeCandidate, beforeEvidence, beforeComplete, beforeCached := p.readResponseView(track, sources)
	if !beforeCached {
		return V2Response{}, Discovery{}, false
	}
	before, beforeOK := p.currentState(track)
	response := p.buildV2Response(track, sources, beforeCandidate)
	response.Evidence = dedupeEvidence(append(beforeEvidence, response.Evidence...))
	after, afterOK := p.currentState(track)
	afterCandidate, afterEvidence, afterComplete, afterCached := p.readResponseView(track, sources)
	// A commit may land between the cached read and the state read. Only a
	// state whose revision and dependencies both survived the build may label
	// this response; otherwise the claim is omitted rather than mismatched.
	if beforeOK && afterOK && before == after && afterCached &&
		before.Complete == beforeComplete && after.Complete == afterComplete &&
		beforeComplete == afterComplete &&
		sameCachedView(beforeCandidate, beforeEvidence, afterCandidate, afterEvidence) {
		return response, before, true
	}
	return response, Discovery{}, true
}

// readResponseView is the cached winner/evidence pair a published response is
// built from. It preserves the winner's evidence even when a per-source entry
// is preferred.
func (p *Provider) readResponseView(track lyrics.TrackInfo, sources []source.Source) (lyric.Candidate, []lyric.Evidence, bool, bool) {
	candidate, evidence, ok, sweepComplete := p.readCachedWinner(track, sources)
	complete := sweepComplete || p.sweepMarkerActive(p.doneMarkerKey(track, sources))
	if !ok {
		best, _ := p.readBestFromSourceCache(track, sources)
		if !best.Present() {
			return lyric.Candidate{}, nil, false, false
		}
		candidate = best
	}
	return candidate, evidence, complete, true
}

func sameCachedView(a lyric.Candidate, aEvidence []lyric.Evidence, b lyric.Candidate, bEvidence []lyric.Evidence) bool {
	aValue, aErr := json.Marshal(a)
	bValue, bErr := json.Marshal(b)
	if aErr != nil || bErr != nil || !bytes.Equal(aValue, bValue) {
		return false
	}
	aEvidenceValue, aErr := json.Marshal(aEvidence)
	bEvidenceValue, bErr := json.Marshal(bEvidence)
	return aErr == nil && bErr == nil && bytes.Equal(aEvidenceValue, bEvidenceValue)
}

// resolveLyrics selects the servable candidate and keeps the source order for
// callers that build variant responses.
func (p *Provider) resolveLyrics(input lyrics.GetLyricsRequest, refresh bool) (lyric.Candidate, []source.Source, []lyric.Evidence, bool, error) {
	track := input.Track
	if title := strings.TrimSpace(track.Title); title == "" || lyric.TrackArtist(track) == "" {
		return lyric.Candidate{}, nil, nil, false, nil
	}

	sources := p.activeSources()
	if len(sources) == 0 {
		// Deliberate all-disabled opt-out (every configured source toggled off):
		// serve nothing without touching the winner cache or task queue — an
		// empty source order creates no key and would wedge every lookup
		// through the unschedulable sweep path.
		return lyric.Candidate{}, sources, nil, false, nil
	}

	if refresh {
		// Refresh ignores negative winner/coverage but preserves fresh positives
		// and provider guards. The host admission token was already minted.
		if best, expiry := p.readBestFromSourceCache(track, sources); best.Present() {
			if err := p.ensurePublication(track); err != nil {
				return lyric.Candidate{}, nil, nil, false, err
			}
			// Re-read after admission: another refresh may have replaced the
			// positive while this lookup was waiting for its admission.
			if fresh, freshExpiry := p.readBestFromSourceCache(track, sources); fresh.Present() {
				best, expiry = fresh, freshExpiry
			} else {
				return lyric.Candidate{}, sources, nil, false, nil
			}
			complete := p.discoveryComplete(track, sources)
			p.cacheWinnerUntil(track, sources, best, complete, expiry, nil)
			p.prepareRefreshTail(track, sources, nil)
			return best, sources, p.sourceEvidence(track, sources, nil), complete, nil
		}
	} else if candidate, evidence, ok, sweepComplete := p.readCachedWinner(track, sources); ok {
		completion := sweepComplete
		if !completion {
			completion = p.sweepMarkerActive(p.doneMarkerKey(track, sources))
		}
		if candidate.Present() && !candidate.Playable() {
			// Defensive: no assessed ambiguous/mismatch result may be served
			// from a cache written by an older or faulty path.
			candidate = lyric.Candidate{}
		}
		if candidate.Present() {
			if !sweepComplete {
				// The sweep may have cached a better result after this winner
				// was written: re-rank before serving.
				if best, expiry := p.readBestFromSourceCache(track, sources); lyric.AtLeastAsPreferred(candidate, best) {
					if err := p.ensurePublication(track); err != nil {
						return candidate, sources, p.sourceEvidence(track, sources, evidence), completion, nil
					}
					fresh, freshEvidence, freshComplete, hasFresh := p.readCachedView(track, sources)
					if !hasFresh {
						// The cache changed under the read; never publish the
						// pre-admission candidate as fresh state.
						return lyric.Candidate{}, sources, nil, false, nil
					}
					if fresh.Present() {
						if freshBest, freshExpiry := p.readBestFromSourceCache(track, sources); freshBest.Present() && lyric.AtLeastAsPreferred(candidate, freshBest) {
							if freshExpiry > 0 {
								expiry = freshExpiry
							}
							p.cacheWinnerUntil(track, sources, fresh, freshComplete, expiry, freshEvidence)
						}
					}
					candidate, evidence, completion = fresh, freshEvidence, freshComplete
				}
				p.maybeEnqueueSweep(track)
			}
			return candidate, sources, p.sourceEvidence(track, sources, evidence), completion, nil
		}
		// A cached miss must not shield fresh sweep results: the background sweep
		// may have written better candidates per source after the miss landed.
		if best, expiry := p.readBestFromSourceCache(track, sources); best.Present() {
			if err := p.ensurePublication(track); err != nil {
				return best, sources, p.sourceEvidence(track, sources, evidence), completion, nil
			}
			fresh, freshEvidence, freshComplete, hasFresh := p.readCachedView(track, sources)
			if !hasFresh {
				return lyric.Candidate{}, sources, nil, false, nil
			}
			if fresh.Present() {
				if _, freshExpiry := p.readBestFromSourceCache(track, sources); freshExpiry > 0 {
					expiry = freshExpiry
				}
				p.cacheWinnerUntil(track, sources, fresh, freshComplete, expiry, freshEvidence)
			}
			return fresh, sources, p.sourceEvidence(track, sources, freshEvidence), freshComplete, nil
		}
		// No per-source improvement yet: the negative winner stays authoritative
		// for its TTL so replays do not re-walk the sources — but an incomplete
		// sweep must still be schedulable.
		if !completion {
			p.maybeEnqueueSweep(track)
		}
		return candidate, sources, p.sourceEvidence(track, sources, evidence), completion, nil
	}
	if best, expiry := p.readBestFromSourceCache(track, sources); best.Present() {
		if err := p.ensurePublication(track); err != nil {
			return best, sources, p.sourceEvidence(track, sources, nil), false, nil
		}
		fresh, freshEvidence, freshComplete, hasFresh := p.readCachedView(track, sources)
		if !hasFresh || !fresh.Present() {
			return lyric.Candidate{}, sources, nil, false, nil
		}
		p.cacheWinnerUntil(track, sources, fresh, freshComplete, expiry, nil)
		p.maybeEnqueueSweep(track)
		return fresh, sources, p.sourceEvidence(track, sources, freshEvidence), freshComplete, nil
	}

	if err := p.ensurePublication(track); err != nil {
		return lyric.Candidate{}, sources, nil, false, err
	}
	candidate, health, coveredAll, evidence, attempted, err := p.fetchLyrics(input, refresh)
	completion := coveredAll
	if !completion {
		completion = p.sweepMarkerActive(p.doneMarkerKey(track, sources))
	}
	if refresh {
		p.prepareRefreshTail(track, sources, attempted)
	}
	if candidate.Present() {
		p.cacheWinnerUntil(track, sources, candidate, completion, p.now().Add(time.Duration(sourceCacheTTLSeconds(candidate.Quality))*time.Second).Unix(), evidence)
		if !completion && !refresh {
			p.maybeEnqueueSweep(track)
		}
		return candidate, sources, p.sourceEvidence(track, sources, evidence), completion, nil
	}
	if err == nil && health < source.Degraded {
		p.cacheNegativeWinner(track, sources, completion, evidence)
		if !completion {
			// A fully covered negative pass needs no sweep: every implemented
			// source already answered inside stage one.
			p.maybeEnqueueSweep(track)
		}
	} else if !candidate.Present() && len(evidence) > 0 {
		// Preserve a successful assertion across another source's outage while
		// recording the pass's actual coverage; the active marker still governs
		// whether a later sweep is needed.
		p.cacheNegativeWinner(track, sources, completion, evidence)
		p.maybeEnqueueSweep(track)
	} else {
		// Retain the error, but an incomplete walk must still reach the tail:
		// schedule the sweep so the returned error is the only failure the
		// request exposes.
		p.maybeEnqueueSweep(track)
	}
	if !candidate.Present() && len(evidence) > 0 {
		// A successful assertion from one source survives another source's
		// failed attempt. The evidence was not inferred from the empty/error
		// result, so it remains visible and does not become a clean negative.
		return candidate, sources, p.sourceEvidence(track, sources, evidence), completion, nil
	}
	if err == nil && health >= source.Degraded {
		err = errors.New("lyrics sources temporarily unavailable")
	}
	return candidate, sources, p.sourceEvidence(track, sources, evidence), completion, p.safeDiagnosticError(err)
}

// readCachedView re-reads the winner and best source after lazy admission so
// callers rank the current cache. Callers omit a pre-admission candidate when
// no fresh view remains.
func (p *Provider) readCachedView(track lyrics.TrackInfo, sources []source.Source) (lyric.Candidate, []lyric.Evidence, bool, bool) {
	winner, evidence, ok, sweepComplete := p.readCachedWinner(track, sources)
	complete := sweepComplete || p.sweepMarkerActive(p.doneMarkerKey(track, sources))
	best, _ := p.readBestFromSourceCache(track, sources)
	if best.Present() && (!winner.Present() || lyric.AtLeastAsPreferred(winner, best)) {
		return best, evidence, complete, true
	}
	if winner.Present() || ok {
		return winner, evidence, complete, true
	}
	return lyric.Candidate{}, nil, false, false
}

// activeSources resolves the source order for this lookup: the injected test
// catalog when present, otherwise the admin-configured enabled sources.
func (p *Provider) activeSources() []source.Source {
	if p.sources != nil {
		return p.sources
	}
	return readProviderConfig(p.configLookup).Sources
}

// fetchLyrics runs the stage-one pass: the first implemented enabled sources in
// configured order under a bounded budget, stopping at the first usable
// synchronized result. Each successful source result is cached per source so later
// lookups and source reordering re-rank without refetching. Remaining sources
// are covered by the background sweep.
func (p *Provider) fetchLyrics(input lyrics.GetLyricsRequest, refresh bool) (lyric.Candidate, source.Health, bool, []lyric.Evidence, []string, error) {
	track := input.Track
	if title := strings.TrimSpace(track.Title); title == "" || lyric.TrackArtist(track) == "" {
		return lyric.Candidate{}, source.Healthy, false, nil, nil, nil
	}

	sources := p.activeSources()
	deadline := p.now().Add(stageOneLookupBudget)

	var (
		best         lyric.Candidate
		health       = source.Healthy
		lookupErrors []error
		walked       int
		walkedTotal  int
		attempted    []string
		// coldHosts records hosts behind a global rate limit observed during this
		// walk. Later sources on a cold host are skipped for the rest of this
		// lookup, matching README's documented Better Lyrics shared cooldown.
		coldHosts = map[string]bool{}
		evidence  []lyric.Evidence
	)
	implementedTotal := 0
	for _, src := range sources {
		if src.Fetch != nil {
			implementedTotal++
		}
	}
	for _, src := range sources {
		if walked >= stageOneImplementedSources {
			break
		}
		if src.Fetch == nil {
			continue
		}
		// A per-source entry (positive or persisted miss) already covers this
		// source for its TTL: refetching here would re-query providers every
		// negative-winner cycle. Explicit refresh retries negative coverage but
		// still reuses fresh positives fetched under the current credential.
		covered := false
		if refresh {
			_, covered = p.readCachedSourcePositive(track, src.ID)
		} else {
			_, covered = p.readCachedSourceCoverage(track, src.ID)
		}
		if covered {
			walkedTotal++
			continue
		}
		if coldHostFor(src, coldHosts) != "" {
			health = health.Merge(source.TemporarilySkipped)
			continue
		}
		if p.sourceInCooldown(src) || p.sourceFailureBackoff(src.ID) > 0 {
			// Cooldowns are temporary; skipping keeps the slot for the next
			// implemented source and the sweep retries it later.
			health = health.Merge(source.TemporarilySkipped)
			continue
		}
		if !p.now().Before(deadline) {
			lookupErrors = append(lookupErrors, source.ErrLookupBudgetExhausted)
			break
		}
		walked++
		attempted = append(attempted, src.ID)
		started := p.now()
		sourceDeadline := minTime(deadline, started.Add(stageOneSourceBudget))
		candidates, sourceHealth, err := src.Fetch(p, track, sourceDeadline)
		sourceFailed := err != nil
		health = health.Merge(sourceHealth)
		if sourceFailed {
			lookupErrors = append(lookupErrors, err)
			health = health.Merge(source.Degraded)
			// The short foreground slot may expire for a healthy, slower
			// source. Let its full-budget sweep retry without outage backoff.
			if p.now().Before(sourceDeadline) {
				p.rememberSourceFailure(src.ID, started, err)
			}
			p.logSourceFailure(src.ID, started, err)
			if source.IsGlobalRateLimitError(err) {
				for _, hostName := range src.Hosts {
					coldHosts[hostName] = true
				}
			}
		} else if sourceHealth <= source.Uncertain {
			p.resetSourceFailureBefore(src.ID, started)
		}
		foundCandidate := false
		foundEvidence := false
		assessed := make([]lyric.Candidate, 0, len(candidates))
		for index := range candidates {
			candidate := &candidates[index]
			p.assessCandidate(track, candidate)
			if len(candidate.Evidence) > 0 {
				foundEvidence = true
			}
			evidence = append(evidence, candidate.Evidence...)
			if candidate.Present() && candidate.Match.Status == lyric.MatchMismatch {
				continue
			}
			if candidate.Present() {
				foundCandidate = true
				p.prepareCandidate(track, src.ID, candidate)
			}
			assessed = append(assessed, *candidate)
			if candidate.Playable() {
				best = lyric.Prefer(best, *candidate)
			}
		}
		storedAny := true
		if len(assessed) > 0 {
			storedAny = p.cacheSourceCandidates(track, src.ID, assessed)
		}
		coveredThisSource := !sourceFailed && storedAny
		if !foundCandidate && !sourceFailed {
			switch sourceHealth {
			case source.Healthy, source.Uncertain:
				if !foundEvidence {
					p.invalidateSourceObservations(track, src.ID)
				}
				// Healthy clean misses and anonymous cache-only misses are
				// bounded coverage: persist them so the sweep and later
				// lookups skip the refetch. The miss keys
				// carry the credential fingerprint, so a configured key or
				// token re-authorizes them immediately.
				if !p.cacheSourceMiss(track, src.ID) {
					coveredThisSource = false
				}
			case source.TemporarilySkipped:
				// Token rejects are not covered work: the next lookup remints
				// and retries, and the pass must not report complete
				// foreground coverage or suppress the sweep.
				health = health.Merge(sourceHealth)
				coveredThisSource = false
			}
		}
		if sourceFailed {
			coveredThisSource = false
		}
		if coveredThisSource {
			walkedTotal++
		}
		if best.Present() && best.Quality >= lyric.TimingLine {
			return best, health, walkedTotal >= implementedTotal, evidence, attempted, nil
		}
	}

	if best.Present() {
		return best, health, walkedTotal >= implementedTotal, evidence, attempted, nil
	}
	if len(lookupErrors) > 0 {
		return lyric.Candidate{}, health, false, evidence, attempted, errors.Join(lookupErrors...)
	}
	// A clean all-miss walk with full coverage is real: the negative winner
	// must record that no sweep needs re-walking the same misses.
	return lyric.Candidate{}, health, walkedTotal >= implementedTotal, evidence, attempted, nil
}

// assessCandidate applies the recording-identity rule after the source has
// returned metadata. Sources that expose no recording metadata remain
// unassessed and legacy-playable; assessed incomplete data is ambiguous.
func (p *Provider) assessCandidate(track lyrics.TrackInfo, candidate *lyric.Candidate) {
	if candidate == nil {
		return
	}
	if candidate.RecordingInfo.Empty() {
		if candidate.Match.Status == "" {
			candidate.Match = lyric.RecordingMatch{Status: lyric.MatchUnassessed}
		}
	} else {
		candidate.Match = lyric.AssessRecording(lyric.RequestRecordingInfo(track), candidate.RecordingInfo)
	}
	for index := range candidate.Evidence {
		candidate.Evidence[index].Match = candidate.Match
	}
}

func minTime(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}

func (p *Provider) logSourceFailure(sourceID string, started time.Time, err error) {
	class := "upstream"
	if errors.Is(err, source.ErrLookupBudgetExhausted) {
		class = "budget"
	} else if source.IsRateLimitError(err) {
		class = "rate-limit"
	}
	p.logf(fmt.Sprintf("lyrics source=%s class=%s elapsed_ms=%d error=%v", sourceID, class, p.now().Sub(started).Milliseconds(), p.safeDiagnosticError(err)))
}

func coldHostFor(src source.Source, coldHosts map[string]bool) string {
	for _, hostName := range src.Hosts {
		if coldHosts[hostName] {
			return hostName
		}
	}
	return ""
}

func (p *Provider) requestTimeoutMilliseconds(deadline time.Time) (int32, error) {
	remaining := deadline.Sub(p.now())
	if remaining <= 0 {
		return 0, source.ErrLookupBudgetExhausted
	}
	perRequest := min(remaining, time.Duration(httpTimeoutMilliseconds)*time.Millisecond)
	milliseconds := max((perRequest+time.Millisecond-1)/time.Millisecond, 1)
	return int32(milliseconds), nil
}

// DefaultHeaders is the plugin's common header set. The identifying User-Agent
// is required by providers that throttle unidentified clients (LRCLib).
func (p *Provider) DefaultHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": p.userAgent,
	}
}
