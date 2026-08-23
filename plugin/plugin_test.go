package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPluginVersion(t *testing.T) {
	t.Parallel()

	if got := pluginVersion(); got != "0.1.0" {
		t.Fatalf("pluginVersion() = %q, want 0.1.0", got)
	}
}

func TestManifestAllowsOnlyProviderHosts(t *testing.T) {
	t.Parallel()

	var manifest struct {
		Permissions struct {
			HTTP struct {
				RequiredHosts []string `json:"requiredHosts"`
			} `json:"http"`
			KVStore struct {
				MaxSize string `json:"maxSize"`
			} `json:"kvstore"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	got := strings.Join(manifest.Permissions.HTTP.RequiredHosts, ",")
	want := "lyrics-api.boidu.dev,unison.boidu.dev"
	if got != want {
		t.Fatalf("manifest HTTP hosts = %q, want %q", got, want)
	}
	if got := manifest.Permissions.KVStore.MaxSize; got != lyricsCacheMaxSize {
		t.Fatalf("manifest KV store maxSize = %q, want %q", got, lyricsCacheMaxSize)
	}
}
