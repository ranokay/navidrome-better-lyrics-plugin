package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
)

const (
	sweepQueueName    = "lyrics-sweep"
	sweepMarkerPrefix = "sweep:"
	sweepDonePrefix   = "sweep-done:"
	// sweepDoneTTL outlives the per-source entries (24 h for synchronized
	// results) so a completed sweep prevents pointless re-sweeps for a day.
	sweepDoneTTL = int64(24 * time.Hour / time.Second)
	// sweepMarkerTTL matches the negative winner TTL so a fresh miss lookup can
	// re-trigger a sweep once its marker expires.
	sweepMarkerTTL = int64(5 * time.Minute / time.Second)
)

// sweepBudget bounds one sweep invocation. The task worker's own callback has
// no declared deadline, so the sweep stays polite on its own: ~24 s keeps it
// well under Navidrome's per-invocation comfort zone even on a full miss. It is
// a var so tests can shrink the budget.
var sweepBudget = 24 * time.Second

type sweepTaskPayload struct {
	Track   lyrics.TrackInfo `json:"track"`
	Sources []string         `json:"sources,omitempty"`
}

// sourceGlobalCooldownKey names the provider-wide rate-limit cooldown for a source.
func sourceGlobalCooldownKey(identity string) string {
	return fmt.Sprintf("rate-limit:%s:global:v1", identity)
}

// OnInit creates the permanent sweep queue. Failing here is non-fatal: lyrics keep
// working and the queue lifecycle can be repaired by reloading the plugin.
func (p *lyricsProvider) OnInit() error {
	return suppressSweepQueueError(createSweepQueue())
}

func suppressSweepQueueError(err error) error {
	_ = err // logged at the host if queue creation fails
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
	status, deferredIDs, _ := p.runSweepSourcesDetailed(payload.Track, payload.Sources)
	if len(deferredIDs) > 0 && p.enqueueSweepSources != nil {
		// Resume the deferred tail immediately instead of waiting for a replay.
		if err := p.enqueueSweepSources(payload.Track, deferredIDs); err != nil {
			logCacheDebug(fmt.Sprintf("sweep continuation enqueue failed: %v", err))
		}
	}
	return status, nil
}

// runSweep walks every enabled source for one track and caches each result, so the
// per-song source picker (Phase B) can list every source that returned lyrics.
func (p *lyricsProvider) runSweep(track lyrics.TrackInfo) string {
	status, _, _ := p.runSweepSourcesDetailed(track, nil)
	return status
}

// sweepDoneMarkerKey fingerprints the enabled source order into the done key so
// reordering or rescaling the catalog invalidates satisfied sweeps.
func sweepDoneMarkerKey(track lyrics.TrackInfo, order []lyricsSource) string {
	trackKey, _ := lyricsTrackKey(track)
	digest := sha256.Sum256([]byte(strings.Join(sourceIDs(order), "|")))
	return fmt.Sprintf("%s%x:%s", sweepDonePrefix, digest, trackKey)
}

// runSweepSources walks the sources for one track, optionally restricted to the
// ids carried by a continuation task. It returns the status string and the ids of
// sources deferred because the budget ran out (so the task can resume them).
func (p *lyricsProvider) runSweepSources(track lyrics.TrackInfo, allowed []string) (string, []string) {
	status, deferredIDs, _ := p.runSweepDetailed(track, allowed)
	return status, deferredIDs
}

func (p *lyricsProvider) runSweepSourcesDetailed(track lyrics.TrackInfo, allowed []string) (string, []string, []lyricsSource) {
	return p.runSweepDetailed(track, allowed)
}

func (p *lyricsProvider) runSweepDetailed(track lyrics.TrackInfo, allowed []string) (string, []string, []lyricsSource) {
	if title := strings.TrimSpace(track.Title); title == "" || trackArtist(track) == "" {
		return "ignored (incomplete track metadata)", nil, nil
	}

	sources := p.activeSources()
	if len(allowed) > 0 {
		allowedSet := make(map[string]bool, len(allowed))
		for _, id := range allowed {
			allowedSet[id] = true
		}
		filtered := make([]lyricsSource, 0, len(allowedSet))
		for _, source := range sources {
			if allowedSet[source.ID] && source.Fetch != nil {
				filtered = append(filtered, source)
			}
		}
		sources = filtered
	}
	deadline := p.now().Add(sweepBudget)
	cached, failed, skipped, deferred := 0, 0, 0, 0
	cooldownSkipped, implemented := 0, 0
	var deferredIDs []string
	localCold := map[string]bool{}
	for _, source := range sources {
		if source.Fetch != nil {
			implemented++
		}
	}
	for _, source := range sources {
		if source.Fetch == nil {
			continue
		}
		if _, ok := p.readCachedSourceCandidate(track, source.ID); ok {
			skipped++
			continue
		}
		if cooledDown := p.sourceInCooldown(source); cooledDown {
			// A cooled-down source is not covered by this pass, so the pass
			// cannot be marked done; retry the sweep after the cooldown lapses.
			cooldownSkipped++
			skipped++
			continue
		}
		if coldHostFor(source, localCold) != "" {
			// A KV write failure must not let siblings hit the same globally
			// rate-limited host inside this pass (Codex: in-memory cold hosts).
			cooldownSkipped++
			skipped++
			continue
		}
		if p.now().After(deadline) {
			deferred++
			deferredIDs = append(deferredIDs, source.ID)
			continue
		}
		candidates, _, err := source.Fetch(p, track, deadline)
		if err != nil {
			failed++
			if isGlobalRateLimitError(err) {
				// Preserve the observed host cooldown in memory too: a KV write
				// failure must not let siblings hit the rate-limited host now.
				for _, hostName := range source.Hosts {
					localCold[hostName] = true
				}
			}
			continue
		}
		storedAny := false
		for _, candidate := range candidates {
			if !candidate.present() {
				continue
			}
			if p.cacheSourceCandidate(track, source.ID, candidate) {
				storedAny = true
			}
		}
		if storedAny {
			cached++
		}
	}
	if failed == 0 && deferred == 0 && cooldownSkipped == 0 && cached+skipped == implemented && implemented > 0 {
		key := sweepDoneMarkerKey(track, sources)
		if p.cache.SetWithTTL(key, []byte(p.now().Format(time.RFC3339)), sweepDoneTTL) == nil {
			// Also verify the write actually stayed (an oversized write is
			// dropped); re-read to be sure.
			if _, exists, _ := p.cache.Get(key); !exists {
				return fmt.Sprintf("cached=%d skipped=%d failed=%d deferred=%d", cached, skipped, failed, deferred), deferredIDs, sources
			}
		}
	}
	return fmt.Sprintf("cached=%d skipped=%d failed=%d deferred=%d", cached, skipped, failed, deferred), deferredIDs, sources
}

// sourceInCooldown reports provider-wide cooldown state for a source's own id and
// the hosts it shares with sibling sources (families such as the Better Lyrics
// endpoints share cooldowns by design — including the v3 keys the legacy
// fetchers recorded before this registry existed).
func (p *lyricsProvider) sourceInCooldown(source lyricsSource) bool {
	if p.activeCooldown(sourceGlobalCooldownKey(source.ID)) > 0 {
		return true
	}
	for _, hostName := range source.Hosts {
		if p.activeCooldown(sourceGlobalCooldownKey(hostName)) > 0 {
			return true
		}
	}
	if sharesBetterLyricsHost(source) && p.activeCooldown(betterLyricsGlobalCooldownKey) > 0 {
		return true
	}
	return false
}

func sharesBetterLyricsHost(source lyricsSource) bool {
	for _, hostName := range source.Hosts {
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
	if _, done, _ := p.cache.Get(sweepDoneMarkerKey(track, p.activeSources())); done {
		return false
	}
	trackKey, ok := lyricsTrackKey(track)
	if !ok {
		return false
	}
	if _, exists, _ := p.cache.Get(sweepMarkerPrefix + trackKey); exists {
		return false
	}
	if err := p.cache.SetWithTTL(sweepMarkerPrefix+trackKey, []byte(strconv.FormatInt(p.now().Unix(), 10)), sweepMarkerTTL); err != nil {
		// A broken cache must not block lyrics: skip the sweep this round.
		return false
	}
	if err := p.enqueueSweep(track); err != nil {
		// Release the spot early with a 1-second TTL so the next lookup can retry
		// the sweep rather than waiting out the whole dedupe window.
		_ = p.cache.SetWithTTL(sweepMarkerPrefix+trackKey, []byte("release"), 1)
		logCacheDebug(fmt.Sprintf("sweep enqueue failed: %v", err))
		return false
	}
	return true
}
