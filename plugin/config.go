package main

import (
	"encoding/json"
	"strings"

	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

type configLookup func(key string) (string, bool)

// providerConfig is the resolved runtime configuration: enabled sources in saved
// order plus the optional secrets administrators may paste into the plugins page.
type providerConfig struct {
	Sources             []source.Source
	BetterLyricsAPIKey  string
	MusixmatchUserToken string
}

type sourceSelection struct {
	ID string `json:"id"`
	// Enabled is a pointer so an omitted property follows the schema default
	// (true) instead of decoding to false and silently dropping the source.
	Enabled *bool `json:"enabled"`
}

// isEnabled applies the schema default for an omitted enabled property.
func (selection sourceSelection) isEnabled() bool {
	return selection.Enabled == nil || *selection.Enabled
}

// readProviderConfig resolves the plugin config blob. The Navidrome config service
// hands a JSON-marshaled blob for the top-level `sources` property, so nothing here
// can query nested properties directly.
func readProviderConfig(lookup configLookup) providerConfig {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}

	var listings []sourceSelection
	listedIDs := make(map[string]bool)
	if raw, exists := lookup("sources"); exists {
		_ = json.Unmarshal([]byte(raw), &listings)
		for _, selection := range listings {
			listedIDs[selection.ID] = true
		}
	}

	current := sourcesFromSelections(listings)
	if len(current) == 0 && len(listings) == 0 {
		current = source.Defaults()
	} else {
		for _, catalogSource := range source.Catalog {
			if catalogSource.DefaultEnabled && !listedIDs[catalogSource.ID] {
				current = append(current, catalogSource)
			}
		}
	}

	var apiKey, musixmatchToken string
	if value, exists := lookup("betterLyricsApiKey"); exists && strings.TrimSpace(value) != "" {
		apiKey = strings.TrimSpace(value)
	}
	if value, exists := lookup("musixmatchUserToken"); exists && strings.TrimSpace(value) != "" {
		musixmatchToken = strings.TrimSpace(value)
	}

	return providerConfig{
		Sources:             current,
		BetterLyricsAPIKey:  apiKey,
		MusixmatchUserToken: musixmatchToken,
	}
}

// sourcesFromSelections preserves the admin-saved order, drops unknown ids, and
// keeps only explicitly enabled entries. An empty result means the blob was
// missing, unreadable, or enabled nothing, and the caller falls back to defaults.
func sourcesFromSelections(selections []sourceSelection) []source.Source {
	ordered := make([]source.Source, 0, len(selections))
	for _, selection := range selections {
		catalogSource, ok := source.ByID(selection.ID)
		if !ok || !selection.isEnabled() {
			continue
		}
		ordered = append(ordered, catalogSource)
	}
	return ordered
}
