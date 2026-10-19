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

func init() {
	p := provider.New(provider.Deps{
		Send:  host.HTTPSend,
		Store: defaultStore(),
		// Hosts may expose a real wall clock without a real monotonic clock.
		// Discard WASI's synthetic monotonic reading so blocking HTTP calls
		// consume the source and lookup budgets on those hosts too.
		Now:         func() time.Time { return time.Now().Round(0) },
		Config:      defaultConfigLookup(),
		CreateQueue: createSweepQueue,
		EnqueueTask: enqueueTask,
		Logf:        logDebug,
		UserAgent:   pluginUserAgent(),
	})
	lyrics.Register(p)
	lifecycle.Register(p)
	taskworker.Register(p)
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
