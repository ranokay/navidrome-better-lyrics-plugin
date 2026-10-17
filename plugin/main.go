package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
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
	// enqueueSweepSources re-enqueues a continuation carrying only the sources a
	// sweep had to defer when its budget ran out.
	enqueueSweepSources func(lyrics.TrackInfo, []string) error
	// sweepBudget overrides defaultSweepBudget (0 = default); tests shrink it to
	// force deferrals deterministically.
	sweepBudget time.Duration
	// credentialSaltBytes memoizes the per-installation HMAC salt for this
	// process so stores whose reads fail do not remint on every fingerprint.
	credentialSaltBytes []byte
}

var _ lyrics.Lyrics = (*lyricsProvider)(nil)

func init() {
	provider := newLyricsProvider(host.HTTPSend)
	lyrics.Register(provider)
	lifecycle.Register(provider)
	taskworker.Register(provider)
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
	provider := &lyricsProvider{send: send, cache: cache, now: now, configLookup: lookup, enqueueSweep: enqueue}
	if provider.enqueueSweepSources == nil {
		provider.enqueueSweepSources = defaultSweepSourcesEnqueuer()
	}
	return provider
}

// GetLyrics prefers rich TTML, then synchronized provider/community results, while
// retaining plain text as the final fallback.
func (p *lyricsProvider) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	track := input.Track
	if title := strings.TrimSpace(track.Title); title == "" || trackArtist(track) == "" {
		return lyrics.GetLyricsResponse{}, nil
	}

	sources := p.activeSources()
	if len(sources) == 0 {
		// Deliberate all-disabled opt-out (documented `sources: []` config):
		// serve nothing without touching the winner cache or task queue — an
		// empty source order creates no key and would wedge every lookup
		// through the unschedulable sweep path.
		return lyrics.GetLyricsResponse{}, nil
	}
	if candidate, ok, sweepComplete := p.readCachedWinner(track, sources); ok {
		if candidate.present() {
			if !sweepComplete {
				// The sweep may have cached a better result after this winner
				// was written: re-rank before serving.
				// Ties adopt the per-source preferred candidate,
				// which respects configured order.
				if best := p.readBestFromSourceCache(track, sources); best.present() && best.quality >= candidate.quality {
					p.cacheWinner(track, sources, best, false)
					candidate = best
				}
				p.maybeEnqueueSweep(track)
			}
			return candidate.response, nil
		}
		// A cached miss must not shield fresh sweep results: the background sweep
		// may have written better candidates per source after the miss landed.
		if best := p.readBestFromSourceCache(track, sources); best.present() {
			p.cacheWinner(track, sources, best, false)
			p.maybeEnqueueSweep(track)
			return best.response, nil
		}
		// No per-source improvement yet: the negative winner stays authoritative
		// for its TTL so replays do not re-walk the sources — but an incomplete
		// sweep must still be schedulable.
		if !sweepComplete {
			p.maybeEnqueueSweep(track)
		}
		return candidate.response, nil
	}
	if best := p.readBestFromSourceCache(track, sources); best.present() {
		p.cacheWinner(track, sources, best, false)
		p.maybeEnqueueSweep(track)
		return best.response, nil
	}

	candidate, _, coveredAll, err := p.fetchLyrics(input)
	if candidate.present() {
		p.cacheWinner(track, sources, candidate, coveredAll)
		if !coveredAll {
			p.maybeEnqueueSweep(track)
		}
		return candidate.response, nil
	}
	if err == nil {
		p.cacheNegativeWinner(track, sources, coveredAll)
		if !coveredAll {
			// A fully covered negative pass needs no sweep: every implemented
			// source already answered inside stage one.
			p.maybeEnqueueSweep(track)
		}
	} else {
		// Retain the error, but an incomplete walk must still reach the tail:
		// schedule the sweep so the returned error is the only failure the
		// request exposes.
		p.maybeEnqueueSweep(track)
	}
	return candidate.response, err
}

// activeSources resolves the source order for this lookup: the injected test
// catalog when present, otherwise the admin-configured enabled sources.
func (p *lyricsProvider) activeSources() []lyricsSource {
	if p.sources != nil {
		return p.sources
	}
	return readProviderConfig(p.configLookup).Sources
}

// fetchLyrics runs the stage-one pass: the first implemented enabled sources in
// configured order under a bounded budget, stopping early once a syllable-timed
// result lands. Each successful source result is cached per source so later
// lookups and source reordering re-rank without refetching. Remaining sources
// are covered by the background sweep (Task 6).
func (p *lyricsProvider) fetchLyrics(input lyrics.GetLyricsRequest) (lyricsCandidate, lookupHealth, bool, error) {
	track := input.Track
	if title := strings.TrimSpace(track.Title); title == "" || trackArtist(track) == "" {
		return lyricsCandidate{}, lookupHealthy, false, nil
	}

	sources := p.activeSources()
	deadline := p.now().Add(stageOneLookupBudget)

	var (
		best         lyricsCandidate
		health       = lookupHealthy
		lookupErrors []error
		walked       int
		walkedTotal  int
		// coldHosts records hosts behind a global rate limit observed during this
		// walk. Later sources on a cold host are skipped for the rest of this
		// lookup, matching README's documented Better Lyrics shared cooldown.
		coldHosts = map[string]bool{}
	)
	implementedTotal := 0
	for _, source := range sources {
		if source.Fetch != nil {
			implementedTotal++
		}
	}
	for _, source := range sources {
		if walked >= stageOneImplementedSources {
			break
		}
		if source.Fetch == nil {
			continue
		}
		// A per-source entry (positive or persisted miss) already covers this
		// source for its TTL: refetching here would re-query providers every
		// negative-winner cycle. Cached positives are served by the
		// re-rank above the walk.
		if _, ok := p.readCachedSourceCoverage(track, source.ID); ok {
			continue
		}
		if coldHostFor(source, coldHosts) != "" {
			continue
		}
		if p.sourceInCooldown(source) {
			// Cooldowns are temporary; skipping keeps the slot for the next
			// implemented source and the sweep retries it later.
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
		foundCandidate := false
		for _, candidate := range candidates {
			if !candidate.present() {
				continue
			}
			foundCandidate = true
			p.cacheSourceCandidate(track, source.ID, candidate)
			best = preferLyricsCandidate(best, candidate)
		}
		coveredThisSource := true
		if !foundCandidate {
			switch sourceHealth {
			case lookupHealthy, lookupUncertain:
				// Healthy clean misses and anonymous cache-only misses are
				// bounded coverage: persist them so the sweep and later
				// lookups skip the refetch. The miss keys
				// carry the credential fingerprint, so a configured key or
				// token re-authorizes them immediately.
				p.cacheSourceMiss(track, source.ID)
			case lookupTemporarilySkipped:
				// Token rejects are not covered work: the next lookup remints
				// and retries, and the pass must not report complete
				// foreground coverage or suppress the sweep.
				health = health.merge(sourceHealth)
				coveredThisSource = false
			}
		}
		if coveredThisSource {
			walkedTotal++
		}
		if best.quality == timingSyllable {
			return best, health, walkedTotal >= implementedTotal, nil
		}
	}

	if best.present() {
		return best, health, walkedTotal >= implementedTotal, nil
	}
	if len(lookupErrors) > 0 {
		return lyricsCandidate{}, health, false, errors.Join(lookupErrors...)
	}
	// A clean all-miss walk with full coverage is real: the negative winner
	// must record that no sweep needs re-walking the same misses.
	return lyricsCandidate{}, health, walkedTotal >= implementedTotal, nil
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
