package main

import (
	_ "embed"
	"encoding/json"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/provider"
)

const pluginRepository = "https://github.com/ranokay/navidrome-better-lyrics-plugin"

//go:embed manifest.json
var manifestJSON []byte

// pluginInstance is the registered provider; the package-level reference keeps
// the PDK callbacks reachable after registration.
var pluginInstance *provider.Provider

func init() {
	pluginInstance = provider.New(provider.Deps{
		Send:        host.HTTPSend,
		Store:       defaultStore(),
		Now:         time.Now,
		Config:      defaultConfigLookup(),
		CreateQueue: createSweepQueue,
		EnqueueTask: enqueueTask,
		Logf:        logDebug,
		UserAgent:   pluginUserAgent(),
	})
	lyrics.Register(pluginInstance)
	lifecycle.Register(pluginInstance)
	taskworker.Register(pluginInstance)
}

func main() {}

func pluginUserAgent() string {
	return "NavidromeBetterLyricsPlugin/" + pluginVersion() + " (+" + pluginRepository + ")"
}

func pluginVersion() string {
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil || manifest.Version == "" {
		return "0.0.0"
	}
	return manifest.Version
}
