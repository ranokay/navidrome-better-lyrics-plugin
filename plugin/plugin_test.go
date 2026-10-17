package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/provider"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func TestPluginVersion(t *testing.T) {
	t.Parallel()

	if got := pluginVersion(); got != "0.3.1" {
		t.Fatalf("pluginVersion() = %q, want 0.3.1", got)
	}
}

func TestPluginUserAgentIdentifiesThePlugin(t *testing.T) {
	t.Parallel()

	// Providers that throttle unidentified clients (LRCLib) rely on this
	// string naming the plugin, its version, and the repository.
	userAgent := pluginUserAgent()
	if !strings.Contains(userAgent, "NavidromeBetterLyricsPlugin/"+pluginVersion()) || !strings.Contains(userAgent, pluginRepository) {
		t.Fatalf("pluginUserAgent() = %q, want the name, version, and repository", userAgent)
	}
}

func TestManifestAllowlistsEverySourceHost(t *testing.T) {
	t.Parallel()

	var manifest struct {
		Permissions struct {
			HTTP struct {
				RequiredHosts []string `json:"requiredHosts"`
			} `json:"http"`
			KVStore struct {
				MaxSize string `json:"maxSize"`
			} `json:"kvstore"`
			TaskQueue struct {
				MaxConcurrency int `json:"maxConcurrency"`
			} `json:"taskqueue"`
		} `json:"permissions"`
		Config struct {
			Schema map[string]interface{} `json:"schema"`
		} `json:"config"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	allowed := make(map[string]bool, len(manifest.Permissions.HTTP.RequiredHosts))
	for _, host := range manifest.Permissions.HTTP.RequiredHosts {
		allowed[host] = true
	}
	for _, src := range source.Catalog {
		for _, host := range src.Hosts {
			if !allowed[host] {
				t.Fatalf("source %q needs host %q that the manifest does not allowlist", src.ID, host)
			}
		}
	}
	if got := manifest.Permissions.KVStore.MaxSize; got != provider.CacheMaxSize {
		t.Fatalf("manifest KV store maxSize = %q, want %q", got, provider.CacheMaxSize)
	}
	if got := manifest.Permissions.TaskQueue.MaxConcurrency; got != 1 {
		t.Fatalf("manifest taskqueue maxConcurrency = %d, want 1 for the sweep queue", got)
	}

	// The config schema must expose the ordered source list plus the two secrets.
	var schema struct {
		Properties struct {
			Sources             json.RawMessage `json:"sources"`
			BetterLyricsAPIKey  json.RawMessage `json:"betterLyricsApiKey"`
			MusixmatchUserToken json.RawMessage `json:"musixmatchUserToken"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(typedManifestConfigSchema(), &schema); err != nil {
		t.Fatalf("decode config schema: %v", err)
	}
	if len(schema.Properties.Sources) == 0 || len(schema.Properties.BetterLyricsAPIKey) == 0 || len(schema.Properties.MusixmatchUserToken) == 0 {
		t.Fatal("manifest config schema must define sources and both optional secrets")
	}
}

func typedManifestConfigSchema() []byte {
	var manifest struct {
		Config struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"config"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return nil
	}
	return manifest.Config.Schema
}

func TestManifestConfigDefaultsMatchCatalog(t *testing.T) {
	t.Parallel()

	var manifest struct {
		Config struct {
			Schema map[string]interface{} `json:"schema"`
		} `json:"config"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	raw, err := json.Marshal(manifest.Config.Schema)
	if err != nil {
		t.Fatalf("re-encode schema: %v", err)
	}
	var configSchema struct {
		Properties struct {
			Sources struct {
				Items struct {
					Properties struct {
						ID struct {
							Enum []string `json:"enum"`
						} `json:"id"`
					} `json:"properties"`
				} `json:"items"`
				Defaults []struct {
					ID      string `json:"id"`
					Enabled bool   `json:"enabled"`
				} `json:"default"`
			} `json:"sources"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &configSchema); err != nil {
		t.Fatalf("decode sources schema: %v", err)
	}

	enum := configSchema.Properties.Sources.Items.Properties.ID.Enum
	if len(enum) != len(source.Catalog) {
		t.Fatalf("source enum has %d entries, want %d", len(enum), len(source.Catalog))
	}
	for i, src := range source.Catalog {
		if got := enum[i]; got != src.ID {
			t.Fatalf("enum[%d] = %q, want %q", i, got, src.ID)
		}
	}

	defaults := configSchema.Properties.Sources.Defaults
	if len(defaults) != len(source.Catalog) {
		t.Fatalf("defaults have %d entries, want one per catalog source", len(defaults))
	}
	for i, wantDefault := range defaults {
		wantOn := source.Catalog[i].DefaultEnabled
		if wantDefault.ID != source.Catalog[i].ID {
			t.Fatalf("defaults[%d] = %q, want %q", i, wantDefault.ID, source.Catalog[i].ID)
		}
		if wantDefault.Enabled != wantOn {
			t.Fatalf("defaults[%d].enabled = %v, want %v", i, wantDefault.Enabled, wantOn)
		}
	}
}
