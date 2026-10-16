package main

import (
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
	// sweepBudget bounds one sweep invocation. The task worker's own callback has
	// no declared deadline, so the sweep stays polite on its own: ~24 s keeps it
	// well under Navidrome's per-invocation comfort zone even on a full miss.
	sweepBudget = 24 * time.Second
)

type sweepTaskPayload struct {
	Track lyrics.TrackInfo `json:"track"`
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
	return p.runSweep(payload.Track), nil
}

// runSweep walks every enabled source for one track and caches each result, so the
// per-song source picker (Phase B) can list every source that returned lyrics.
func (p *lyricsProvider) runSweep(track lyrics.TrackInfo) string {
	if title := strings.TrimSpace(track.Title); title == "" || trackArtist(track) == "" {
		return "ignored (incomplete track metadata)"
	}

	sources := p.activeSources()
	deadline := p.now().Add(sweepBudget)
	cached, failed, skipped, deferred := 0, 0, 0, 0
	implemented := 0
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
			skipped++
			continue
		}
		if p.now().After(deadline) {
			deferred++
			continue
		}
		candidates, _, err := source.Fetch(p, track, deadline)
		if err != nil {
			failed++
			continue
		}
		stored := false
		for _, candidate := range candidates {
			if !candidate.present() {
				continue
			}
			p.cacheSourceCandidate(track, source.ID, candidate)
			stored = true
		}
		if stored {
			cached++
		}
	}
	if failed == 0 && deferred == 0 && implemented > 0 {
		if trackKey, ok := lyricsTrackKey(track); ok {
			p.cache.SetWithTTL(sweepDonePrefix+trackKey, []byte(p.now().Format(time.RFC3339)), sweepDoneTTL)
		}
	}
	return fmt.Sprintf("cached=%d skipped=%d failed=%d deferred=%d", cached, skipped, failed, deferred)
}

// sourceInCooldown reports provider-wide cooldown state for a source's own id and
// the hosts it shares with sibling sources (families such as the Better Lyrics
// endpoints share cooldowns by design).
func (p *lyricsProvider) sourceInCooldown(source lyricsSource) bool {
	if p.activeCooldown(sourceGlobalCooldownKey(source.ID)) > 0 {
		return true
	}
	for _, hostName := range source.Hosts {
		if p.activeCooldown(sourceGlobalCooldownKey(hostName)) > 0 {
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
	trackKey, ok := lyricsTrackKey(track)
	if !ok {
		return false
	}
	if _, done, _ := p.cache.Get(sweepDonePrefix + trackKey); done {
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
		logCacheDebug(fmt.Sprintf("sweep enqueue failed: %v", err))
		return false
	}
	return true
}
