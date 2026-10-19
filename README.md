<p align="center"><img src="docs/assets/icon.png" alt="ND Better Lyrics plugin icon" width="180"></p>

# Navidrome Better Lyrics plugin

A Navidrome lyrics-provider plugin that fills the gap when a track has no local lyrics. It resolves tracks through seventeen configurable sources, including the Better Lyrics API (syllable TTML / word-synced QQ "Portato" / line-synced Kugou "Legato"), the Unison community database, BiniLyrics, AMLL, LRCLib, Musixmatch, NetEase, Soda (Luna), PetitLyrics, Lyricify, LyricsPlus, YouTube Music, Genius, and two community mirrors (LunaBeat and an Apple Music mirror via Lyrically).

The plugin does not inject or replace Navidrome's interface. It returns TTML, enhanced LRC, or plain lyrics through Navidrome's Lyrics capability, converting proprietary word-timed formats into enhanced LRC so compatible clients can render word-level highlighting.

> [!NOTE]
> **Pending upstream — [navidrome/navidrome#5733](https://github.com/navidrome/navidrome/pull/5733) (responsive lyrics sidebar).**
> The sidebar UI that renders these lyrics lives in that PR and is not in a released Navidrome yet.
> *When it ships:* update this paragraph and the sidebar mention under Requirements; lyrics work on stock Navidrome either way.

> [!NOTE]
> **Pending upstream — [navidrome/navidrome#6033](https://github.com/navidrome/navidrome/pull/6033) (lyrics source provenance).**
> This plugin already reports the selected provider and format through the optional provenance contract from that PR, but current Navidrome hosts ignore the extra metadata.
> *When it ships:* remove the temporary PDK `replace` in `go.mod` (see Development), point the PDK link under License back at upstream, and simplify the Unison attribution below.

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

## Provider behavior

The plugin walks the enabled sources in configured order:

1. **Stage one: first paint.** On a cache miss it queries up to three enabled
   sources under a two-second total budget, giving each source at most 750 ms.
   It returns the first usable synchronized result. Plain text remains a fallback.
   Word-synced payloads from proprietary formats (QRC, KRC, YRC, LYL, richsync,
   WSY) are converted to enhanced LRC, including the final word's end marker.
2. **Stage two — background sweep.** The same lookup enqueues a task that walks
   *every* enabled source for the track and caches each result separately, so the
   per-song source picker (pending [navidrome/navidrome#6033](https://github.com/navidrome/navidrome/pull/6033)) can offer the cached alternatives. Responses return the
   winner first and label each alternative with its provider and format.
   Later lookups rank cached results by syllable, word, line, then plain timing.
   Showing background discoveries automatically requires the matching Navidrome
   UI changes; stock hosts do not poll for them.

Selection and caching rules:

- The winner cache keys score against a fingerprint of the enabled and ordered
  source list, so reordering sources in Settings takes effect on the next lookup.
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

> [!NOTE]
> **Pending upstream — [navidrome/navidrome#6032](https://github.com/navidrome/navidrome/pull/6032) (host-side request coalescing).**
> Navidrome currently creates separate plugin calls for concurrent requests of the same track; PR #6032 coalesces them in the host. Until that change is present in the running Navidrome build, identical simultaneous cold lookups can still reach this plugin independently.
> *When it ships:* delete this block.

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
| Musixmatch | word (richsync→elrc) / line / plain | on | `apic.musixmatch.com` | the anonymous self-minted token currently answers without subtitle macros (intermittently captcha-gated); the optional user-token config unlocks the word and line tiers |
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

- **PaxSenix: Spotify / PaxSenix: Musixmatch** — the `lyrics.paxsenix.org`
  backends beyond `/apple-music/lyrics` were shut down ("no longer available";
  verified live). Spotify-word lyrics have no other keyless source, and native
  Musixmatch (this plugin's own source) is richer than the mirror.
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
- **Musixmatch user token** — optional; sent only to `apic.musixmatch.com`,
  replacing the anonymous self-minted token when a captcha challenge appears,
  and the only credential that currently unlocks the line and word tiers.

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
- `apic.musixmatch.com`: title, artist, duration, plus the anonymous (or admin-configured) user token in the query string as the documented client does.
- `music.163.com` (NetEase): title, artist, duration, and the matched song's id in the query string. Unofficial endpoints may change without notice.
- `api.qishui.com` and `beta-luna.douyin.com` (Soda/Luna): title, artist, duration, and generated device identifiers that imitate the Android client; large search payloads are part of that API's contract.
- `p0.petitlyrics.com`: title, artist, album, and the official app's `clientAppId`.
- `api.credits.fm` and `api.lyricify.app`: title and artist, then a base64 ISRC and a generated throwaway username.
- `2755337087.github.io` (LunaBeat, default off) and `itunes.apple.com` + `lyrics.paxsenix.org` (Apple via Lyrically, default off): community mirrors whose operators set their own terms.
- `music.youtube.com` (YouTube Music, default off): title and artist in the search request, then the matched video id in the follow-up calls.
- `genius.com` (Genius, default off): title and artist to the search endpoint, then the matched song's page.

Because several providers are **unofficial endpoints scraped or reverse-engineered by other open-source apps, they can break or change without notice**. Every source is off-switchable in Settings, and local lyrics always win `LyricsPriority` before fetchers run.

Lyrics are supplied by the enabled providers and their upstream contributors. This repository does not bundle lyrics. Users are responsible for complying with the terms and copyright rules that apply in their jurisdiction.

Thanks to [Better Lyrics](https://betterlyrics.org/) for making its synchronized-lyrics API available.

Lyrics from Unison ([unison.boidu.dev](https://unison.boidu.dev/)). Unison requires this attribution.

> [!NOTE]
> **Pending upstream — navidrome/navidrome#6033.** Once the provenance contract ships, hosts that display the source will identify Unison (or Better Lyrics/Kugou) beside the lyrics automatically; until then this static attribution is what users see.

## Development

This repository uses [mise](https://mise.jdx.dev/) for tool versions and tasks:

```bash
mise install
mise run test
mise run check
```

`mise run check` verifies formatting, runs the race-enabled Go tests, builds the WebAssembly module with the standard Go WASI toolchain, packages `dist/nd-better-lyrics.ndp`, and checks the archive.

### Project layout

```
plugin/              package main: manifest embed, registration, host seams, contract tests
internal/provider/   orchestration: source walk, sweep, winner cache, config, transport
internal/source/     upstream adapters, one file per catalog source; fixtures in testdata/
internal/lyric/      lyric domain: candidates, timing grades, identity matching, word-sync parsers
dist/                local build output (plugin.wasm, nd-better-lyrics.ndp); not committed
docs/                feasibility research, design specs, and implementation plans
```

> [!NOTE]
> **Pending upstream — navidrome/navidrome#6033.** The implementation temporarily replaces the released Go PDK with the additive provenance contract from that PR. Older Navidrome hosts ignore the optional source metadata and continue receiving the lyrics; compatible hosts display it.
> *When it ships:* delete the `replace` directive in `go.mod`, run `go mod tidy`, and remove this block.

## Releases

Every downloadable artifact is named `nd-better-lyrics.ndp`; the version lives inside its `plugin/manifest.json`. To cut a release, set `version` in `plugin/manifest.json` and push a matching tag:

```bash
git tag v0.4.0
git push origin v0.4.0
```

The release workflow verifies that the tag equals `v` plus the manifest version, reruns `mise run check`, and publishes a GitHub release containing `nd-better-lyrics.ndp` with automatically generated notes.

## License

The original source in this repository remains available under the [MIT License](LICENSE).

Packaged `.ndp` artifacts statically include the [Navidrome Go PDK](https://github.com/ranokay/navidrome/tree/lyrics-source-provenance/plugins/pdk/go), which is licensed under GPL-3.0. Distribution of the combined artifact must comply with the PDK's GPL-3.0 terms. The Better Lyrics API and Unison are separate network services; no server, database, or extension source is copied into this plugin.

> [!NOTE]
> **Pending upstream — navidrome/navidrome#6033.** The PDK link above points at the `lyrics-source-provenance` branch because the packaged PDK currently comes from that PR. When #6033 merges upstream, link the PDK from `navidrome/navidrome` instead and drop this block.
