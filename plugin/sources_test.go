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
	for _, source := range allSources {
		if _, wired := sourceFetches[source.ID]; !wired {
			t.Fatalf("source %q has no Fetch wiring", source.ID)
		}
	}
}
