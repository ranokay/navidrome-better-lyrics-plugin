package provider

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// cachedSourceVariant is one addressable representation stored in a source
// bundle. Every candidate an adapter returned is kept, not only the winner.
type cachedSourceVariant struct {
	Quality   lyric.TimingQuality      `json:"quality"`
	Response  lyrics.GetLyricsResponse `json:"response"`
	Variant   *lyric.VariantIdentity   `json:"variant,omitempty"`
	Original  *lyric.OriginalInfo      `json:"original,omitempty"`
	Match     lyric.RecordingMatch     `json:"match,omitempty"`
	ExpiresAt int64                    `json:"expiresAt,omitempty"`
}

type cachedSourceBundle struct {
	Generation  int                   `json:"generation,omitempty"`
	Candidates  []cachedSourceVariant `json:"candidates,omitempty"`
	Evidence    []lyric.Evidence      `json:"evidence,omitempty"`
	Credentials string                `json:"credentials,omitempty"`
	ExpiresAt   int64                 `json:"expiresAt,omitempty"`
}

type cachedOriginalEnvelope struct {
	Generation int                   `json:"generation,omitempty"`
	Identity   lyric.VariantIdentity `json:"identity"`
	Document   lyric.Original        `json:"document"`
	ExpiresAt  int64                 `json:"expiresAt,omitempty"`
}

func lyricsVariantCacheKey(track lyrics.TrackInfo, sourceID string) (string, bool) {
	return lyricsSourceCacheKey(track, sourceID, lyricsVariantCacheKeyPrefix)
}

func lyricsOriginalCacheKey(track lyrics.TrackInfo, identity lyric.VariantIdentity) (string, bool) {
	trackKey, ok := lyricsTrackKey(track)
	sourceID, sourceOK := identity.InstanceSourceID()
	if !ok || !sourceOK || !identity.Valid() {
		return "", false
	}
	return fmt.Sprintf("%s%s:%s:%s", lyricsOriginalCacheKeyPrefix, sourceID, trackKey, identity.Key()), true
}

// prepareCandidate assigns the instance/recording identity and stores the
// native document as a separate bounded artifact. The summary reflects what is
// actually retrievable, so a refused write never advertises an original.
func (p *Provider) prepareCandidate(track lyrics.TrackInfo, sourceID string, candidate *lyric.Candidate) {
	if candidate == nil || !candidate.Present() {
		return
	}
	p.assignIdentity(sourceID, candidate)
	if candidate.Identity == nil {
		candidate.OriginalSummary = lyric.UnavailableOriginalInfo(lyric.OriginalReasonNoDocument)
		return
	}
	if candidate.Original == nil {
		candidate.OriginalSummary = candidate.OriginalDetails()
		return
	}
	if p.storeOriginalArtifact(track, *candidate) {
		candidate.OriginalSummary = candidate.Original.Info()
	} else {
		candidate.OriginalSummary = lyric.UnavailableOriginalInfo(lyric.OriginalReasonCacheRefused)
	}
	// The bytes are no longer needed in memory; the artifact is authoritative.
	candidate.Original = nil
}

// storeOriginalArtifact writes one exact native document under its identity key
// with the candidate's freshness bound.
func (p *Provider) storeOriginalArtifact(track lyrics.TrackInfo, candidate lyric.Candidate) bool {
	if candidate.Identity == nil || candidate.Original == nil {
		return false
	}
	key, ok := lyricsOriginalCacheKey(track, *candidate.Identity)
	if !ok {
		return false
	}
	ttl := sourceCacheTTLSeconds(candidate.Quality)
	value, err := json.Marshal(cachedOriginalEnvelope{
		Generation: lyricsCacheGeneration,
		Identity:   *candidate.Identity,
		Document:   *candidate.Original,
		ExpiresAt:  p.now().Add(time.Duration(ttl) * time.Second).Unix(),
	})
	if err != nil {
		p.logf(fmt.Sprintf("lyrics original encode failed: %v", p.safeDiagnosticError(err)))
		return false
	}
	return p.storeCandidateBytes(key, value, len(value) > MaxCacheEntryBytes, ttl)
}

// readOriginalArtifact reads one exact identity's document without renewing it.
func (p *Provider) readOriginalArtifact(track lyrics.TrackInfo, identity lyric.VariantIdentity) (lyric.Original, bool) {
	key, ok := lyricsOriginalCacheKey(track, identity)
	if !ok {
		return lyric.Original{}, false
	}
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		return lyric.Original{}, false
	}
	var envelope cachedOriginalEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		p.logf(fmt.Sprintf("lyrics original decode failed: %v", p.safeDiagnosticError(err)))
		return lyric.Original{}, false
	}
	if envelope.Generation != lyricsCacheGeneration || !envelope.Identity.Equal(identity) {
		return lyric.Original{}, false
	}
	if envelope.ExpiresAt > 0 && envelope.ExpiresAt <= p.now().Unix() {
		return lyric.Original{}, false
	}
	if envelope.Document.Format == "" || len(envelope.Document.Bytes) == 0 || len(envelope.Document.Bytes) > lyric.MaxDocumentBytes {
		return lyric.Original{}, false
	}
	return envelope.Document, true
}

// cacheSourceCandidates persists the best candidate as the legacy per-source
// entry (winner re-rank and old readers) and every candidate in a bounded
// bundle for playback v2 and the variant listing. It reports whether the legacy
// entry landed, preserving the sweep's coverage contract.
func (p *Provider) cacheSourceCandidates(track lyrics.TrackInfo, sourceID string, candidates []lyric.Candidate) bool {
	kept := make([]lyric.Candidate, 0, len(candidates))
	var best lyric.Candidate
	for index := range candidates {
		candidate := candidates[index]
		if !candidate.Present() {
			kept = append(kept, candidate)
			continue
		}
		if candidate.Identity == nil {
			p.prepareCandidate(track, sourceID, &candidate)
		}
		kept = append(kept, candidate)
		if candidate.Playable() {
			best = lyric.Prefer(best, candidate)
		}
	}
	if len(kept) == 0 {
		return false
	}
	credentials := p.sourceCredentialsFingerprint(sourceID)
	stored := false
	if best.Present() {
		if key, ok := lyricsCacheKeyForSource(track, sourceID); ok {
			stored = p.storeCandidateValue(key, best, sourceCacheTTLSeconds(best.Quality), credentials)
		}
	}
	p.storeSourceBundle(track, sourceID, kept, credentials)
	return stored
}

func (p *Provider) storeSourceBundle(track lyrics.TrackInfo, sourceID string, candidates []lyric.Candidate, credentials string) {
	key, ok := lyricsVariantCacheKey(track, sourceID)
	if !ok {
		return
	}
	now := p.now().Unix()
	maxTTL := int64(0)
	entries := make([]cachedSourceVariant, 0, len(candidates))
	evidence := make([]lyric.Evidence, 0)
	for _, candidate := range candidates {
		evidence = append(evidence, candidate.Evidence...)
		if !candidate.Present() {
			continue
		}
		candidateTTL := sourceCacheTTLSeconds(candidate.Quality)
		if candidateTTL > maxTTL {
			maxTTL = candidateTTL
		}
		original := candidate.OriginalSummary
		entries = append(entries, cachedSourceVariant{
			Quality:   candidate.Quality,
			Response:  candidate.Response,
			Variant:   candidate.Identity,
			Original:  &original,
			Match:     candidate.Match,
			ExpiresAt: now + candidateTTL,
		})
	}
	if len(evidence) > 0 && maxTTL < lyricsCacheHourTTLSeconds {
		maxTTL = lyricsCacheHourTTLSeconds
	}
	if maxTTL <= 0 {
		return
	}
	encode := func(candidates []cachedSourceVariant) ([]byte, error) {
		return json.Marshal(cachedSourceBundle{
			Generation:  lyricsCacheGeneration,
			Candidates:  candidates,
			Evidence:    dedupeEvidence(evidence),
			Credentials: credentials,
			ExpiresAt:   now + maxTTL,
		})
	}
	value, err := encode(entries)
	if err != nil {
		p.logf(fmt.Sprintf("lyrics variant bundle encode failed: %v", p.safeDiagnosticError(err)))
		return
	}
	if len(value) > MaxCacheEntryBytes {
		// Deterministically keep the highest-priority candidates that fit,
		// skipping an oversized entry instead of discarding later ones. A
		// dropped selection stays absent (and therefore unavailable); it is
		// never remapped to another candidate.
		kept := make([]cachedSourceVariant, 0, len(entries))
		for _, entry := range entries {
			trial := append(kept, entry)
			trialValue, trialErr := encode(trial)
			if trialErr != nil {
				continue
			}
			if len(trialValue) <= MaxCacheEntryBytes {
				kept = trial
				value = trialValue
			}
		}
		if len(kept) == 0 {
			return
		}
	}
	p.storeCandidateBytes(key, value, false, maxTTL)
}

// readCachedSourceCandidates returns every cached representation for a source.
// A legacy single entry is exposed as a one-candidate view; a new identity is
// computed from its content so retrieval can refetch that exact selection.
func (p *Provider) readCachedSourceCandidates(track lyrics.TrackInfo, sourceID string) []lyric.Candidate {
	key, ok := lyricsVariantCacheKey(track, sourceID)
	if ok {
		value, exists, err := p.cache.Get(key)
		if err == nil && exists {
			var bundle cachedSourceBundle
			if err := json.Unmarshal(value, &bundle); err == nil &&
				bundle.Generation == lyricsCacheGeneration &&
				bundle.Credentials == p.sourceCredentialsFingerprint(sourceID) &&
				(bundle.ExpiresAt == 0 || bundle.ExpiresAt > p.now().Unix()) {
				candidates := make([]lyric.Candidate, 0, len(bundle.Candidates))
				for _, entry := range bundle.Candidates {
					if entry.ExpiresAt > 0 && entry.ExpiresAt <= p.now().Unix() {
						continue
					}
					candidate := lyric.Candidate{
						Response: entry.Response,
						Quality:  entry.Quality,
						Identity: entry.Variant,
						Match:    entry.Match,
					}
					if candidate.Match.Status == "" {
						candidate.Match = lyric.RecordingMatch{Status: lyric.MatchUnassessed}
					}
					if entry.Original != nil {
						candidate.OriginalSummary = *entry.Original
					}
					if candidate.Present() {
						if candidate.Identity != nil && candidate.Identity.Instance != p.sourceInstanceID(sourceID) {
							// A stale instance (credential/config change) stays
							// readable playback but is not addressable.
							candidate.Identity = nil
						} else if candidate.Identity == nil {
							p.assignIdentity(sourceID, &candidate)
						}
						candidates = append(candidates, candidate)
					}
				}
				if len(candidates) > 0 {
					return candidates
				}
			}
		}
	}
	candidate, ok := p.readCachedSourceCandidate(track, sourceID)
	if !ok || !candidate.Present() {
		return nil
	}
	if candidate.Identity != nil && candidate.Identity.Instance != p.sourceInstanceID(sourceID) {
		// Content fetched under an earlier credential/config is readable but
		// not addressable: retrieval must refetch under the current instance.
		candidate.Identity = nil
	} else if candidate.Identity == nil {
		p.assignIdentity(sourceID, &candidate)
	}
	return []lyric.Candidate{candidate}
}

// readCachedSourceEvidence returns typed assertions stored with a source's
// candidate bundle. It never fabricates evidence from misses or errors.
func (p *Provider) readCachedSourceEvidence(track lyrics.TrackInfo, sourceID string) []lyric.Evidence {
	key, ok := lyricsVariantCacheKey(track, sourceID)
	if !ok {
		return nil
	}
	value, exists, err := p.cache.Get(key)
	if err != nil || !exists {
		return nil
	}
	var bundle cachedSourceBundle
	if err := json.Unmarshal(value, &bundle); err != nil ||
		bundle.Generation != lyricsCacheGeneration ||
		bundle.Credentials != p.sourceCredentialsFingerprint(sourceID) ||
		(bundle.ExpiresAt > 0 && bundle.ExpiresAt <= p.now().Unix()) {
		return nil
	}
	return bundle.Evidence
}

// sourceEvidence merges winner-envelope and per-source assertions without
// double-counting, so cached reads keep the same conflict visibility as a fresh
// lookup.
func (p *Provider) sourceEvidence(track lyrics.TrackInfo, sources []source.Source, winner []lyric.Evidence) []lyric.Evidence {
	evidence := append([]lyric.Evidence{}, winner...)
	for _, src := range sources {
		evidence = append(evidence, p.readCachedSourceEvidence(track, src.ID)...)
	}
	return dedupeEvidence(evidence)
}

func dedupeEvidence(evidence []lyric.Evidence) []lyric.Evidence {
	out := make([]lyric.Evidence, 0, len(evidence))
	for _, entry := range evidence {
		exists := false
		for _, kept := range out {
			if lyric.SameEvidence(kept, entry) {
				exists = true
				break
			}
		}
		if !exists {
			out = append(out, entry)
		}
	}
	return out
}
