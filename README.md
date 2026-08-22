# Navidrome Better Lyrics plugin

A Navidrome lyrics-provider plugin that uses the public [Better Lyrics API](https://lyrics-api-docs.boidu.dev/) when a track has no local lyrics.

The plugin does not inject or replace Navidrome's interface. It returns the API's TTML document unchanged through Navidrome's Lyrics capability, so compatible clients—including the sidebar proposed in [navidrome/navidrome#5733](https://github.com/navidrome/navidrome/pull/5733)—can render the same syllable timing, translations, and pronunciation data as local TTML files.

## Requirements

- Navidrome v0.63.2 or newer
- Navidrome plugins enabled
- A Navidrome client that displays synchronized lyrics; PR #5733 is required specifically for its responsive sidebar UI until that work ships upstream

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

After installation, validate and enable the plugin:

```bash
navidrome plugin validate better-lyrics.ndp
navidrome plugin enable better-lyrics
```

With the priority above, Navidrome stops at the first non-empty source. Sidecar and embedded lyrics therefore win; Better Lyrics is contacted only when every configured local source is empty.

## API behavior

The plugin sends the track title, artist, album, and duration to `https://lyrics-api.boidu.dev/getLyrics`. It preserves successful TTML exactly as received.

The public API is cache-first:

- Cached songs do not require an API key.
- Uncached songs can return `401 Unauthorized`; the plugin treats that cached-only miss as no lyrics because Better Lyrics is not currently issuing new API keys.
- `404 Not Found` is treated as a normal no-match result.
- Invalid-request, rate-limit, network, malformed-response, and server failures are logged by Navidrome and do not replace higher-priority lyrics.

See the Better Lyrics documentation for the current [authentication](https://lyrics-api-docs.boidu.dev/docs/authentication), [rate limits](https://lyrics-api-docs.boidu.dev/docs/rate-limiting), and [response format](https://lyrics-api-docs.boidu.dev/docs/response-format).

## Privacy and attribution

Using this plugin sends song metadata and the Navidrome server's IP address and user agent to Better Lyrics and its infrastructure. Better Lyrics says request logs can be retained for up to seven days; review its [privacy policy](https://github.com/better-lyrics/better-lyrics/blob/master/PRIVACY.md) before enabling the plugin.

Lyrics are supplied by Better Lyrics and its upstream providers. This repository does not bundle lyrics. Users are responsible for complying with the terms and copyright rules that apply in their jurisdiction.

Thanks to [Better Lyrics](https://betterlyrics.org/) for making its synchronized-lyrics API available.

## Development

This repository uses [mise](https://mise.jdx.dev/) for tool versions and tasks:

```bash
mise install
mise run test
mise run check
```

`mise run check` verifies formatting, runs the race-enabled Go tests, builds the WebAssembly module, packages `better-lyrics.ndp`, and checks the archive.

The implementation targets the Navidrome v0.63.2 Go PDK so the plugin is built against a released Lyrics-capability contract rather than a moving branch.

## License

The original source in this repository remains available under the [MIT License](LICENSE).

Packaged `.ndp` artifacts statically include the [Navidrome Go PDK](https://github.com/navidrome/navidrome/tree/v0.63.2/plugins/pdk/go), which is licensed under GPL-3.0. Distribution of the combined artifact must comply with the PDK's GPL-3.0 terms. The Better Lyrics API is a separate network service; no Better Lyrics server or extension source is copied into this plugin.
