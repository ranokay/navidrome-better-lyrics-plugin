package provider

import (
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

// Deps is the plugin surface the provider runs against: host HTTP, the KV
// store, the clock, config, the sweep queue, logging, and the identifying
// User-Agent. The entrypoint supplies the host-backed implementations; nil
// fields fall back to inert defaults (a transport that fails every request, a
// no-op store performing no writes).
type Deps struct {
	Send        requestSender
	Store       Store
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
			return enqueueSweepTask(enqueue, track, nil, 0)
		}
		p.enqueueSweepSources = func(track lyrics.TrackInfo, sources []string, stalledPasses int) error {
			return enqueueSweepTask(enqueue, track, sources, stalledPasses)
		}
	}
	return p
}

func enqueueSweepTask(enqueue func(queue string, payload []byte) error, track lyrics.TrackInfo, sources []string, stalledPasses int) error {
	payload, err := json.Marshal(sweepTaskPayload{Track: track, Sources: sources, StalledPasses: stalledPasses})
	if err != nil {
		return err
	}
	return enqueue(sweepQueueName, payload)
}

// GetLyrics prefers rich TTML, then synchronized provider/community results, while
// retaining plain text as the final fallback. This is the required capability
// and its response shape stays compatible with older hosts and clients.
func (p *Provider) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	candidate, sources, _, err := p.resolveLyrics(input)
	return p.responsesWithVariants(input.Track, sources, candidate), err
}

// GetLyricsV2 is the optional playback export: the same selection plus a
// versioned identity on every entry and every cached representation from each
// source. It resolves once and does not add a second fetch.
func (p *Provider) GetLyricsV2(input lyrics.GetLyricsRequest) (V2Response, error) {
	candidate, sources, evidence, err := p.resolveLyrics(input)
	response := p.buildV2Response(input.Track, sources, candidate)
	response.Evidence = dedupeEvidence(append(evidence, response.Evidence...))
	if err != nil && len(response.Entries) == 0 && len(response.Evidence) == 0 {
		return V2Response{}, err
	}
	return response, nil
}

// resolveLyrics selects the servable candidate and keeps the source order for
// callers that build variant responses.
func (p *Provider) resolveLyrics(input lyrics.GetLyricsRequest) (lyric.Candidate, []source.Source, []lyric.Evidence, error) {
	track := input.Track
	if title := strings.TrimSpace(track.Title); title == "" || lyric.TrackArtist(track) == "" {
		return lyric.Candidate{}, nil, nil, nil
	}

	sources := p.activeSources()
	if len(sources) == 0 {
		// Deliberate all-disabled opt-out (every configured source toggled off):
		// serve nothing without touching the winner cache or task queue — an
		// empty source order creates no key and would wedge every lookup
		// through the unschedulable sweep path.
		return lyric.Candidate{}, sources, nil, nil
	}

	if candidate, evidence, ok, sweepComplete := p.readCachedWinner(track, sources); ok {
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
					p.cacheWinnerUntil(track, sources, best, false, expiry, evidence)
					candidate = best
				}
				p.maybeEnqueueSweep(track)
			}
			return candidate, sources, p.sourceEvidence(track, sources, evidence), nil
		}
		// A cached miss must not shield fresh sweep results: the background sweep
		// may have written better candidates per source after the miss landed.
		if best, expiry := p.readBestFromSourceCache(track, sources); best.Present() {
			p.cacheWinnerUntil(track, sources, best, false, expiry, evidence)
			p.maybeEnqueueSweep(track)
			return best, sources, p.sourceEvidence(track, sources, evidence), nil
		}
		// No per-source improvement yet: the negative winner stays authoritative
		// for its TTL so replays do not re-walk the sources — but an incomplete
		// sweep must still be schedulable.
		if !sweepComplete {
			p.maybeEnqueueSweep(track)
		}
		return candidate, sources, p.sourceEvidence(track, sources, evidence), nil
	}
	if best, expiry := p.readBestFromSourceCache(track, sources); best.Present() {
		p.cacheWinnerUntil(track, sources, best, false, expiry, nil)
		p.maybeEnqueueSweep(track)
		return best, sources, p.sourceEvidence(track, sources, nil), nil
	}

	candidate, health, coveredAll, evidence, err := p.fetchLyrics(input)
	if candidate.Present() {
		p.cacheWinnerUntil(track, sources, candidate, coveredAll, p.now().Add(time.Duration(sourceCacheTTLSeconds(candidate.Quality))*time.Second).Unix(), evidence)
		if !coveredAll {
			p.maybeEnqueueSweep(track)
		}
		return candidate, sources, p.sourceEvidence(track, sources, evidence), nil
	}
	if err == nil && health < source.Degraded {
		p.cacheNegativeWinner(track, sources, coveredAll, evidence)
		if !coveredAll {
			// A fully covered negative pass needs no sweep: every implemented
			// source already answered inside stage one.
			p.maybeEnqueueSweep(track)
		}
	} else if !candidate.Present() && len(evidence) > 0 {
		// Preserve a successful assertion across another source's outage while
		// keeping the pass explicitly incomplete for retry/sweep purposes.
		p.cacheNegativeWinner(track, sources, false, evidence)
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
		return candidate, sources, p.sourceEvidence(track, sources, evidence), nil
	}
	if err == nil && health >= source.Degraded {
		err = errors.New("lyrics sources temporarily unavailable")
	}
	return candidate, sources, p.sourceEvidence(track, sources, evidence), p.safeDiagnosticError(err)
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
func (p *Provider) fetchLyrics(input lyrics.GetLyricsRequest) (lyric.Candidate, source.Health, bool, []lyric.Evidence, error) {
	track := input.Track
	if title := strings.TrimSpace(track.Title); title == "" || lyric.TrackArtist(track) == "" {
		return lyric.Candidate{}, source.Healthy, false, nil, nil
	}

	sources := p.activeSources()
	deadline := p.now().Add(stageOneLookupBudget)

	var (
		best         lyric.Candidate
		health       = source.Healthy
		lookupErrors []error
		walked       int
		walkedTotal  int
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
		// negative-winner cycle. Cached positives are served by the
		// re-rank above the walk.
		if _, ok := p.readCachedSourceCoverage(track, src.ID); ok {
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
				p.rememberSourceFailure(src.ID, err)
			}
			p.logSourceFailure(src.ID, started, err)
			if source.IsGlobalRateLimitError(err) {
				for _, hostName := range src.Hosts {
					coldHosts[hostName] = true
				}
			}
		} else if sourceHealth <= source.Uncertain {
			p.resetSourceFailure(src.ID)
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
		if len(assessed) > 0 {
			p.cacheSourceCandidates(track, src.ID, assessed)
		}
		coveredThisSource := !sourceFailed
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
				p.cacheSourceMiss(track, src.ID)
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
			return best, health, walkedTotal >= implementedTotal, evidence, nil
		}
	}

	if best.Present() {
		return best, health, walkedTotal >= implementedTotal, evidence, nil
	}
	if len(lookupErrors) > 0 {
		return lyric.Candidate{}, health, false, evidence, errors.Join(lookupErrors...)
	}
	// A clean all-miss walk with full coverage is real: the negative winner
	// must record that no sweep needs re-walking the same misses.
	return lyric.Candidate{}, health, walkedTotal >= implementedTotal, evidence, nil
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
