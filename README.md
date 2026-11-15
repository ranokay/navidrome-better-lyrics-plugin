<p align="center"><img src="docs/assets/icon.png" alt="ND Better Lyrics plugin icon" width="180"></p>

# Navidrome Better Lyrics plugin

A Navidrome lyrics-provider plugin that fills the gap when a track has no local lyrics. It resolves tracks through sixteen configurable sources, including the Better Lyrics API (syllable TTML / word-synced QQ "Portato" / line-synced Kugou "Legato"), the Unison community database, BiniLyrics, AMLL, LRCLib, NetEase, Soda (Luna), PetitLyrics, Lyricify, LyricsPlus, YouTube Music, Genius, and two community mirrors (LunaBeat and an Apple Music mirror via Lyrically).

The plugin does not inject or replace Navidrome's interface. It returns TTML, enhanced LRC, or plain lyrics through Navidrome's Lyrics capability, converting proprietary word-timed formats into enhanced LRC so compatible clients can render word-level highlighting.

> [!NOTE]
> As checked on 2026-11-15, the latest Navidrome release is v0.64.2.
> The responsive sidebar ([#5733](https://github.com/navidrome/navidrome/pull/5733))
> and source provenance/controls ([#6033](https://github.com/navidrome/navidrome/pull/6033))
> remain open upstream. The plugin already returns labeled variants; displaying
> those labels and controls requires a compatible host build.
> [Current contracts and remaining work](docs/current-state.md) distinguish
> released support, local changes, and upstream dependencies.

## Requirements

- Navidrome v0.63.2 or newer
- Navidrome plugins enabled
- A Navidrome client that displays synchronized lyrics
- For the responsive sidebar UI specifically: a build with navidrome/navidrome#5733 (see the note above)
- For source details in that sidebar: a build with navidrome/navidrome#6033 (see the note above)

## Install

1. Download `nd-better-lyrics.ndp` from a release or a successful GitHub Actions run.
2. Copy it to Navidrome's plugin folder, which defaults to `<DataFolder>/plugins`.
3. Enable plugins and add `nd-better-lyrics` after every local source in `LyricsPriority`:

```toml
LyricsPriority = ".ttml,.yaml,.yml,.elrc,.lrc,.srt,.txt,embedded,nd-better-lyrics"

[Plugins]
Enabled = true
```

The plugin ID comes from the package filename. Keep the file named `nd-better-lyrics.ndp`, or use its new filename in `LyricsPriority`.

After copying the package, validate it, ask a running Navidrome instance to discover it, and enable it:

```bash
navidrome plugin validate nd-better-lyrics.ndp
navidrome plugin rescan
navidrome plugin enable nd-better-lyrics
```

`plugin rescan` is needed when `Plugins.AutoReload` is disabled. Restarting Navidrome or setting `Plugins.AutoReload = true` are alternatives that also discover a newly copied package before it is enabled.

With the priority above, Navidrome stops at the first non-empty source. Sidecar and embedded lyrics therefore win; ND Better Lyrics is contacted only when every configured local source is empty.

### Upgrade from 0.3.x

Version 0.4.0 renames the package and plugin ID from `better-lyrics` to
`nd-better-lyrics`. Disable the old plugin, replace its package with
`nd-better-lyrics.ndp`, and replace `better-lyrics` with `nd-better-lyrics` in
`LyricsPriority`. Rescan and enable the new plugin as above. Reapply your source
order, enabled sources, and optional credentials in Settings → Plugins →
ND Better Lyrics. Navidrome stores settings and caches by plugin ID, so the new
ID starts with default settings and an empty cache.

### Upgrade from 0.4.0

Version 0.4.1 removes the Musixmatch source at Musixmatch's request. On first
load after upgrading, the plugin deletes the cached Musixmatch token, its cached
lyrics, cached LyricsPlus results (LyricsPlus used to fall back to Musixmatch),
and the winner cache, so the next lookup of each track re-queries the remaining
sources. A saved source list that still has a `musixmatch` row keeps
working because the plugin skips unknown ids. Navidrome rejects that row when
you next save the settings, so delete it first.

## Provider behavior

The plugin walks the enabled sources in configured order:

1. **Stage one: first paint.** On a cache miss it queries up to three enabled
   sources under a two-second total budget, giving each source at most 750 ms.
   It returns the first usable synchronized result. Plain text remains a fallback.
   Word-synced payloads from proprietary formats (QRC, KRC, YRC, LYL,
   WSY) are converted to enhanced LRC, including the final word's end marker.
2. **Stage two — background sweep.** The same lookup enqueues a task that walks
   *every* enabled source for the track and caches each result separately, so the
   per-song source picker (pending [navidrome/navidrome#6033](https://github.com/navidrome/navidrome/pull/6033)) can offer the cached alternatives. Responses return the
   winner first and label each alternative with its provider and format.
   Later lookups rank cached results by syllable, word, line, then plain timing.
   Background discoveries become visible when the host calls the plugin again.
   Automatic refresh depends on the host and UI behavior described below.

Selection and caching rules:

- Navidrome builds containing [#6465](https://github.com/navidrome/navidrome/pull/6465)
  also cache the plugin response per plugin and track for five minutes, including
  empty results and errors. Concurrent requests for the same track share one
  lookup with a one-minute timeout. While that host cache entry remains valid,
  Navidrome serves it without calling the plugin again. Background discoveries,
  source-setting or credential changes, and recovery from a temporary provider
  failure may therefore take up to five minutes to appear on a later request.
  Polling during that period returns the cached response. The plugin's own
  per-source caches and cooldowns still apply when Navidrome calls it.
  That host change merged on 2026-11-15 and is not in the latest released
  v0.64.2. Treat refresh after cache expiry as the baseline; prompt progressive
  discovery requires an explicit upstream invalidation or refresh contract.
- The winner cache keys score against a fingerprint of the enabled and ordered
  source list, so reordering sources in Settings takes effect the next time
  Navidrome calls the plugin.
  Winner TTLs follow timing quality: 24 h for syllable TTML, 1 h for word/line,
  5 min for plain text and complete misses. Per-source results cache for 24 h
  (synchronized) or 1 h (plain). Per-source clean misses expire after five
  minutes. Changing a source's credential keeps old positive lyrics readable
  while allowing the sweep to refresh that source with the new credential.
- The Better Lyrics endpoints keep their specificity tiers: full
  metadata → without album → title+artist only, with identical-URL dedup. Unison
  follows the same broadening after clean misses. Other sources use a single
  query scoped by a duration drift of at most two seconds. The `401` behavior is
  cache-only ("possibly exists, but not anonymously"), and an optional API key
  turns it into a clean miss.
- Independent source failures leave time for later sources; a global rate limit
  observed from one endpoint pauses its sibling endpoints for the rest of that
  lookup and for the cooldown duration. Retry-After values (capped at one hour)
  are honored, with conservative per-source default cooldowns. Provider cooldowns
  persist across plugin instances when the KV store is available; cache/cooldown
  storage failures fail open rather than making lyrics unavailable. Ordinary
  outages get a shared, source-specific retry backoff of five seconds, doubling
  to at most one minute. Exhausting a foreground allowance defers work to the
  sweep instead of marking the source as broken.
- Each background HTTP request can run for at most six seconds. Foreground
  requests use the smaller remaining source and lookup budgets. The blocking
  WASI host HTTP interface still runs requests sequentially.
- [Measured through a local Navidrome build](docs/research/latency-2026-10-18.md):
  five live cold lookups returned in 113–395 ms. With every provider stalled,
  the median response fell from 18.05 s to 2.04 s. These small samples measure
  endpoint latency, including failures, rather than browser paint time.
- Enhanced LRC preserves the last word's end. Intermediate silence and arbitrary
  voice metadata remain limited by that format and the host parser.
- Sources that ship disabled by default (`luna-beat`, `apple-lyrically`) query
  community mirrors with unclear distribution rights; enable them deliberately
  in Settings → Plugins → ND Better Lyrics.

Legacy is deprecated and is not queried.

## Source catalogue

| Source | Sync level | Default | Endpoint(s) | Notes |
|---|---|---|---|---|
| Better Lyrics | syllable TTML | on | `lyrics-api.boidu.dev` | cached-first public API; optional API key config |
| Unison | syllable/word/line/plain | on | `unison.boidu.dev` | crowdsourced; attribution required |
| BiniLyrics | word/line TTML | on | `lyrics-api.binimum.org` → `lrc.red` | Apple-shaped TTML; Navidrome parses it natively |
| AMLL | word/line TTML | on | `api.amll.dev` | community Apple-style TTML DB |
| LunaBeat | word TTML | **off** | `2755337087.github.io` | static community hub, unclear licensing |
| Apple Music (Lyrically) | word elrc/TTML | **off** | `itunes.apple.com`, `lyrics.paxsenix.org` | third-party mirror with unknown longevity |
| Better Lyrics Portato | word (QRC→elrc) | on | `lyrics-api.boidu.dev/qq/getLyrics` | QQ word sync via the same API |
| NetEase | word (YRC→elrc) / line | on | `music.163.com` | unauthenticated plain API; YRC only where available |
| Soda (Luna) | word (KRC→elrc) / line | on | `api.qishui.com`, `beta-luna.douyin.com` | unofficial endpoints, client UA required |
| PetitLyrics | word (WSY→elrc) / plain | on | `p0.petitlyrics.com` | uses the official app's client id; official client-binary decoding of the line-tier binary is not implemented |
| LRCLib | line / word (`lyricsfile`) / plain | on | `lrclib.net` | public API with an identifying User-Agent and real throttling |
| Better Lyrics Legato | line LRC | on | `lyrics-api.boidu.dev/kugou/getLyrics` | Kugou LRC via the same API |
| Lyricify | line / word LYL | on | `api.credits.fm`, `api.lyricify.app` | unofficial mobile endpoint; throwaway random username |
| LyricsPlus | word (KPoe→elrc) | on | `lyricsplus.binimum.org`, `lyricsplus.atomix.one` | community Apple-timing mirrors; multi-host rotation on mirror failure |
| YouTube Music | plain | **off** | `music.youtube.com` | the official YTM lyrics tab via the public innertube client; 3-hop lookup, duration-gated video match |
| Genius | plain | **off** | `genius.com` | keyless Genius search + plain-text lyric scrape; strict-ish metadata gate |

Not carried over from the Better Lyrics extension's list:

- **PaxSenix backends other than Apple Music** — the `lyrics.paxsenix.org`
  backends beyond `/apple-music/lyrics` were shut down ("no longer available";
  verified live). Spotify-word lyrics have no other keyless source.
- **lrc.red as a separate source** — BiniLyrics and Unison already cover the
  same Apple-graded TTML store (BiniLyrics fetches `lrc.red/s/{isrc}.ttml`
  directly).
- **YouTube captions** — closed-caption tracks for music videos are spoken-ASR,
  not lyric-timed; the extension matches them in-browser against the playing
  video, which has no Navidrome-side equivalent at the same fidelity.

Sources with word timing convert their proprietary formats into enhanced LRC so
Navidrome's own parser keeps the word cues. `api.betterlyrics.org` and
`unison.betterlyrics.org` are replacements for the older boidu.dev domains; this
release keeps the existing hosts because both currently serve identical traffic.

## Settings (navidrome plugin config)

Settings → Plugins → ND Better Lyrics exposes:

- **Lyric sources** — an ordered list with enable checkboxes; the top row wins
  timing-quality ties. The shipped default order is the catalogue above.
- **Better Lyrics API key** — optional; sent only as the `X-API-Key` header to
  `lyrics-api.boidu.dev`, never logged or included in URLs.

The plugin falls back to its built-in defaults when a config save drops or
empties the source list; a listed-but-disabled source stays disabled. Saving the
list with every row disabled is a deliberate opt-out: the plugin then performs no
network fetches at all (and nd-better-lyrics disappears from `LyricsPriority`
behavior until it is re-enabled).

See the Better Lyrics documentation for the current [authentication](https://lyrics-api-docs.boidu.dev/docs/authentication), [rate limits](https://lyrics-api-docs.boidu.dev/docs/rate-limiting), and [response format](https://lyrics-api-docs.boidu.dev/docs/response-format).

## Privacy and attribution

Using this plugin sends song metadata (title, artist, album, duration), the Navidrome server's IP address, and a fixed plugin user agent (`NavidromeBetterLyricsPlugin/<version> (+https://github.com/ranokay/navidrome-better-lyrics-plugin)`) to **every enabled source's host**; it never forwards a client browser's user agent beyond what each provider requires:

- `lyrics-api.boidu.dev` (Better Lyrics TTML/QQ/Kugou): title, artist, album, and duration on the first attempt; broader retries can omit album and then duration. Request logs can be retained for up to seven days and song metadata may be forwarded to third-party APIs such as LRCLib. Review its [privacy policy](https://github.com/better-lyrics/better-lyrics/blob/master/PRIVACY.md).
- `unison.boidu.dev`: title, artist, album, and duration on the first attempt; broader retries can omit album and then duration.
- `lrclib.net`: title, artist, album, and duration; the API asks clients to identify themselves via the User-Agent.
- `lyrics-api.binimum.org` and `lrc.red`: title, artist, album, and duration, then the matched record's `lyricsUrl`.
- `lyricsplus.binimum.org` and `lyricsplus.atomix.one` (LyricsPlus): title, artist, album, and duration, with mirror rotation.
- `api.amll.dev`: title, artist, and album.
- `music.163.com` (NetEase): title, artist, duration, and the matched song's id in the query string. Unofficial endpoints may change without notice.
- `api.qishui.com` and `beta-luna.douyin.com` (Soda/Luna): title, artist, duration, and generated device identifiers that imitate the Android client; large search payloads are part of that API's contract.
- `p0.petitlyrics.com`: title, artist, album, and the official app's `clientAppId`.
- `api.credits.fm` and `api.lyricify.app`: title and artist, then a base64 ISRC and a generated throwaway username.
- `2755337087.github.io` (LunaBeat, default off) and `itunes.apple.com` + `lyrics.paxsenix.org` (Apple via Lyrically, default off): community mirrors whose operators set their own terms.
- `music.youtube.com` (YouTube Music, default off): title and artist in the search request, then the matched video id in the follow-up calls.
- `genius.com` (Genius, default off): title and artist to the search endpoint, then the matched song's page.

Because several providers are **unofficial endpoints scraped or reverse-engineered by other open-source apps, they can break or change without notice**. Every source is off-switchable in Settings, and local lyrics always win `LyricsPriority` before fetchers run.

Lyrics are supplied by the enabled providers and their upstream contributors.
The repository includes minimized provider responses and synthetic lyrics for
tests, with [fixture provenance](internal/source/testdata/fixtures.md).
Test fixtures are excluded from the downloadable plugin package. Users are
responsible for complying with the terms and copyright rules that apply in
their jurisdiction.

Thanks to [Better Lyrics](https://betterlyrics.org/) for making its synchronized-lyrics API available.

Lyrics from Unison ([unison.boidu.dev](https://unison.boidu.dev/)). Unison requires this attribution.

## Development

This repository uses [mise](https://mise.jdx.dev/) for tool versions and tasks:

```bash
mise install
mise run test
mise run check
```

`mise run check` verifies formatting, workflows, Go vet results and module integrity,
runs the race-enabled Go tests, builds the WebAssembly module with the standard Go
WASI toolchain, packages `dist/nd-better-lyrics.ndp`, and checks the archive.
The tests validate the manifest and default configuration against the versioned
host schema in `plugin/testdata`, without network access.

LRCLib lyricsfile parsing uses YAML v4 and requires exactly one YAML document.
Unknown provider fields remain supported. Go toolchain and dependency updates
must preserve the standard WASI build and the optional provenance contract.
See [dependency holds, advisory dispositions, and upgrade checks](docs/current-state.md#dependencies-and-upgrades).

### Project layout

```
plugin/              package main: manifest embed, registration, host seams, contract tests
internal/provider/   orchestration: source walk, sweep, winner cache, config, transport
internal/source/     upstream adapters, one file per catalog source; fixtures in testdata/
internal/lyric/      lyric domain: candidates, timing grades, identity matching, word-sync parsers
dist/                local build output (plugin.wasm, nd-better-lyrics.ndp); not committed
docs/                current-state contract and archived research/design history
scripts/             bounded public probes and disposable WASI host qualification
```

The Go PDK replacement retains the optional provenance contract from #6033.
Upstream's current PDK lacks those fields. Keep the explicit provenance revision
until an upstream PDK provides the same entry and response metadata and passes
the compatibility checks in [current state](docs/current-state.md#upstream-dependencies).

## Releases

Every downloadable artifact is named `nd-better-lyrics.ndp`; the version lives inside its `plugin/manifest.json`. To cut a release, set `version` in `plugin/manifest.json` and push a matching tag:

```bash
version="v$(jq -r .version plugin/manifest.json)"
git tag "$version"
git push origin "$version"
```

The release workflow verifies that the tag equals `v` plus the manifest version, reruns `mise run check`, and publishes a GitHub release containing `nd-better-lyrics.ndp` with automatically generated notes.

A manual run of the Release workflow uploads the package as a workflow artifact.
Running it on a branch builds a development package without publishing a release.

## License

The original source in this repository remains available under the [MIT License](LICENSE).

Packaged `.ndp` artifacts statically include the [pinned Navidrome Go PDK](https://github.com/ranokay/navidrome/tree/a730db2a7cd9bd5eae2a0fcb9472b871d606e9c1/plugins/pdk/go), which is licensed under GPL-3.0. Distribution of the combined artifact must comply with the PDK's GPL-3.0 terms. The Better Lyrics API and Unison are separate network services; no server, database, or extension source is copied into this plugin.
