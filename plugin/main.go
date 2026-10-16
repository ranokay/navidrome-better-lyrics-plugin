package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const (
	httpTimeoutMilliseconds = int32(6_000)
	pluginRepository        = "https://github.com/ranokay/navidrome-better-lyrics-plugin"
	// stageOneImplementedSources bounds the synchronous part of a cold lookup:
	// the first sources with an implemented fetch run here, and the background
	// sweep covers the rest without blocking the response.
	stageOneImplementedSources = 3
	stageOneLookupBudget       = 8 * time.Second
)

var errLyricsLookupBudgetExhausted = errors.New("lyrics lookup time budget exhausted")

//go:embed manifest.json
var manifestJSON []byte

type requestSender func(host.HTTPRequest) (*host.HTTPResponse, error)

// lyricsProvider resolves lyrics across all enabled sources.
type lyricsProvider struct {
	send         requestSender
	cache        lyricsCacheStore
	now          func() time.Time
	configLookup configLookup
	enqueueSweep func(lyrics.TrackInfo) error
	// sources overrides the config-derived catalog for tests; nil means prod walks
	// whatever the admin configured.
	sources []lyricsSource
}

var _ lyrics.Lyrics = (*lyricsProvider)(nil)

func init() {
	provider := newLyricsProvider(host.HTTPSend)
	lyrics.Register(provider)
}

func main() {}

func newLyricsProvider(send requestSender) *lyricsProvider {
	return newLyricsProviderWithDeps(send, defaultLyricsCache(), time.Now, defaultConfigLookup(), defaultSweepEnqueuer())
}

func newLyricsProviderWithDeps(send requestSender, cache lyricsCacheStore, now func() time.Time, lookup configLookup, enqueue func(lyrics.TrackInfo) error) *lyricsProvider {
	if cache == nil {
		cache = noopLyricsCache{}
	}
	if now == nil {
		now = time.Now
	}
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	if enqueue == nil {
		enqueue = func(lyrics.TrackInfo) error { return nil }
	}
	return &lyricsProvider{send: send, cache: cache, now: now, configLookup: lookup, enqueueSweep: enqueue}
}

// newLyricsProviderWithSources bypasses the config service for source-walk tests
// and injects stub sources directly.
func newLyricsProviderWithSources(send requestSender, cache lyricsCacheStore, now func() time.Time, sources []lyricsSource, enqueue func(lyrics.TrackInfo) error) *lyricsProvider {
	provider := newLyricsProviderWithDeps(send, cache, now, nil, enqueue)
	provider.sources = sources
	return provider
}

// GetLyrics prefers rich TTML, then synchronized provider/community results, while
// retaining plain text as the final fallback.
func (p *lyricsProvider) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	if cached, ok := p.readCachedLyrics(input.Track); ok {
		return cached, nil
	}

	response, health, err := p.fetchLyrics(input)
	if err == nil {
		p.cacheLyrics(input.Track, response, health)
	}
	return response, err
}

// fetchLyrics runs the stage-one pass: the first implemented enabled sources in
// configured order under a bounded budget, stopping early once a syllable-timed
// result lands. Remaining sources are covered by the background sweep (Task 6).
func (p *lyricsProvider) fetchLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, lookupHealth, error) {
	track := input.Track
	if title := strings.TrimSpace(track.Title); title == "" || trackArtist(track) == "" {
		return lyrics.GetLyricsResponse{}, lookupHealthy, nil
	}

	sources := p.sources
	if sources == nil {
		sources = readProviderConfig(p.configLookup).Sources
	}

	deadline := p.now().Add(stageOneLookupBudget)

	var (
		best         lyricsCandidate
		health       = lookupHealthy
		lookupErrors []error
		walked       int
		// coldHosts records hosts behind a global rate limit observed during this
		// walk. Later sources on a cold host are skipped for the rest of this
		// lookup, matching README's documented Better Lyrics shared cooldown.
		coldHosts = map[string]bool{}
	)
	for _, source := range sources {
		if walked >= stageOneImplementedSources {
			break
		}
		if source.Fetch == nil {
			continue
		}
		if coldHostFor(source, coldHosts) != "" {
			continue
		}
		walked++
		candidates, sourceHealth, err := source.Fetch(p, track, deadline)
		health = health.merge(sourceHealth)
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			health = health.merge(lookupDegraded)
			if isGlobalRateLimitError(err) {
				for _, hostName := range source.Hosts {
					coldHosts[hostName] = true
				}
			}
			continue
		}
		for _, candidate := range candidates {
			if !candidate.present() {
				continue
			}
			best = preferLyricsCandidate(best, candidate)
		}
		if best.quality == timingSyllable {
			return best.response, health, nil
		}
	}

	if best.present() {
		return best.response, health, nil
	}
	if len(lookupErrors) > 0 {
		return lyrics.GetLyricsResponse{}, health, errors.Join(lookupErrors...)
	}
	return lyrics.GetLyricsResponse{}, health, nil
}

func coldHostFor(source lyricsSource, coldHosts map[string]bool) string {
	for _, hostName := range source.Hosts {
		if coldHosts[hostName] {
			return hostName
		}
	}
	return ""
}

func (p *lyricsProvider) requestTimeoutMilliseconds(deadline time.Time) (int32, error) {
	remaining := deadline.Sub(p.now())
	if remaining <= 0 {
		return 0, errLyricsLookupBudgetExhausted
	}
	perRequest := time.Duration(httpTimeoutMilliseconds) * time.Millisecond
	if remaining < perRequest {
		perRequest = remaining
	}
	milliseconds := (perRequest + time.Millisecond - 1) / time.Millisecond
	if milliseconds < 1 {
		milliseconds = 1
	}
	return int32(milliseconds), nil
}

func (p *lyricsProvider) betterLyricsAPIKeyConfigured() bool {
	return readProviderConfig(p.configLookup).BetterLyricsAPIKey != ""
}

func sourcedLyricsResponse(text lyrics.LyricsText, provider, format string) lyrics.GetLyricsResponse {
	return lyrics.GetLyricsResponse{
		Lyrics: []lyrics.LyricsText{text},
		Source: &lyrics.LyricsSource{
			Provider: provider,
			Format:   format,
		},
	}
}

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
