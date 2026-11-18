package provider

import (
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// originalRefetchBudget bounds the on-demand refetch used when an artifact has
// expired or been evicted. Retrieval never walks other sources.
const originalRefetchBudget = 3 * time.Second

// GetLyricsOriginal returns the exact native document for one selection. The
// selection is bound to this track, this plugin's enabled source, and the
// current source instance; an evicted artifact triggers one bounded refetch of
// the same source and must match the identity exactly. A missing or changed
// selection is reported unavailable, never replaced by another candidate.
func (p *Provider) GetLyricsOriginal(track lyrics.TrackInfo, identity lyric.VariantIdentity) (lyric.Original, string) {
	return p.GetLyricsOriginalPublished(track, identity, Publication{})
}

// GetLyricsOriginalPublished is the publication-aware original path. It only
// admits when an evicted artifact requires a refetch that mutates shared
// source caches.
func (p *Provider) GetLyricsOriginalPublished(track lyrics.TrackInfo, identity lyric.VariantIdentity, meta Publication) (lyric.Original, string) {
	p.preparePublication(meta, "original")
	if len(strings.TrimSpace(track.Title)) == 0 || lyric.TrackArtist(track) == "" {
		return lyric.Original{}, "track metadata is incomplete"
	}
	if !identity.Valid() {
		return lyric.Original{}, "invalid selection"
	}
	sourceID, ok := identity.InstanceSourceID()
	if !ok {
		return lyric.Original{}, "invalid selection"
	}
	enabled := false
	for _, src := range p.activeSources() {
		if src.ID == sourceID {
			enabled = true
			break
		}
	}
	if !enabled {
		return lyric.Original{}, "source is disabled"
	}
	if p.sourceInstanceID(sourceID) != identity.Instance {
		return lyric.Original{}, "source configuration changed"
	}

	listed := false
	for _, candidate := range p.readCachedSourceCandidates(track, sourceID) {
		if candidate.Identity != nil && candidate.Identity.Equal(identity) {
			listed = true
			break
		}
	}
	if listed {
		if document, ok := p.readOriginalArtifact(track, identity); ok {
			return document, ""
		}
	}

	var src source.Source
	found := false
	for _, active := range p.activeSources() {
		if active.ID == sourceID {
			src, found = active, true
			break
		}
	}
	if !found || src.Fetch == nil {
		return lyric.Original{}, "source does not support retrieval"
	}
	if p.sourceInCooldown(src) || p.sourceFailureBackoff(src.ID) > 0 {
		return lyric.Original{}, "source is cooling down"
	}
	if err := p.ensurePublication(track); err != nil {
		return lyric.Original{}, "publication busy"
	}
	deadline := p.now().Add(originalRefetchBudget)
	started := p.now()
	candidates, health, err := src.Fetch(p, track, deadline)
	if err != nil {
		// Mirror the foreground and sweep failure contract: an upstream
		// outage backs off so repeated downloads cannot hammer it. A budget
		// timeout is not an outage and is skipped by rememberSourceFailure.
		if p.now().Before(deadline) {
			p.rememberSourceFailure(src.ID, err)
		}
		p.logSourceFailure(src.ID, started, err)
		p.releasePublication()
		p.publication = nil
		return lyric.Original{}, "selection unavailable"
	}
	if health <= source.Uncertain {
		p.resetSourceFailureBefore(src.ID, started)
	}
	for index := range candidates {
		p.prepareCandidate(track, sourceID, &candidates[index])
	}
	p.cacheSourceCandidates(track, sourceID, candidates)
	if _, err := p.commitPublication(p.discoveryComplete(track, p.activeSources())); err != nil {
		return lyric.Original{}, "selection unavailable"
	}
	p.publication = nil
	// A bounded bundle may have dropped this selection: only a selection that
	// remains listed is addressable, and a dropped one reports unavailable
	// rather than causing endless refetches.
	listed = false
	for _, candidate := range p.readCachedSourceCandidates(track, sourceID) {
		if candidate.Identity != nil && candidate.Identity.Equal(identity) {
			listed = true
			break
		}
	}
	if !listed {
		return lyric.Original{}, "selection is no longer offered"
	}
	for index := range candidates {
		candidate := candidates[index]
		if candidate.Identity == nil || !candidate.Identity.Equal(identity) {
			continue
		}
		if document, ok := p.readOriginalArtifact(track, identity); ok {
			return document, ""
		}
		reason := strings.TrimSpace(candidate.OriginalDetails().Reason)
		if reason == "" {
			reason = lyric.OriginalReasonArtifactUnavailable
		}
		return lyric.Original{}, reason
	}
	return lyric.Original{}, "selection is no longer offered"
}
