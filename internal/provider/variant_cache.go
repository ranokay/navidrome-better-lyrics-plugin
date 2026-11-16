package provider

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

// cachedSourceVariant is one addressable representation stored in a source
// bundle. Every candidate an adapter returned is kept, not only the winner.
type cachedSourceVariant struct {
	Quality  lyric.TimingQuality      `json:"quality"`
	Response lyrics.GetLyricsResponse `json:"response"`
	Variant  *lyric.VariantIdentity   `json:"variant,omitempty"`
	Original *lyric.OriginalInfo      `json:"original,omitempty"`
}

type cachedSourceBundle struct {
	Generation  int                   `json:"generation,omitempty"`
	Candidates  []cachedSourceVariant `json:"candidates,omitempty"`
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
	trackKey, ok := lyricsTrackKey(track)
	if !ok || sourceID == "" {
		return "", false
	}
	return fmt.Sprintf("%s%s:%s", lyricsVariantCacheKeyPrefix, sourceID, trackKey), true
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
	present := make([]lyric.Candidate, 0, len(candidates))
	var best lyric.Candidate
	for index := range candidates {
		candidate := candidates[index]
		if !candidate.Present() {
			continue
		}
		if candidate.Identity == nil {
			p.prepareCandidate(track, sourceID, &candidate)
		}
		candidates[index] = candidate
		present = append(present, candidate)
		best = lyric.Prefer(best, candidate)
	}
	if len(present) == 0 {
		return false
	}
	key, ok := lyricsCacheKeyForSource(track, sourceID)
	if !ok {
		return false
	}
	credentials := p.sourceCredentialsFingerprint(sourceID)
	stored := p.storeCandidateValue(key, best, sourceCacheTTLSeconds(best.Quality), credentials)

	p.storeSourceBundle(track, sourceID, present, credentials)
	return stored
}

func (p *Provider) storeSourceBundle(track lyrics.TrackInfo, sourceID string, candidates []lyric.Candidate, credentials string) {
	key, ok := lyricsVariantCacheKey(track, sourceID)
	if !ok {
		return
	}
	now := p.now().Unix()
	ttl := int64(0)
	entries := make([]cachedSourceVariant, 0, len(candidates))
	for _, candidate := range candidates {
		candidateTTL := sourceCacheTTLSeconds(candidate.Quality)
		if ttl == 0 || candidateTTL < ttl {
			ttl = candidateTTL
		}
		original := candidate.OriginalSummary
		entries = append(entries, cachedSourceVariant{
			Quality:  candidate.Quality,
			Response: candidate.Response,
			Variant:  candidate.Identity,
			Original: &original,
		})
	}
	if ttl <= 0 {
		return
	}
	encode := func(candidates []cachedSourceVariant) ([]byte, error) {
		return json.Marshal(cachedSourceBundle{
			Generation:  lyricsCacheGeneration,
			Candidates:  candidates,
			Credentials: credentials,
			ExpiresAt:   now + ttl,
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
	p.storeCandidateBytes(key, value, false, ttl)
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
					candidate := lyric.Candidate{
						Response: entry.Response,
						Quality:  entry.Quality,
						Identity: entry.Variant,
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
