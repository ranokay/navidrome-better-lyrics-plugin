# Navidrome Better Lyrics plugin

A Navidrome lyrics-provider plugin that uses the public [Better Lyrics API](https://lyrics-api-docs.boidu.dev/) and [Unison community database](https://unison.boidu.dev/) when a track has no local lyrics.

The plugin does not inject or replace Navidrome's interface. It returns TTML, LRC, or plain lyrics unchanged through Navidrome's Lyrics capability, so compatible clients can render the same rich timing and metadata present in provider TTML.

> [!NOTE]
> **Pending upstream — [navidrome/navidrome#5733](https://github.com/navidrome/navidrome/pull/5733) (responsive lyrics sidebar).**
> The sidebar UI that renders these lyrics lives in that PR and is not in a released Navidrome yet.
> *When it ships:* update this paragraph and the sidebar mention under Requirements; lyrics work on stock Navidrome either way.

> [!NOTE]
> **Pending upstream — [ranokay/navidrome#13](https://github.com/ranokay/navidrome/pull/13) (lyrics source provenance).**
> This plugin already reports the selected provider and format through the optional provenance contract from that PR, but current Navidrome hosts ignore the extra metadata.
> *When it ships:* remove the temporary PDK `replace` in `go.mod` (see Development), point the PDK link under License back at upstream, and simplify the Unison attribution below.

## Requirements

- Navidrome v0.63.2 or newer
- Navidrome plugins enabled
- A Navidrome client that displays synchronized lyrics
- For the responsive sidebar UI specifically: a build with navidrome/navidrome#5733 (see the note above)
- For source details in that sidebar: a build with ranokay/navidrome#13 (see the note above)

## Install

1. Download `better-lyrics.ndp` from a release or a successful GitHub Actions run.
2. Copy it to Navidrome's plugin folder, which defaults to `<DataFolder>/plugins`.
3. Enable plugins and add `better-lyrics` after every local source in `LyricsPriority`:

```toml
LyricsPriority = ".ttml,.yaml,.yml,.elrc,.lrc,.srt,.txt,embedded,better-lyrics"

[Plugins]
Enabled = true
```

The plugin ID comes from the package filename. Keep the file named `better-lyrics.ndp`, or use its new filename in `LyricsPriority`.

After copying the package, validate it, ask a running Navidrome instance to discover it, and enable it:

```bash
navidrome plugin validate better-lyrics.ndp
navidrome plugin rescan
navidrome plugin enable better-lyrics
```

`plugin rescan` is needed when `Plugins.AutoReload` is disabled. Restarting Navidrome or setting `Plugins.AutoReload = true` are alternatives that also discover a newly copied package before it is enabled.

With the priority above, Navidrome stops at the first non-empty source. Sidecar and embedded lyrics therefore win; Better Lyrics is contacted only when every configured local source is empty.

## Provider behavior

The plugin ranks usable results by synchronization quality:

1. Syllable-timed TTML.
2. Word-timed TTML.
3. Line-timed TTML or timestamped LRC.
4. Unsynchronized text.

The plugin checks Better Lyrics TTML first using title, artist, album, and duration. After a `401`, `404`, an empty TTML payload, or a cached-tier `429`, it broadens the lookup without immediately discarding useful disambiguation: first title, artist, and duration without album, then title and artist only. A valid syllable-timed result returns immediately. Lower-quality TTML is compared with Unison before selection. Unison follows the same specificity-first idea on clean misses: title, artist, album, and duration; then title, artist, and duration; then title and artist. Kugou is queried only when no service has produced line-timed or better lyrics. Better Lyrics wins quality ties, followed by Unison and Kugou.

Provider-reported TTML must parse as complete XML before it can win the quality comparison, so malformed high-quality metadata cannot suppress a usable fallback. LRC is considered line-synchronized only when it contains timestamped lyric lines. Successful TTML, LRC, and plain text are otherwise preserved exactly as received so Navidrome can select the matching parser.

Legacy is deprecated and is not queried.

The public API is cache-first:

- Cached songs do not require an API key.
- Uncached Better Lyrics requests can return `401 Unauthorized`. Because this means that the exact query is not available to an unauthenticated cache-only lookup rather than proving that lyrics do not exist, a fallback reached through `401` is treated as uncertain and cached for one hour instead of a full day.
- `404 Not Found` is a normal no-match result.
- Independent provider failures do not block a later provider. If every provider misses, operational failures are returned to Navidrome for logging.
- Clean successful results are cached in Navidrome's plugin store for 24 hours. Successful fallbacks reached through cache-only `401` responses use a one-hour TTL. Successful fallbacks reached after transport, server, parsing, or rate-limit failures and complete misses use a five-minute TTL, so preferred providers can be retried promptly. Operational failure responses themselves are never cached.
- The persistent store is capped at 32 MB. The plugin does not cache an individual lyrics response larger than 1 MB and reserves 1 MB of store capacity for cooldown state when the host exposes storage usage, preventing ordinary lyrics entries from consuming all space required for rate-limit protection.
- A Better Lyrics cached-tier `429 Too Many Requests` cools down only the failed query, so another song can still hit the public cache. An explicit `X-RateLimit-Type: exceeded` response starts a shared cooldown for all Better Lyrics endpoints, including Kugou. Better Lyrics honors `Retry-After` as either seconds or an HTTP date, falling back to 30 seconds when the header is absent or invalid.
- Unison has a separate provider-wide cooldown. Its current public read limiter may return `429` without `Retry-After`; in that case the plugin waits 60 seconds before trying Unison again while Better Lyrics and Kugou remain available.
- Provider cooldowns are persisted across plugin instances when the KV store is available. `Retry-After` is capped at one hour so a malformed or extreme upstream value cannot disable a provider indefinitely. Cache/cooldown storage failures fail open rather than making lyrics unavailable, while a rate limit observed during the current lookup is still respected for the rest of that lookup.
- Each HTTP request can run for at most six seconds, and an entire cold lookup has a 15-second budget. Later fallbacks receive only the remaining budget. A valid syllable-timed Better Lyrics cache hit still completes after one request; broader metadata tiers are attempted only when the preceding response makes them useful.

> [!NOTE]
> **Pending upstream — [ranokay/navidrome#15](https://github.com/ranokay/navidrome/pull/15) (host-side request coalescing).**
> Navidrome currently creates separate plugin calls for concurrent requests of the same track; PR #15 coalesces them in the host. Until that change is present in the running Navidrome build, identical simultaneous cold lookups can still reach this plugin independently.
> *When it ships:* delete this block.

See the Better Lyrics documentation for the current [authentication](https://lyrics-api-docs.boidu.dev/docs/authentication), [rate limits](https://lyrics-api-docs.boidu.dev/docs/rate-limiting), and [response format](https://lyrics-api-docs.boidu.dev/docs/response-format).

## Privacy and attribution

Using this plugin sends song metadata, the Navidrome server's IP address, and a fixed plugin user agent (`NavidromeBetterLyricsPlugin/<version> (+https://github.com/ranokay/navidrome-better-lyrics-plugin)`) to Better Lyrics and Unison; it does not send the browser's user agent. Better Lyrics receives title, artist, album, and duration on the first attempt; broader retries can omit album and then duration. Unison likewise receives title, artist, album, and duration on its first attempt and can retry without album and then duration after clean misses. The Kugou fallback endpoint (`https://lyrics-api.boidu.dev/kugou/getLyrics`) receives title, artist, the Navidrome server's IP address, and the same fixed plugin user agent. Better Lyrics says request logs can be retained for up to seven days and song metadata may be forwarded to third-party APIs such as LRCLib. Review its [privacy policy](https://github.com/better-lyrics/better-lyrics/blob/master/PRIVACY.md) before enabling the plugin.

Lyrics are supplied by Better Lyrics, Unison, and their upstream contributors. This repository does not bundle lyrics. Users are responsible for complying with the terms and copyright rules that apply in their jurisdiction.

Thanks to [Better Lyrics](https://betterlyrics.org/) for making its synchronized-lyrics API available.

Lyrics from Unison ([unison.boidu.dev](https://unison.boidu.dev/)). Unison requires this attribution.

> [!NOTE]
> **Pending upstream — ranokay/navidrome#13.** Once the provenance contract ships, hosts that display the source will identify Unison (or Better Lyrics/Kugou) beside the lyrics automatically; until then this static attribution is what users see.

## Development

This repository uses [mise](https://mise.jdx.dev/) for tool versions and tasks:

```bash
mise install
mise run test
mise run check
```

`mise run check` verifies formatting, runs the race-enabled Go tests, builds the WebAssembly module with the standard Go WASI toolchain, packages `dist/better-lyrics.ndp`, and checks the archive.

### Project layout

```
plugin/          Go sources, tests, and manifest.json for the plugin package
dist/            local build output (plugin.wasm, better-lyrics.ndp); not committed
docs/research/   feasibility and design research
```

> [!NOTE]
> **Pending upstream — ranokay/navidrome#13.** The implementation temporarily replaces the released Go PDK with the additive provenance contract from that PR. Older Navidrome hosts ignore the optional source metadata and continue receiving the lyrics; compatible hosts display it.
> *When it ships:* delete the `replace` directive in `go.mod`, run `go mod tidy`, and remove this block.

## Releases

Every downloadable artifact is named `better-lyrics.ndp`; the version lives inside its `plugin/manifest.json`. To cut a release, set `version` in `plugin/manifest.json` and push a matching tag:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The release workflow verifies that the tag equals `v` plus the manifest version, reruns `mise run check`, and publishes a GitHub release containing `better-lyrics.ndp` with automatically generated notes.

## License

The original source in this repository remains available under the [MIT License](LICENSE).

Packaged `.ndp` artifacts statically include the [Navidrome Go PDK](https://github.com/ranokay/navidrome/tree/lyrics-source-provenance/plugins/pdk/go), which is licensed under GPL-3.0. Distribution of the combined artifact must comply with the PDK's GPL-3.0 terms. The Better Lyrics API and Unison are separate network services; no server, database, or extension source is copied into this plugin.

> [!NOTE]
> **Pending upstream — ranokay/navidrome#13.** The PDK link above points at the `lyrics-source-provenance` branch because the packaged PDK currently comes from that PR. When #13 merges upstream, link the PDK from `navidrome/navidrome` instead and drop this block.
