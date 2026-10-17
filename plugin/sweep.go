package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

const (
	sweepQueueName    = "lyrics-sweep"
	sweepMarkerPrefix = "sweep:"
	sweepDonePrefix   = "sweep-done:"
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
)

// defaultSweepBudget bounds one sweep invocation. The task worker's own callback
// has no declared deadline, so the sweep stays polite on its own: ~24 s keeps it
// well under Navidrome's per-invocation comfort zone even on a full miss.
const defaultSweepBudget = 24 * time.Second

type sweepTaskPayload struct {
	Track   lyrics.TrackInfo `json:"track"`
	Sources []string         `json:"sources,omitempty"`
}

// OnInit creates the permanent sweep queue. Failing here is non-fatal: lyrics keep
// working and the queue lifecycle can be repaired by reloading the plugin.
func (p *lyricsProvider) OnInit() error {
	_ = createSweepQueue()
	return nil
}

// OnTaskExecute dispatches queued sweeps; tasks from other queues are ignored so a
// misconfigured queue cannot trigger unrelated fetches.
func (p *lyricsProvider) OnTaskExecute(req taskworker.TaskExecuteRequest) (string, error) {
	if req.QueueName != sweepQueueName {
		return "ignored queue " + req.QueueName, nil
	}
	var payload sweepTaskPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode sweep payload: %w", err)
	}
	// The task may have queued behind a backlog; refresh the marker when
	// execution starts, and release it afterwards unless the sweep completed
	// — an incomplete finish must stay re-enqueueable, while a completed one
	// is gated by the done marker instead.
	markerKey := ""
	if trackKey, ok := lyricsTrackKey(payload.Track); ok {
		markerKey = p.sweepPendingMarkerKey(payload.Track, trackKey)
		_ = p.cache.SetWithTTL(markerKey, []byte(strconv.FormatInt(p.now().Unix(), 10)), sweepMarkerTTL)
	}
	status, deferredIDs, markerOrder := p.runSweepDetailed(payload.Track, payload.Sources)
	continuationQueued := false
	if len(deferredIDs) > 0 && p.enqueueSweepSources != nil {
		// Resume the deferred tail immediately instead of waiting for a replay.
		if err := p.enqueueSweepSources(payload.Track, deferredIDs); err != nil {
			logCacheDebug(fmt.Sprintf("sweep continuation enqueue failed: %v", err))
		} else {
			continuationQueued = true
		}
	}
	if markerKey != "" {
		// The done marker is written for the full configured order, so the
		// completion check must use that same order: checking the continuation
		// subset would release the pending marker although the sweep is done.
		_, done, _ := p.cache.Get(p.doneMarkerKey(payload.Track, markerOrder))
		switch {
		case done:
			// The completed marker gates duplicates now.
		case continuationQueued:
			// A continuation still works this track: keep the queued marker
			// alive so replays cannot enqueue a parallel full sweep while it
			// waits behind other songs.
			_ = p.cache.SetWithTTL(markerKey, []byte(strconv.FormatInt(p.now().Unix(), 10)), sweepQueuedMarkerTTL)
		default:
			// Nothing is pending: release the slot so the next lookup can
			// retry the incomplete sweep promptly.
			_ = p.cache.SetWithTTL(markerKey, []byte("release"), 1)
		}
	}
	return status, nil
}

// runSweep walks every enabled source for one track and caches each result, so the
// per-song source picker (Phase B) can list every source that returned lyrics.
func (p *lyricsProvider) runSweep(track lyrics.TrackInfo) string {
	status, _, _ := p.runSweepDetailed(track, nil)
	return status
}

// sweepDoneMarkerKey fingerprints the enabled source order into the done key so
// reordering or rescaling the catalog invalidates satisfied sweeps.
func sweepDoneMarkerKey(track lyrics.TrackInfo, order []source.Source) string {
	trackKey, _ := lyricsTrackKey(track)
	digest := sha256.Sum256([]byte(strings.Join(source.IDs(order), "|")))
	return fmt.Sprintf("%s%x:%s", sweepDonePrefix, digest, trackKey)
}

// doneMarkerKey is the provider-view done key: it also carries the credential
// fingerprint so re-authorizing after a recorded miss re-runs the sweep.
// Credential changes must invalidate negative coverage.
func (p *lyricsProvider) doneMarkerKey(track lyrics.TrackInfo, order []source.Source) string {
	return fmt.Sprintf("%s:cred:%s", sweepDoneMarkerKey(track, order), p.lyricsCredentialsFingerprint())
}

// cacheTTLForCoveredCandidate maps a covered entry to the TTL it bounds the
// done marker by: positive candidates use their per-source TTL; a persisted
// miss uses its one-hour retry window, so a marker cannot outlive the fresh
// answers it stands for.
func cacheTTLForCoveredCandidate(candidate lyric.Candidate) int64 {
	if candidate.Present() {
		return sourceCacheTTLSeconds(candidate.Quality)
	}
	return unsyncedLyricsCacheTTLSeconds
}

// runSweepSources walks the sources for one track, optionally restricted to the
// ids carried by a continuation task. It returns the status string and the ids of
// sources deferred because the budget ran out (so the task can resume them).
func (p *lyricsProvider) runSweepSources(track lyrics.TrackInfo, allowed []string) (string, []string) {
	status, deferredIDs, _ := p.runSweepDetailed(track, allowed)
	return status, deferredIDs
}

func (p *lyricsProvider) runSweepDetailed(track lyrics.TrackInfo, allowed []string) (string, []string, []source.Source) {
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
		if _, expiresAt, ok := p.readCachedSourceCoverageWithExpiry(track, src.ID); ok {
			remaining := expiresAt - p.now().Unix()
			if expiresAt > 0 && remaining > 0 {
				skipped++
				if remaining < coverageTTL {
					coverageTTL = remaining
				}
				continue
			}
			// Entries without a trustworthy remaining lifetime (pre-stamp
			// writes) refetch so the pass leaves stamped coverage behind
			// instead of letting a marker outlive them.
		}
		if cooledDown := p.sourceInCooldown(src); cooledDown {
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
		if p.now().After(deadline) {
			deferred++
			deferredIDs = append(deferredIDs, src.ID)
			continue
		}
		candidates, fetchHealth, err := src.Fetch(p, track, deadline)
		if err != nil {
			if errors.Is(err, errLyricsLookupBudgetExhausted) {
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
		for _, candidate := range candidates {
			if !candidate.Present() {
				continue
			}
			hadCandidates = true
			if p.cacheSourceCandidate(track, src.ID, candidate) {
				storedAny = true
			}
		}
		switch {
		case storedAny:
			cached++
			for _, candidate := range candidates {
				if covered := cacheTTLForCoveredCandidate(candidate); covered > 0 && covered < coverageTTL {
					coverageTTL = covered
				}
			}
		case hadCandidates:
			// Every write was refused (oversized/quota): this pass cannot
			// honestly claim the source is covered.
			notCovered++
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
			// The miss entry expires within the hour, so it also bounds the
			// marker's re-check window.
			if unsyncedLyricsCacheTTLSeconds < coverageTTL {
				coverageTTL = unsyncedLyricsCacheTTLSeconds
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
	if failed == 0 && deferred == 0 && cooldownSkipped == 0 && notCovered == 0 && coveredOthers && cached+missed+skipped == implemented && implemented > 0 {
		key := p.doneMarkerKey(track, fullOrder)
		if p.cache.SetWithTTL(key, []byte(p.now().Format(time.RFC3339)), coverageTTL) == nil {
			// Also verify the write actually stayed (an oversized write is
			// dropped); re-read to be sure.
			if _, exists, _ := p.cache.Get(key); !exists {
				return fmt.Sprintf("cached=%d skipped=%d failed=%d deferred=%d", cached, skipped, failed, deferred), deferredIDs, sources
			}
		}
	}
	return fmt.Sprintf("cached=%d skipped=%d failed=%d deferred=%d", cached, skipped, failed, deferred), deferredIDs, fullOrder
}

// sourceInCooldown reports provider-wide cooldown state for a source's own id and
// the hosts it shares with sibling sources (families such as the Better Lyrics
// endpoints share cooldowns by design — including the v3 keys the legacy
// fetchers recorded before this registry existed).
func (p *lyricsProvider) sourceInCooldown(src source.Source) bool {
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
	for _, hostName := range src.Hosts {
		if hostName == "lyrics-api.boidu.dev" {
			return true
		}
	}
	return false
}

// maybeEnqueueSweep triggers the background sweep for a track unless one already
// runs. Source cooldowns observed during the stage-one pass are honored here too:
// sources behind those cooldowns benefit from the same deviating skip logic in the
// sweep, so enqueuing without a marker check could cause duplicate work.
func (p *lyricsProvider) maybeEnqueueSweep(track lyrics.TrackInfo) bool {
	if _, done, _ := p.cache.Get(p.doneMarkerKey(track, p.activeSources())); done {
		return false
	}
	trackKey, ok := lyricsTrackKey(track)
	if !ok {
		return false
	}
	markerKey := p.sweepPendingMarkerKey(track, trackKey)
	if _, exists, _ := p.cache.Get(markerKey); exists {
		return false
	}
	if err := p.cache.SetWithTTL(markerKey, []byte(strconv.FormatInt(p.now().Unix(), 10)), sweepQueuedMarkerTTL); err != nil {
		// A broken cache must not block lyrics: skip the sweep this round.
		return false
	}
	if err := p.enqueueSweep(track); err != nil {
		// Release the spot early with a 1-second TTL so the next lookup can retry
		// the sweep rather than waiting out the whole dedupe window.
		_ = p.cache.SetWithTTL(markerKey, []byte("release"), 1)
		logCacheDebug(fmt.Sprintf("sweep enqueue failed: %v", err))
		return false
	}
	return true
}

// sweepPendingMarkerKey namespaces the dedupe marker by source order and
// credential state: reconfiguring sources must not delay a fresh sweep for
// the old marker window.
func (p *lyricsProvider) sweepPendingMarkerKey(track lyrics.TrackInfo, trackKey string) string {
	digest := sha256.Sum256([]byte(strings.Join(source.IDs(p.activeSources()), "|")))
	return fmt.Sprintf("%s%s:%x:%s", sweepMarkerPrefix, p.lyricsCredentialsFingerprint(), digest[:8], trackKey)
}
