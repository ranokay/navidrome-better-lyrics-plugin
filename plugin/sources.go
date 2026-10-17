package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

type configLookup func(key string) (string, bool)

// lyricsSource describes one upstream lyrics backend participating in the chain.
// Fetch performs the lookup and reports per-source health alongside candidates; it is
// wired per source by the tasks that implement it.
type lyricsSource struct {
	ID             string
	Display        string
	Hosts          []string
	DefaultEnabled bool
	Fetch          func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error)
}

// allSources is the full catalog in default config order. Sourced display names match
// the labels users see in the Navidrome plugins page and, later, in the lyrics UI.
var allSources = []lyricsSource{
	{ID: "better-lyrics", Display: "Better Lyrics", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true},
	{ID: "unison", Display: "Unison", Hosts: []string{"unison.boidu.dev"}, DefaultEnabled: true},
	{ID: "binimum", Display: "BiniLyrics", Hosts: []string{"lyrics-api.binimum.org", "lrc.red"}, DefaultEnabled: true},
	{ID: "amll", Display: "AMLL", Hosts: []string{"api.amll.dev"}, DefaultEnabled: true},
	{ID: "luna-beat", Display: "LunaBeat", Hosts: []string{"2755337087.github.io"}, DefaultEnabled: false},
	{ID: "apple-lyrically", Display: "Apple Music (Lyrically)", Hosts: []string{"itunes.apple.com", "lyrics.paxsenix.org"}, DefaultEnabled: false},
	{ID: "better-lyrics-portato", Display: "Better Lyrics Portato", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true},
	{ID: "musixmatch", Display: "Musixmatch", Hosts: []string{"apic.musixmatch.com"}, DefaultEnabled: true},
	{ID: "netease", Display: "NetEase", Hosts: []string{"music.163.com"}, DefaultEnabled: true},
	{ID: "luna", Display: "Soda (Luna)", Hosts: []string{"api.qishui.com", "beta-luna.douyin.com"}, DefaultEnabled: true},
	{ID: "petitlyrics", Display: "PetitLyrics", Hosts: []string{"p0.petitlyrics.com"}, DefaultEnabled: true},
	{ID: "lrclib", Display: "LRCLib", Hosts: []string{"lrclib.net"}, DefaultEnabled: true},
	{ID: "better-lyrics-legato", Display: "Better Lyrics Legato", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true},
	{ID: "lyricify", Display: "Lyricify", Hosts: []string{"api.credits.fm", "api.lyricify.app"}, DefaultEnabled: true},
	{ID: "lyricsplus", Display: "LyricsPlus", Hosts: []string{"lyricsplus.binimum.org", "lyricsplus.atomix.one"}, DefaultEnabled: true},
	{ID: "yt-music", Display: "YouTube Music", Hosts: []string{"music.youtube.com"}, DefaultEnabled: false},
	{ID: "genius", Display: "Genius", Hosts: []string{"genius.com"}, DefaultEnabled: false},
}

// Fetches are attached at init rather than in the literal above so that source
// files can consult the config reader without creating an initialization cycle.
// Every source with an implemented Fetch MUST appear in sourceFetches — the
// test below pins nothing but the compiler would still accept a missing entry, so
// the coverage check lives in sources_test.go (TestAllSources_ImplementedFetches).
func init() {
	for i := range allSources {
		if fn, ok := sourceFetches[allSources[i].ID]; ok {
			allSources[i].Fetch = fn
		}
	}
}

var sourceFetches = map[string]func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, lookupHealth, error){
	"better-lyrics":         fetchBetterLyricsLyrics,
	"unison":                fetchUnisonCandidates,
	"binimum":               fetchBinimumLyricsCandidates,
	"amll":                  fetchAMLLyricsCandidates,
	"better-lyrics-portato": fetchPortatoLyricsCandidates,
	"lrclib":                fetchLRCLibLyricsCandidates,
	"netease":               fetchNetEaseLyricsCandidates,
	"luna":                  fetchLunaLyricsCandidates,
	"petitlyrics":           fetchPetitLyricsCandidates,
	"apple-lyrically":       fetchLyricallyLyricsCandidates,
	"lyricify":              fetchLyricifyLyricsCandidates,
	"luna-beat":             fetchLunaBeatLyricsCandidates,
	"musixmatch":            fetchMusixmatchLyricsCandidates,
	"better-lyrics-legato":  fetchKugouLyricsCandidates,
	"lyricsplus":            fetchLyricsPlusLyricsCandidates,
	"yt-music":              fetchYTMusicLyricsCandidates,
	"genius":                fetchGeniusLyricsCandidates,
}

// catalogSource resolves a catalog entry live so callers always see the Fetch
// wiring attached by init(); do not snapshot the catalog into a package map, or
// the variable initialization order races ahead of init().
func catalogSource(id string) (lyricsSource, bool) {
	for _, source := range allSources {
		if source.ID == id {
			return source, true
		}
	}
	return lyricsSource{}, false
}

// providerConfig is the resolved runtime configuration: enabled sources in saved
// order plus the optional secrets administrators may paste into the plugins page.
type providerConfig struct {
	Sources             []lyricsSource
	BetterLyricsAPIKey  string
	MusixmatchUserToken string
}

type sourceSelection struct {
	ID string `json:"id"`
	// Enabled is a pointer so an omitted property follows the schema default
	// (true) instead of decoding to false and silently dropping the source
	// (Codex).
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
		current = defaultSources()
	} else {
		for _, source := range allSources {
			if source.DefaultEnabled && !listedIDs[source.ID] {
				current = append(current, source)
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
func sourcesFromSelections(selections []sourceSelection) []lyricsSource {
	ordered := make([]lyricsSource, 0, len(selections))
	for _, selection := range selections {
		source, ok := catalogSource(selection.ID)
		if !ok || !selection.isEnabled() {
			continue
		}
		ordered = append(ordered, source)
	}
	return ordered
}

// sourceIDs lists the catalog ids in the given order; the winner cache uses them
// as the fingerprint input.
func sourceIDs(sources []lyricsSource) []string {
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	return ids
}

func defaultSources() []lyricsSource {
	sources := make([]lyricsSource, 0, len(allSources))
	for _, source := range allSources {
		if source.DefaultEnabled {
			sources = append(sources, source)
		}
	}
	return sources
}
