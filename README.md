# Navidrome Better Lyrics plugin

A Navidrome lyrics-provider plugin that uses the public [Better Lyrics API](https://lyrics-api-docs.boidu.dev/) and [Unison community database](https://unison.boidu.dev/) when a track has no local lyrics.

The plugin does not inject or replace Navidrome's interface. It returns TTML, LRC, or plain lyrics unchanged through Navidrome's Lyrics capability. Compatible clients, including the sidebar proposed in [navidrome/navidrome#5733](https://github.com/navidrome/navidrome/pull/5733), can render the same rich timing and metadata present in provider TTML. On builds with [ranokay/navidrome#13](https://github.com/ranokay/navidrome/pull/13), it also reports the selected provider and format for the sidebar's source popover.

## Requirements

- Navidrome v0.63.2 or newer
- Navidrome plugins enabled
- A Navidrome client that displays synchronized lyrics; PR #5733 is required specifically for its responsive sidebar UI until that work ships upstream
- PR #13 is required specifically for source details in that sidebar until the provenance contract ships upstream

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
3. Line-timed TTML or LRC.
4. Unsynchronized text.

The plugin checks Better Lyrics TTML first using title, artist, album, and duration. It retries with only title and artist after `401`, `404`, or a cached-tier `429`, because the broader query may already be cached. A syllable-timed result returns immediately. Lower-quality TTML is compared with Unison before selection, and Kugou is queried only when neither service produced line-timed or better lyrics. Better Lyrics wins ties, followed by Unison and Kugou.

Legacy is deprecated and is not queried. Successful TTML, LRC, and plain text are preserved exactly as received so Navidrome can select the matching parser.

The public API is cache-first:

- Cached songs do not require an API key.
- Uncached Better Lyrics requests can return `401 Unauthorized`; the plugin treats that cached-only miss as a normal provider miss because Better Lyrics is not currently issuing new API keys.
- `404 Not Found` is a normal no-match result.
- Independent provider failures do not block a later provider. If every provider misses, operational failures are returned to Navidrome for logging.
- Successful results reached after clean provider responses are cached in Navidrome's plugin store for 24 hours. Successful lower-priority fallbacks reached after an operational failure and complete misses are cached for five minutes, so preferred providers can be retried promptly. The store is capped at 8 MB; transport, server, parsing, and rate-limit failure responses are never cached.
- A cached-tier `429 Too Many Requests` cools down only the failed query, so an uncached Kugou request cannot block another song's cached TTML. An explicit `X-RateLimit-Type: exceeded` response starts a shared cooldown for all Better Lyrics endpoints. The plugin honors `Retry-After` as either seconds or an HTTP date, falling back to 30 seconds when the header is absent or invalid. Unison remains available during either cooldown.
- Each HTTP attempt has a six-second timeout. On a cold cache, a syllable-timed Better Lyrics result makes one request; comparing lower-quality candidates or completing a miss makes at most four sequential requests.

See the Better Lyrics documentation for the current [authentication](https://lyrics-api-docs.boidu.dev/docs/authentication), [rate limits](https://lyrics-api-docs.boidu.dev/docs/rate-limiting), and [response format](https://lyrics-api-docs.boidu.dev/docs/response-format).

## Privacy and attribution

Using this plugin sends song metadata, the Navidrome server's IP address, and a fixed plugin user agent (`NavidromeBetterLyricsPlugin/<version> (+https://github.com/ranokay/navidrome-better-lyrics-plugin)`) to Better Lyrics and Unison; it does not send the browser's user agent. Better Lyrics receives title, artist, album, and duration on the first attempt, while Unison receives title, artist, and duration. The Kugou fallback endpoint (`https://lyrics-api.boidu.dev/kugou/getLyrics`) receives title, artist, the Navidrome server's IP address, and the same fixed plugin user agent. Better Lyrics says request logs can be retained for up to seven days and song metadata may be forwarded to third-party APIs such as LRCLib. Review its [privacy policy](https://github.com/better-lyrics/better-lyrics/blob/master/PRIVACY.md) before enabling the plugin.

Lyrics are supplied by Better Lyrics, Unison, and their upstream contributors. This repository does not bundle lyrics. Users are responsible for complying with the terms and copyright rules that apply in their jurisdiction.

Thanks to [Better Lyrics](https://betterlyrics.org/) for making its synchronized-lyrics API available.

Lyrics from Unison ([unison.boidu.dev](https://unison.boidu.dev/)). Unison requires this attribution. Builds with PR #13 also identify Unison as the selected provider beside the lyrics.

## Development

This repository uses [mise](https://mise.jdx.dev/) for tool versions and tasks:

```bash
mise install
mise run test
mise run check
```

`mise run check` verifies formatting, runs the race-enabled Go tests, builds the WebAssembly module, packages `better-lyrics.ndp`, and checks the archive.

The implementation temporarily replaces the released Go PDK with the additive provenance contract from PR #13. Older Navidrome hosts ignore the optional source metadata and continue receiving the lyrics; compatible hosts display it.

## License

The original source in this repository remains available under the [MIT License](LICENSE).

Packaged `.ndp` artifacts statically include the [Navidrome Go PDK](https://github.com/ranokay/navidrome/tree/lyrics-source-provenance/plugins/pdk/go), which is licensed under GPL-3.0. Distribution of the combined artifact must comply with the PDK's GPL-3.0 terms. The Better Lyrics API and Unison are separate network services; no server, database, or extension source is copied into this plugin.
