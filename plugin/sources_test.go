package main

import "testing"

func catalogSources(ids []string) []lyricsSource {
	sources := make([]lyricsSource, 0, len(ids))
	for _, id := range ids {
		if source, ok := catalogSource(id); ok {
			sources = append(sources, source)
		}
	}
	return sources
}

func TestAllSources_ImplementedFetchesAreAttached(t *testing.T) {
	t.Parallel()

	// Sources whose Fetch is nil are only unreachable skeletons: they must be a
	// deliberately bounded set, not silent wiring drops.
	wantUnimplemented := map[string]bool{
		"luna-beat":       true,
		"apple-lyrically": true,
		"musixmatch":      true,
		"netease":         true,
		"luna":            true,
		"petitlyrics":     true,
		"lyricify":        true,
	}
	for _, source := range allSources {
		_, wired := sourceFetches[source.ID]
		if wantUnimplemented[source.ID] {
			if wired {
				t.Fatalf("source %q is expected unimplemented, yet sourceFetches wires it", source.ID)
			}
			continue
		}
		if !wired || source.Fetch == nil {
			t.Fatalf("source %q has no Fetch wiring", source.ID)
		}
	}
}
