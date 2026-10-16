package main

import (
	"strings"
	"testing"
)

func TestAllSources_CatalogOrderAndDefaults(t *testing.T) {
	t.Parallel()

	wantOrder := []string{
		"better-lyrics",
		"unison",
		"binimum",
		"amll",
		"luna-beat",
		"apple-lyrically",
		"better-lyrics-portato",
		"musixmatch",
		"netease",
		"luna",
		"petitlyrics",
		"lrclib",
		"better-lyrics-legato",
		"lyricify",
	}

	if len(allSources) != len(wantOrder) {
		t.Fatalf("allSources count = %d, want %d", len(allSources), len(wantOrder))
	}
	seen := make(map[string]int, len(allSources))
	for i, source := range allSources {
		seen[source.ID]++
		if want := wantOrder[i]; source.ID != want {
			t.Fatalf("allSources[%d].ID = %q, want %q", i, source.ID, want)
		}
		if source.Display == "" {
			t.Fatalf("allSources[%d].Display is empty", i)
		}
		if len(source.Hosts) == 0 {
			t.Fatalf("allSources[%d] (%s) has no hosts", i, source.ID)
		}
		for _, host := range source.Hosts {
			if host == "" || strings.Contains(host, "://") {
				t.Fatalf("allSources[%d] (%s) host %q must be a bare host", i, source.ID, host)
			}
		}
	}
	for _, id := range wantOrder {
		if seen[id] != 1 {
			t.Fatalf("source id %q appears %d times, want 1", id, seen[id])
		}
	}
	offByDefault := map[string]bool{"luna-beat": true, "apple-lyrically": true}
	for _, source := range allSources {
		if wantOn := !offByDefault[source.ID]; source.DefaultEnabled != wantOn {
			t.Fatalf("source %q DefaultEnabled = %v, want %v", source.ID, source.DefaultEnabled, wantOn)
		}
	}
}

func defaultSourceIDs() []string {
	ids := make([]string, 0, len(allSources))
	for _, source := range allSources {
		if source.DefaultEnabled {
			ids = append(ids, source.ID)
		}
	}
	return ids
}

func sourceIDs(sources []lyricsSource) []string {
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	return ids
}

func countIn(ids []string, id string) int {
	count := 0
	for _, candidate := range ids {
		if candidate == id {
			count++
		}
	}
	return count
}

func lookupWith(sourcesJSON string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if key == "sources" {
			return sourcesJSON, true
		}
		return "", false
	}
}

func TestReadProviderConfig_PreservesOrderAndDropsUnknown(t *testing.T) {
	t.Parallel()

	// Named entries keep their saved order; unknown ids are dropped; listed-but-
	// disabled sources stay out (spec: disabled means never queried); unlisted
	// default-enabled sources are appended so older configs pick up new versions.
	config := readProviderConfig(lookupWith(`[{"id":"lrclib","enabled":true},{"id":"bogus","enabled":true},{"id":"better-lyrics","enabled":false}]`))

	got := sourceIDs(config.Sources)
	if got[0] != "lrclib" || countIn(got, "lrclib") != 1 {
		t.Fatalf("config.Sources = %v, want saved ids preserved exactly once", got)
	}
	if countIn(got, "better-lyrics") != 0 {
		t.Fatalf("explicitly disabled better-lyrics must not be queried or appended: %v", got)
	}
	for _, missing := range []string{"unison", "binimum", "amll", "better-lyrics-portato", "musixmatch", "netease", "luna", "petitlyrics", "better-lyrics-legato", "lyricify"} {
		if countIn(got, missing) != 1 {
			t.Fatalf("catalog source %q missing after previous-version config: %v", missing, got)
		}
	}
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestReadProviderConfig_FallsBackToDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		lookup func(string) (string, bool)
		want   []string
	}{
		{name: "sources key missing", lookup: func(string) (string, bool) { return "", false }, want: defaultSourceIDs()},
		{name: "empty array", lookup: lookupWith("[]"), want: defaultSourceIDs()},
		{name: "invalid json", lookup: lookupWith("not json"), want: defaultSourceIDs()},
		// A listed entry without "enabled" is disabled (JSON zero value), so the
		// enabled set is the default set minus the explicitly disabled source.
		{name: "entry missing enabled flag is disabled", lookup: lookupWith(`[{"id":"lrclib"}]`), want: withoutID(defaultSourceIDs(), "lrclib")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := readProviderConfig(test.lookup)
			if got := sourceIDs(config.Sources); !equalIDs(got, test.want) {
				t.Fatalf("config.Sources order = %v, want %v", got, test.want)
			}
		})
	}
}

func withoutID(ids []string, id string) []string {
	out := make([]string, 0, len(ids))
	for _, candidate := range ids {
		if candidate != id {
			out = append(out, candidate)
		}
	}
	return out
}

func TestReadProviderConfig_ReadsSecrets(t *testing.T) {
	t.Parallel()

	config := readProviderConfig(func(key string) (string, bool) {
		switch key {
		case "sources":
			return "[]", true
		case "betterLyricsApiKey":
			return " key-value ", true
		case "musixmatchUserToken":
			return "token-value", true
		}
		return "", false
	})

	if config.BetterLyricsAPIKey != "key-value" {
		t.Fatalf("BetterLyricsAPIKey = %q, want trimmed %q", config.BetterLyricsAPIKey, "key-value")
	}
	if config.MusixmatchUserToken != "token-value" {
		t.Fatalf("MusixmatchUserToken = %q, want %q", config.MusixmatchUserToken, "token-value")
	}
}
