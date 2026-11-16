package provider

import (
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// responsesWithVariants augments the winner's response with every other cached
// source-tagged variant, winner first and the rest in configured source order.
// Serving them from the per-source cache (rather than from the winner's cached
// response) keeps the list fresh: sources the sweep has filled since the winner
// was written still show up, and per-source TTLs are never shorter than the
// winner's. The contract treats the first entry as the winner; the response
// level source still names it for hosts that only read that.
func (p *Provider) responsesWithVariants(track lyrics.TrackInfo, sources []source.Source, winner lyric.Candidate) lyrics.GetLyricsResponse {
	response := winner.Response
	if !winner.Present() || len(response.Lyrics) == 0 {
		return response
	}

	entries := make([]lyrics.LyricsText, 0, len(response.Lyrics)+len(sources))
	seen := make(map[string]bool, len(sources)+1)
	for _, text := range response.Lyrics {
		if text.Source == nil {
			text.Source = response.Source
		}
		if provider := sourceProvider(text.Source); provider != "" {
			seen[provider] = true
		}
		entries = append(entries, text)
	}

	for _, src := range sources {
		candidate, ok := p.readCachedSourceCandidate(track, src.ID)
		if !ok || !candidate.Present() {
			continue
		}
		// Some fetchers return a dynamic upstream label rather than the
		// catalog display name. Deduplicate by that returned identity, which
		// also prevents repeating the winner under its catalog alias.
		candidateProviders := make(map[string]bool)
		for _, text := range candidate.Response.Lyrics {
			if text.Source == nil {
				text.Source = candidate.Response.Source
			}
			provider := sourceProvider(text.Source)
			if provider != "" && seen[provider] {
				continue
			}
			entries = append(entries, text)
			candidateProviders[provider] = true
		}
		for provider := range candidateProviders {
			seen[provider] = true
		}
	}

	response.Lyrics = entries
	return response
}

// sourceProvider reads an entry's upstream label, tolerating a missing source.
func sourceProvider(from *lyrics.LyricsSource) string {
	if from == nil {
		return ""
	}
	return strings.TrimSpace(from.Provider)
}

// MaxListedVariants bounds one variant response. Playback v2 and the listing
// share this bound, so every emitted identity is listable. Dropped selections
// stay absent (and therefore unavailable); they are never remapped.
const MaxListedVariants = 64

// VariantEntry is one playback representation with its addressable identity.
type VariantEntry struct {
	Text     lyrics.LyricsText
	Identity *lyric.VariantIdentity
	Original lyric.OriginalInfo
}

// V2Response is the optional playback response: every representation from every
// source, winner first, each carrying its exact identity.
type V2Response struct {
	Entries []VariantEntry
	Source  *lyrics.LyricsSource
}

// VariantSummary is one listing row. PlaybackIndex is advisory: identity is the
// stable link to the playback entry.
type VariantSummary struct {
	Identity      lyric.VariantIdentity
	Provider      string
	Language      string
	Role          string
	Format        string
	PlaybackIndex int
	Original      lyric.OriginalInfo
}

// buildV2Response emits the winner's entries first, then every cached
// representation from each source in configured order, deduplicated by
// identity. Adapters that returned several representations keep all of them.
func (p *Provider) buildV2Response(track lyrics.TrackInfo, sources []source.Source, winner lyric.Candidate) V2Response {
	entries := make([]VariantEntry, 0, len(sources)+1)
	seen := make(map[string]bool, len(sources)+1)
	appendCandidate := func(candidate lyric.Candidate) {
		if !candidate.Present() {
			return
		}
		key := ""
		if candidate.Identity != nil {
			key = "identity:" + candidate.Identity.Key()
		} else {
			key = "legacy:" + candidate.Provider() + "\x00" + candidate.Format()
		}
		if seen[key] {
			return
		}
		seen[key] = true
		for _, text := range candidate.Response.Lyrics {
			if len(entries) >= MaxListedVariants {
				break
			}
			if text.Source == nil {
				text.Source = candidate.Response.Source
			}
			entries = append(entries, VariantEntry{
				Text:     text,
				Identity: candidate.Identity,
				Original: candidate.OriginalDetails(),
			})
		}
	}

	if winner.Identity != nil {
		sourceID, ok := winner.Identity.InstanceSourceID()
		if !ok || p.sourceInstanceID(sourceID) != winner.Identity.Instance || !p.cachedIdentityListed(track, *winner.Identity) {
			// The bundle is the authoritative addressable set. A winner
			// dropped by bundle truncation remains playable but is not
			// exposed as a retrievable selection.
			winner.Identity = nil
		}
	}
	appendCandidate(winner)
	for _, src := range sources {
		for _, candidate := range p.readCachedSourceCandidates(track, src.ID) {
			appendCandidate(candidate)
		}
	}
	var responseSource *lyrics.LyricsSource
	if winner.Response.Source != nil {
		responseSource = winner.Response.Source
	}
	return V2Response{Entries: entries, Source: responseSource}
}

// cachedIdentityListed reports whether one identity is part of the current
// addressable source bundle.
func (p *Provider) cachedIdentityListed(track lyrics.TrackInfo, identity lyric.VariantIdentity) bool {
	sourceID, ok := identity.InstanceSourceID()
	if !ok {
		return false
	}
	for _, candidate := range p.readCachedSourceCandidates(track, sourceID) {
		if candidate.Identity != nil && candidate.Identity.Equal(identity) {
			return true
		}
	}
	return false
}

// GetLyricsVariants lists the currently cached, addressable representations.
// It never fetches: playback has already populated the caches, and a cold
// listing stays empty rather than consuming the foreground budget.
func (p *Provider) GetLyricsVariants(track lyrics.TrackInfo) []VariantSummary {
	if len(strings.TrimSpace(track.Title)) == 0 || lyric.TrackArtist(track) == "" {
		return nil
	}
	sources := p.activeSources()
	if len(sources) == 0 {
		return nil
	}
	candidate, ok, sweepComplete := p.readCachedWinner(track, sources)
	if ok && candidate.Present() && !sweepComplete {
		if best, _ := p.readBestFromSourceCache(track, sources); best.Present() && best.Quality >= candidate.Quality {
			candidate = best
		}
	}
	if !ok || !candidate.Present() {
		best, _ := p.readBestFromSourceCache(track, sources)
		if !best.Present() {
			// The legacy per-source entry may have been refused while a
			// smaller bundled representation fit; list that instead.
			for _, src := range sources {
				for _, bundled := range p.readCachedSourceCandidates(track, src.ID) {
					best = lyric.Prefer(best, bundled)
				}
			}
		}
		if !best.Present() {
			return nil
		}
		candidate = best
	}

	response := p.buildV2Response(track, sources, candidate)
	summaries := make([]VariantSummary, 0, len(response.Entries))
	for index, entry := range response.Entries {
		if entry.Identity == nil {
			continue
		}
		provider := ""
		if entry.Text.Source != nil {
			provider = entry.Text.Source.Provider
		}
		summaries = append(summaries, VariantSummary{
			Identity:      *entry.Identity,
			Provider:      provider,
			Language:      entry.Identity.Language,
			Role:          entry.Identity.Role,
			Format:        entry.Identity.Format,
			PlaybackIndex: index,
			Original:      entry.Original,
		})
		if len(summaries) >= MaxListedVariants {
			break
		}
	}
	return summaries
}
