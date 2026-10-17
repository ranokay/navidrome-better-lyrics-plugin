package source

import (
	"testing"
)

func TestCatalogInvariants(t *testing.T) {
	t.Parallel()

	// Fetch wiring is inline, so an unwired source no longer compiles; what
	// remains pinnable is unique ids, display names, and advertised hosts.
	seen := make(map[string]bool, len(Catalog))
	for _, src := range Catalog {
		if src.ID == "" || src.Display == "" {
			t.Fatalf("catalog entry missing id/display: %#v", src)
		}
		if seen[src.ID] {
			t.Fatalf("duplicate catalog id %q", src.ID)
		}
		seen[src.ID] = true
		if src.Fetch == nil {
			t.Fatalf("source %q has no Fetch wiring", src.ID)
		}
		if len(src.Hosts) == 0 {
			t.Fatalf("source %q advertises no hosts", src.ID)
		}
	}
}
