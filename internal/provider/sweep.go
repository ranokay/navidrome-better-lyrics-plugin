package provider

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

const (
	sweepQueueName          = "lyrics-sweep"
	sweepMarkerPrefix       = "sweep:"
	sweepDonePrefix         = "sweep-done:"
	refreshTailMarkerPrefix = "refresh-tail:"
	// sweepDoneTTL is the ceiling for a completed sweep's marker; the write
	// clamps it to the shortest cached-result TTL the sweep actually covered
	// so a marker never outlives the results it stands for.
	sweepDoneTTL = int64(24 * time.Hour / time.Second)
	// sweepMarkerTTL matches the negative winner TTL so a fresh miss lookup can
	// re-trigger a sweep once its marker expires.
	sweepMarkerTTL = int64(5 * time.Minute / time.Second)
	// sweepQueuedMarkerTTL keeps the pending marker valid while a task waits
	// behind a concurrency-1 backlog; OnTaskExecute refreshes it again when
	// execution starts and releases it when an incomplete run finishes.
	sweepQueuedMarkerTTL = int64(30 * time.Minute / time.Second)
	// A source that cannot finish within a fresh full budget must not keep a
	// concurrency-one queue occupied forever. A later lookup may start again.
	maxSweepStalledPasses = 2
)

// defaultSweepBudget bounds one sweep invocation. The task worker's own callback
// has no declared deadline, so the sweep stays polite on its own: ~24 s keeps it
// well under Navidrome's per-invocation comfort zone even on a full miss.
const defaultSweepBudget = 24 * time.Second

type sweepTaskPayload struct {
	Track         lyrics.TrackInfo `json:"track"`
	Sources       []string         `json:"sources,omitempty"`
	StalledPasses int              `json:"stalledPasses,omitempty"`
	Publication   Publication      `json:"publication,omitempty"`
	Refresh       bool             `json:"refresh,omitempty"`
}

// OnInit purges cached data from removed sources and creates the permanent
// sweep queue. Failing here is non-fatal: lyrics keep working and the queue
// lifecycle can be repaired by reloading the plugin.
func (p *Provider) OnInit() error {
	p.purgeLegacyMatchingCache()
	p.purgeRemovedSourceCache()
	if p.createQueue != nil {
		if err := p.createQueue(sweepQueueName, 1); err != nil {
			p.logf(fmt.Sprintf("lyrics sweep queue creation failed; background source discovery unavailable; reload the plugin after checking task-worker configuration: %v", p.safeDiagnosticError(err)))
		}
	}
	return nil
}

// OnTaskExecute dispatches queued sweeps; tasks from other queues are ignored so a
// misconfigured queue cannot trigger unrelated fetches.
func (p *Provider) OnTaskExecute(req taskworker.TaskExecuteRequest) (string, error) {
	if req.QueueName != sweepQueueName {
		return "ignored queue " + req.QueueName, nil
	}
	var payload sweepTaskPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode sweep payload: %w", err)
	}
	kind := "sweep"
	if payload.Refresh {
		kind = "tail"
	}
	p.preparePublication(payload.Publication, kind)
	if payload.Publication.Generation == "" {
		if p.publisher != nil {
			capable, err := p.publisher.Probe()
			if err != nil || capable {
				if err != nil {
					p.logf(fmt.Sprintf("publication probe failed: %v", p.safeDiagnosticError(err)))
				}
				// A retained pre-upgrade task has no live generation. On a
				// capable host it must not fall back to unadmitted writes.
				return "unadmitted task", nil
			}
		}
		p.publication = &pendingPublication{legacy: true}
	} else {
		var err error
		if payload.Publication.Token != "" {
			err = p.resumePublication(payload.Track, payload.Publication, kind)
		} else {
			err = p.beginPublication(payload.Track, payload.Publication, kind)
		}
		if err != nil {
			p.releaseOperation(payload.Track, payload.Publication)
			return "stale publication", nil
		}
	}
	// Only the admitted task may refresh the shared pending marker; a stale
	// task must not stomp a newer generation's marker.
	markerKey := ""
	if trackKey, ok := lyricsTrackKey(payload.Track); ok {
		markerKey = p.sweepPendingMarkerKey(payload.Track, trackKey)
		_ = p.storeSweepMarker(markerKey, sweepMarkerTTL)
	}
	status, deferredIDs, markerOrder := p.runSweepDetailed(payload.Track, payload.Sources, payload.Refresh)
	continuationQueued := false
	if len(deferredIDs) > 0 && p.enqueueSweepSources != nil {
		p.enqueueMeta = payload.Publication
		p.enqueueRefresh = payload.Refresh
		stalledPasses := 0
		if slices.Equal(payload.Sources, deferredIDs) {
			stalledPasses = min(max(payload.StalledPasses, 0), maxSweepStalledPasses) + 1
		}
		// A shrinking tail is progress; an unchanged tail gets bounded retries.
		if stalledPasses >= maxSweepStalledPasses {
			p.logf("lyrics sweep stopped after repeated budget exhaustion; a later lookup can retry")
		} else if err := p.enqueueSweepSources(payload.Track, deferredIDs, stalledPasses); err != nil {
			p.logf(fmt.Sprintf("sweep continuation enqueue failed: %v", p.safeDiagnosticError(err)))
		} else {
			continuationQueued = true
		}
	}
	if markerKey != "" {
		// The done marker is written for the full configured order, so the
		// completion check must use that same order: checking the continuation
		// subset would release the pending marker although the sweep is done.
		done := p.sweepMarkerActive(p.doneMarkerKey(payload.Track, markerOrder))
		switch {
		case done:
			// The completed marker gates duplicates now.
		case continuationQueued:
			// A continuation still works this track: keep the queued marker
			// alive so replays cannot enqueue a parallel full sweep while it
			// waits behind other songs.
			_ = p.storeSweepMarker(markerKey, sweepQueuedMarkerTTL)
		default:
			// Nothing is pending: release the slot so the next lookup can
			// retry the incomplete sweep promptly.
			_ = p.storeSweepMarker(markerKey, 1)
		}
	}
	if !continuationQueued {
		if payload.Refresh {
			if key, ok := p.refreshTailMarkerKey(payload.Track); ok {
				_ = p.storeSweepMarker(key, 1)
			}
		}
		// Every terminal task releases its slot, including ordinary sweeps
		// that produced no commit.
		p.releaseOperation(payload.Track, payload.Publication)
	}
	return status, nil
}

// runSweep walks every enabled source for one track and caches each result, so the
// per-song source picker (Phase B) can list every source that returned lyrics.
func (p *Provider) runSweep(track lyrics.TrackInfo) string {
	status, _, _ := p.runSweepDetailed(track, nil, false)
	return status
}

// sweepDoneMarkerKey includes generation and source order: older coverage must
// not suppress discovery after a parser/cache-policy upgrade or source change.
func sweepDoneMarkerKey(track lyrics.TrackInfo, order []source.Source) string {
	trackKey, _ := lyricsTrackKey(track)
	digest := sha256.Sum256([]byte(strings.Join(source.IDs(order), "|")))
	return fmt.Sprintf("%sg%d:%x:%s", sweepDonePrefix, lyricsCacheGeneration, digest, trackKey)
}

// doneMarkerKey is the provider-view done key: it also carries the credential
// fingerprint so re-authorizing after a recorded miss re-runs the sweep.
// Credential changes must invalidate negative coverage.
func (p *Provider) doneMarkerKey(track lyrics.TrackInfo, order []source.Source) string {
	return fmt.Sprintf("%s:cred:%s", sweepDoneMarkerKey(track, order), p.lyricsCredentialsFingerprint())
}

// cacheTTLForCoveredCandidate maps a covered entry to the TTL it bounds the
// done marker by: positive candidates use their per-source TTL; a persisted
// miss uses its five-minute retry window, so a marker cannot outlive the fresh
// answers it stands for.
func cacheTTLForCoveredCandidate(candidate lyric.Candidate) int64 {
	if candidate.Present() {
		return sourceCacheTTLSeconds(candidate.Quality)
	}
	return negativeLyricsCacheTTLSeconds
}

// runSweepSources walks the sources for one track, optionally restricted to the
// ids carried by a continuation task. It returns the status string and the ids of
// sources deferred because the budget ran out (so the task can resume them).
func (p *Provider) runSweepSources(track lyrics.TrackInfo, allowed []string) (string, []string) {
	status, deferredIDs, _ := p.runSweepDetailed(track, allowed, false)
	return status, deferredIDs
}

func (p *Provider) runSweepDetailed(track lyrics.TrackInfo, allowed []string, refresh bool) (string, []string, []source.Source) {
	if title := strings.TrimSpace(track.Title); title == "" || lyric.TrackArtist(track) == "" {
		return "ignored (incomplete track metadata)", nil, nil
	}

	budget := p.sweepBudget
	if budget == 0 {
		budget = defaultSweepBudget
	}
	deadline := p.now().Add(budget)

	sources := p.activeSources()
	var allowedSet map[string]bool
	if len(allowed) > 0 {
		allowedSet = make(map[string]bool, len(allowed))
		for _, id := range allowed {
			allowedSet[id] = true
		}
		filtered := make([]source.Source, 0, len(allowedSet))
		for _, src := range sources {
			if allowedSet[src.ID] && src.Fetch != nil {
				filtered = append(filtered, src)
			}
		}
		sources = filtered
	}
	// Continuations hand the done marker back to the full configured order:
	// caching against the deferred subset alone would never satisfy a fresh
	// full-order check and repeat the whole pass. The same order is returned
	// so the task's completion check uses the key the write used.
	fullOrder := sources
	if allowedSet != nil {
		fullOrder = p.activeSources()
	}
	cached, failed, skipped, deferred := 0, 0, 0, 0
	cooldownSkipped, implemented := 0, 0
	// missed counts clean "no lyrics here" responses: real covered work that
	// must terminate the sweep. notCovered counts fetches that succeeded but
	// could not store anything (e.g. quota-refused writes).
	missed, notCovered := 0, 0
	// coverageTTL bounds the done marker by the shortest cached-result TTL it
	// covers, so a marker never outlives the results it stands for.
	coverageTTL := sweepDoneTTL
	var deferredIDs []string
	localCold := map[string]bool{}
	for _, src := range sources {
		if src.Fetch != nil {
			implemented++
		}
	}
	for _, src := range sources {
		if src.Fetch == nil {
			continue
		}
		_, expiresAt, covered := p.readCachedSourceCoverageWithExpiry(track, src.ID)
		if refresh {
			_, expiresAt, covered = p.readCachedSourcePositiveWithExpiry(track, src.ID)
		}
		remaining := expiresAt - p.now().Unix()
		if covered && expiresAt > 0 && remaining > 0 {
			skipped++
			if remaining < coverageTTL {
				coverageTTL = remaining
			}
			continue
		}
		// Unstamped entries refetch so the pass leaves trustworthy coverage.
		if p.sourceInCooldown(src) || p.sourceFailureBackoff(src.ID) > 0 {
			// A cooled-down source is not covered by this pass, so the pass
			// cannot be marked done; retry the sweep after the cooldown lapses.
			cooldownSkipped++
			skipped++
			continue
		}
		if coldHostFor(src, localCold) != "" {
			// A KV write failure must not let siblings hit the same globally
			// rate-limited host inside this pass (in-memory cold hosts).
			cooldownSkipped++
			skipped++
			continue
		}
		if !p.now().Before(deadline) {
			deferred++
			deferredIDs = append(deferredIDs, src.ID)
			continue
		}
		started := p.now()
		candidates, fetchHealth, err := src.Fetch(p, track, deadline)
		if err != nil {
			if p.now().Before(deadline) {
				p.rememberSourceFailure(src.ID, started, err)
			}
			p.logSourceFailure(src.ID, started, err)
			if errors.Is(err, source.ErrLookupBudgetExhausted) {
				// The search finished against the budget and the lyrics hop
				// could not start: this source belongs in the continuation,
				// not in the failure count.
				deferred++
				deferredIDs = append(deferredIDs, src.ID)
				continue
			}
			failed++
			if source.IsGlobalRateLimitError(err) {
				// Preserve the observed host cooldown in memory too: a KV write
				// failure must not let siblings hit the rate-limited host now.
				for _, hostName := range src.Hosts {
					localCold[hostName] = true
				}
			}
			continue
		}
		if fetchHealth <= source.Uncertain {
			p.resetSourceFailureBefore(src.ID, started)
		}
		if fetchHealth == source.TemporarilySkipped && len(candidates) == 0 {
			// A temporary skip (token rejection) is neither covered work nor a
			// miss: the next sweep can get a fresh token. Anonymous
			// cache-only misses stay uncertain and fall through to miss
			// coverage instead.
			cooldownSkipped++
			skipped++
			continue
		}
		storedAny := false
		hadCandidates := false
		hadPlayable := false
		assessed := make([]lyric.Candidate, 0, len(candidates))
		for index := range candidates {
			candidate := &candidates[index]
			p.assessCandidate(track, candidate)
			if candidate.Present() && candidate.Match.Status == lyric.MatchMismatch {
				continue
			}
			if candidate.Present() {
				hadCandidates = true
				if candidate.Playable() {
					hadPlayable = true
				}
				p.prepareCandidate(track, src.ID, candidate)
			}
			assessed = append(assessed, *candidate)
		}
		if len(assessed) > 0 {
			storedAny = p.cacheSourceCandidates(track, src.ID, assessed)
		}
		switch {
		case storedAny:
			cached++
			for _, candidate := range assessed {
				if covered := cacheTTLForCoveredCandidate(candidate); covered > 0 && covered < coverageTTL {
					coverageTTL = covered
				}
			}
		case hadPlayable:
			// A playable positive was refused by the size/quota guard. That is
			// not a miss and must stay retryable; never freeze the sweep on a
			// false negative.
			notCovered++
		case hadCandidates:
			// Ambiguous suggestions are cached but are not playable coverage:
			// persist a miss so the source is not re-walked every pass.
			if !p.cacheSourceMiss(track, src.ID) {
				notCovered++
				continue
			}
			missed++
			if negativeLyricsCacheTTLSeconds < coverageTTL {
				coverageTTL = negativeLyricsCacheTTLSeconds
			}
		default:
			// A clean miss is covered work too: the source was fetched and
			// genuinely has nothing (misses must terminate the sweep).
			// Persist the miss so later passes and continuations can skip the
			// refetch instead of re-walking the same clean misses every cycle.
			if !p.cacheSourceMiss(track, src.ID) {
				// A refused miss write is not honest coverage.
				notCovered++
				continue
			}
			missed++
			// The miss entry expires within five minutes, so it also bounds the
			// marker's re-check window.
			if negativeLyricsCacheTTLSeconds < coverageTTL {
				coverageTTL = negativeLyricsCacheTTLSeconds
			}
		}
	}
	// For a continuation pass, the sources outside the deferred subset must
	// already be cached from the original sweep; otherwise the full order is
	// not covered yet and no marker may be written.
	coveredOthers := true
	for _, src := range fullOrder {
		if allowedSet == nil || src.Fetch == nil || allowedSet[src.ID] {
			continue
		}
		_, siblingExpiry, ok := p.readCachedSourceCoverageWithExpiry(track, src.ID)
		remaining := siblingExpiry - p.now().Unix()
		if !ok || siblingExpiry <= 0 || remaining <= 0 {
			// A sibling without a trustworthy remaining lifetime cannot be
			// covered by this marker; the next full sweep restamps it.
			coveredOthers = false
			break
		}
		// A cached sibling's REMAINING life bounds the marker, so a
		// synchronized continuation cannot outlive a plain sibling's shorter
		// entry.
		if remaining < coverageTTL {
			coverageTTL = remaining
		}
	}
	complete := false
	if failed == 0 && deferred == 0 && cooldownSkipped == 0 && notCovered == 0 && coveredOthers && cached+missed+skipped == implemented && implemented > 0 {
		key := p.doneMarkerKey(track, fullOrder)
		if p.storeDoneMarker(key, coverageTTL) == nil {
			complete = true
			// Direct legacy writes can be verified immediately; buffered
			// publication writes only exist after the atomic commit below.
			if p.publication == nil || !p.publication.active {
				if _, exists, _ := p.cache.Get(key); !exists {
					complete = false
				}
			}
		}
	}
	if _, err := p.commitPublication(complete, refresh, p.pendingSnapshotExpiry(track, fullOrder)); err != nil {
		p.releasePublication()
		p.publication = nil
		// The admission is gone; a continuation carrying this token cannot
		// resume and would only leave the markers suppressed for their TTL.
		return fmt.Sprintf("cached=%d skipped=%d failed=%d deferred=%d stale-publication", cached, skipped, failed, deferred), nil, fullOrder
	}
	if p.publication != nil && p.publication.active && p.publication.minted && len(p.publication.writes) == 0 {
		// beginPublication minted this token for a token-less sweep. A
		// zero-write pass has no commit to mark it terminal, and the
		// payload cannot release it.
		p.releasePublication()
	}
	p.publication = nil
	return fmt.Sprintf("cached=%d skipped=%d failed=%d deferred=%d", cached, skipped, failed, deferred), deferredIDs, fullOrder
}

// sourceInCooldown reports provider-wide cooldown state for a source's own id and
// the hosts it shares with sibling sources (families such as the Better Lyrics
// endpoints share cooldowns by design — including the v3 keys the legacy
// fetchers recorded before this registry existed).
func (p *Provider) sourceInCooldown(src source.Source) bool {
	if p.Cooldown(source.GlobalCooldownKey(src.ID)) > 0 {
		return true
	}
	if src.ID == "lyricsplus" {
		// Its hosts are independent mirrors: per-host cooldowns are honored
		// inside the fetch while rotating mirrors, and must not suppress the
		// whole multi-host source here.
		return false
	}
	for _, hostName := range src.Hosts {
		if p.Cooldown(source.GlobalCooldownKey(hostName)) > 0 {
			return true
		}
	}
	if sharesBetterLyricsHost(src) && p.Cooldown(source.BetterLyricsGlobalCooldownKey) > 0 {
		return true
	}
	return false
}

func sharesBetterLyricsHost(src source.Source) bool {
	return slices.Contains(src.Hosts, "lyrics-api.boidu.dev")
}

// maybeEnqueueSweep triggers the background sweep for a track unless one already
// runs. Source cooldowns observed during the stage-one pass are honored here too:
// sources behind those cooldowns benefit from the same deviating skip logic in the
// sweep, so enqueuing without a marker check could cause duplicate work.
func (p *Provider) maybeEnqueueSweep(track lyrics.TrackInfo) bool {
	if len(p.tailSources) > 0 {
		// The explicit refresh tail owns the deferred coverage and the
		// foreground admission; a competing full sweep would duplicate work
		// and could release the token the tail needs.
		return false
	}
	if p.sweepMarkerActive(p.doneMarkerKey(track, p.activeSources())) {
		return false
	}
	if key, ok := p.refreshTailMarkerKey(track); ok && p.sweepMarkerActive(key) {
		// The explicit refresh tail owns deferred negative coverage.
		return false
	}
	trackKey, ok := lyricsTrackKey(track)
	if !ok {
		return false
	}
	markerKey := p.sweepPendingMarkerKey(track, trackKey)
	if p.sweepMarkerActive(markerKey) {
		return false
	}
	if err := p.storeSweepMarker(markerKey, sweepQueuedMarkerTTL); err != nil {
		// A broken cache must not block lyrics: skip the sweep this round.
		return false
	}
	p.enqueueMeta = p.captureEnqueuePublication()
	p.enqueueRefresh = false
	if err := p.enqueueSweep(track); err != nil {
		// Release the spot early with a 1-second TTL so the next lookup can retry
		// the sweep rather than waiting out the whole dedupe window.
		_ = p.storeSweepMarker(markerKey, 1)
		p.logf(fmt.Sprintf("sweep enqueue failed: %v", p.safeDiagnosticError(err)))
		return false
	}
	return true
}

// Store explicit marker expiries as well as the host TTL, so completion and
// queue deduplication cannot outlive their bounded coverage on a stale KV read.
func (p *Provider) storeSweepMarker(key string, ttl int64) error {
	value, err := json.Marshal(cachedLyricsEnvelope{ExpiresAt: p.now().Add(time.Duration(ttl) * time.Second).Unix()})
	if err != nil {
		return err
	}
	return p.cache.SetWithTTL(key, value, ttl)
}

// storeDoneMarker is discovery-visible coverage and must commit with the
// source results it describes.
func (p *Provider) storeDoneMarker(key string, ttl int64) error {
	value, err := json.Marshal(cachedLyricsEnvelope{ExpiresAt: p.now().Add(time.Duration(ttl) * time.Second).Unix()})
	if err != nil {
		return err
	}
	if !p.storeDiscoveryBytes(key, value, ttl) {
		return errors.New("publication refused done marker")
	}
	return nil
}

func (p *Provider) sweepMarkerActive(key string) bool {
	envelope, found := p.readCachedEnvelope(key)
	return found && envelope.ExpiresAt > p.now().Unix()
}

// sweepPendingMarkerKey namespaces the dedupe marker by source order and
// credential state: reconfiguring sources must not delay a fresh sweep for
// the old marker window.
func (p *Provider) sweepPendingMarkerKey(track lyrics.TrackInfo, trackKey string) string {
	digest := sha256.Sum256([]byte(strings.Join(source.IDs(p.activeSources()), "|")))
	return fmt.Sprintf("%sg%d:%s:%x:%s", sweepMarkerPrefix, lyricsCacheGeneration, p.lyricsCredentialsFingerprint(), digest[:8], trackKey)
}

// discoveryComplete combines the active done marker with a winner's recorded
// coverage. A sweep can complete after the foreground winner was written; the
// next re-rank must not overwrite that completion with a stale false value.
func (p *Provider) discoveryComplete(track lyrics.TrackInfo, sources []source.Source) bool {
	if p.sweepMarkerActive(p.doneMarkerKey(track, sources)) {
		return true
	}
	_, _, ok, sweepComplete := p.readCachedWinner(track, sources)
	return ok && sweepComplete
}

func (p *Provider) refreshTailMarkerKey(track lyrics.TrackInfo) (string, bool) {
	trackKey, ok := lyricsTrackKey(track)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%sg%d:%s:%s", refreshTailMarkerPrefix, lyricsCacheGeneration, p.lyricsCredentialsFingerprint(), trackKey), true
}

// prepareRefreshTail records eligible negative/error sources beyond the
// foreground bound. It is enqueued only after the foreground publication
// commits, so the tail can never observe pre-commit coverage.
func (p *Provider) prepareRefreshTail(track lyrics.TrackInfo, sources []source.Source, attempted []string) {
	if p.publication == nil || !p.publication.active {
		return
	}
	attemptedSet := make(map[string]bool, len(attempted))
	for _, id := range attempted {
		attemptedSet[id] = true
	}
	remaining := make([]string, 0, len(sources))
	for _, src := range sources {
		if src.Fetch == nil || attemptedSet[src.ID] {
			continue
		}
		if _, fresh := p.readCachedSourcePositive(track, src.ID); fresh {
			continue
		}
		remaining = append(remaining, src.ID)
	}
	if len(remaining) == 0 {
		// A newer refresh that needs no tail retires any stale queued-tail
		// marker, otherwise it would suppress new sweeps for the marker TTL.
		if markerKey, ok := p.refreshTailMarkerKey(track); ok {
			_ = p.storeSweepMarker(markerKey, 1)
		}
		return
	}
	p.tailSources = remaining
	p.tailOp = Publication{
		Generation: p.publication.generation,
		Token:      p.publication.token,
		Kind:       "tail",
	}
}

func (p *Provider) enqueueRefreshTailWithOp(track lyrics.TrackInfo, sources []string, op Publication) error {
	if len(sources) == 0 || op.Generation == "" || op.Token == "" {
		return nil
	}
	if markerKey, ok := p.refreshTailMarkerKey(track); ok {
		// A newer refresh owns this track; replace any stale tail marker.
		_ = p.storeSweepMarker(markerKey, sweepQueuedMarkerTTL)
	}
	p.enqueueMeta = op
	p.enqueueRefresh = true
	if err := p.enqueueSweepSources(track, sources, 0); err != nil {
		if markerKey, ok := p.refreshTailMarkerKey(track); ok {
			_ = p.storeSweepMarker(markerKey, 1)
		}
		return err
	}
	return nil
}

func (p *Provider) releaseOperation(track lyrics.TrackInfo, op Publication) {
	if p.publisher == nil || op.Generation == "" || op.Token == "" {
		return
	}
	trackKey, ok := publicationTrackKey(track)
	if !ok {
		return
	}
	request, err := json.Marshal(admissionWire{
		Version: 1, Action: "release", Generation: op.Generation,
		Track: trackKey, Kind: op.Kind, Token: op.Token,
	})
	if err == nil {
		_, _ = p.publisher.Admit(trackKey, request)
	}
}
