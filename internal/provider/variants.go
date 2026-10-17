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
		if seen[src.Display] {
			continue
		}
		candidate, ok := p.readCachedSourceCandidate(track, src.ID)
		if !ok || !candidate.Present() {
			continue
		}
		seen[src.Display] = true
		for _, text := range candidate.Response.Lyrics {
			if text.Source == nil {
				text.Source = candidate.Response.Source
			}
			entries = append(entries, text)
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
