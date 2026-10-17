package provider

import (
	"testing"

	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func TestConfigSelectionOmittingEnabledUsesSchemaDefault(t *testing.T) {
	t.Parallel()

	// `{"id":"better-lyrics"}` must keep the source enabled (schema default
	// true) instead of silently dropping it and appending the other defaults.
	lookup := func(key string) (string, bool) {
		if key == "sources" {
			return `[{"id":"better-lyrics"}]`, true
		}
		return "", false
	}
	resolved := readProviderConfig(lookup)
	ids := source.IDs(resolved.Sources)
	// The upgrade-compat pass still appends unlisted default sources, but the
	// listed entry itself must not vanish (it decoded as disabled pre-fix).
	found := false
	for _, id := range ids {
		if id == "better-lyrics" {
			found = true
		}
	}
	if !found {
		t.Fatalf("resolved = %v, want the listed source kept enabled by the schema default", ids)
	}
}
