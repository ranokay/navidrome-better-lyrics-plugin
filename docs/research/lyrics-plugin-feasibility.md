# Better Lyrics fallback plugin feasibility

Research date: 2026-08-23

## Verdict

This is feasible as a **Navidrome backend lyrics provider**, with an important boundary: a `.ndp` plugin cannot add, replace, or inject web UI. Navidrome plugin packages contain a manifest and a WebAssembly module, and the public lyrics capability returns lyric text to Navidrome's existing lyrics pipeline; it exposes no JavaScript, CSS, React, or sidebar registration hook ([Navidrome package loader](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/package.go#L12-L94), [lyrics capability](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/capabilities/lyrics.go#L3-L26)).

The plugin can nevertheless feed Better Lyrics TTML into the standard `getLyricsBySongId` path. PR [navidrome/navidrome#5733](https://github.com/navidrome/navidrome/pull/5733) uses that path, so its sidebar can render plugin-provided lyrics without Better Lyrics-specific UI code. Users must run a Navidrome frontend containing #5733; installing this plugin alone will not create the sidebar.

There is a second, material limitation: Better Lyrics does not currently issue API keys, and its documented production policy only permits unauthenticated access to already-cached lyrics. An uncached song can return `401`. The proposed plugin is therefore a best-effort cached fallback today, not a dependable universal lyrics source ([authentication documentation](https://lyrics-api-docs.boidu.dev/docs/authentication/)).

## What Navidrome plugins can do

### Backend capability, not UI injection

A Navidrome plugin declares capabilities in `manifest.json`, compiles to `plugin.wasm`, and is distributed as an `.ndp` archive. The official Apple Music plugin demonstrates the intended pattern: register a PDK capability in Go, use host functions such as HTTP and storage, and package the manifest with the compiled module ([entry point](https://github.com/navidrome/apple-music-plugin/blob/60a6100f7aaeca676de71cc7b343e49ef42e2fca/main.go#L1-L69), [HTTP helper](https://github.com/navidrome/apple-music-plugin/blob/60a6100f7aaeca676de71cc7b343e49ef42e2fca/helpers.go#L47-L109), [manifest permissions](https://github.com/navidrome/apple-music-plugin/blob/60a6100f7aaeca676de71cc7b343e49ef42e2fca/manifest.json#L132-L145)).

The lyrics capability is server-side. Navidrome calls a plugin, parses the returned text into its lyrics model, and exposes the result through its normal API ([adapter](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/lyrics_adapter.go#L17-L78)). Neither the package format nor the lyrics contract has an asset or UI extension point.

### Exact plugin contract

The current contract is `v1-draft` JSON passed through an Extism export, not a WebAssembly Component Model `.wit` interface. The generated Go PDK registers an exported function named `nd_lyrics_get_lyrics` ([capability definition](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/capabilities/lyrics.yaml#L1-L140), [generated PDK](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/pdk/go/lyrics/lyrics.go#L18-L117)).

Conceptually, the request is:

```json
{
  "track": {
    "id": "...",
    "title": "...",
    "album": "...",
    "artist": "...",
    "albumArtist": "...",
    "artists": [{ "id": "...", "name": "..." }],
    "albumArtists": [{ "id": "...", "name": "..." }],
    "duration": 213.4,
    "trackNumber": 1,
    "discNumber": 1,
    "mbzRecordingId": "optional",
    "mbzAlbumId": "optional",
    "mbzReleaseGroupId": "optional",
    "mbzReleaseTrackId": "optional",
    "libraryId": "permission-gated",
    "path": "permission-gated"
  }
}
```

The response is:

```json
{
  "lyrics": [
    { "lang": "en", "text": "<tt ...>...</tt>" }
  ]
}
```

`text` is required, `lang` is optional, and multiple lyric entries may be returned. Navidrome only supplies `libraryId` and `path` when the plugin has permission to access the track's library, which this provider does not need. The authoritative fields and permission behavior are in the [capability schema](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/capabilities/lyrics.yaml#L18-L140).

Navidrome content-sniffs each returned `text` and parses TTML, SRT, enhanced or ordinary LRC, YAML, or plain text. Invalid entries are skipped; a blank language becomes the `xxx` undetermined code ([parser dispatch](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/model/lyrics_parse.go#L18-L79), [plugin adapter](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/lyrics_adapter.go#L40-L78)). Better Lyrics' default response is TTML, so the plugin should pass that string through unchanged rather than converting it to a less expressive format.

Navidrome's TTML reader covers timed lines and spans, agents, background lines, translations, and transliterations/pronunciations, which aligns with the richer Apple-style TTML emitted by Better Lyrics ([TTML line parsing](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/model/lyrics_ttml.go#L119-L208), [TTML roles and metadata](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/model/lyrics_ttml.go#L606-L700)).

### Runtime constraints

- Current Navidrome master permits at most two concurrent lyrics calls per plugin instance ([adapter](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/lyrics_adapter.go#L17-L35)). This protects the provider somewhat, but all users still share the server's public IP and its Better Lyrics quota.
- A plugin host call has a 30-second default deadline. Host HTTP uses a 10-second client timeout, follows at most five redirects, limits response bodies to 10 MiB, and only permits hostnames declared in the manifest ([HTTP host interface](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/host/http.go#L22-L40), [HTTP client constraints](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/host_httpclient.go#L19-L23), [request validation and execution](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/plugins/host_httpclient.go#L70-L161)).
- The rich lyrics parser is available in Navidrome 0.63.x. The intended target should be Navidrome 0.63 or newer, and in practice a build containing PR #5733 for the sidebar experience ([v0.63.2 release](https://github.com/navidrome/navidrome/releases/tag/v0.63.2)).

## Better Lyrics API facts

### Request and response

The first-party endpoint is:

```text
GET https://lyrics-api.boidu.dev/getLyrics
```

Required query parameters are song title (`s`, `song`, or `songName`) and artist (`a`, `artist`, or `artistName`). Album (`al`, `album`, or `albumName`) and duration in seconds (`d` or `duration`) are optional, but the provider recommends sending album and duration to improve matching ([endpoint reference](https://lyrics-api-docs.boidu.dev/reference/get-lyrics/), [best practices](https://lyrics-api-docs.boidu.dev/docs/best-practices/)). The smallest request should use `s`, `a`, optional `al`, and `d`.

A successful response is documented as:

```json
{
  "ttml": "<?xml version=\"1.0\" ...",
  "score": 0.97
}
```

The useful response headers include `X-Cache-Status`, `X-Provider`, `X-RateLimit-*`, and `X-Auth-Mode` ([response format](https://lyrics-api-docs.boidu.dev/docs/response-format/)). The implementation should require only a non-empty `ttml` string and tolerate an absent `score`; cached production responses observed during this research did not consistently include `score`.

The default endpoint is the right MVP choice because it uses the service's default TTML provider. First-party docs also expose `/ttml/getLyrics`, `/kugou/getLyrics` for line-timed LRC, and a deprecated `/legacy/getLyrics`; adding client-side fallback to Kugou would broaden behavior, use a separate cache namespace, and spend more quota ([provider documentation](https://lyrics-api-docs.boidu.dev/docs/providers/)).

### Authentication, cache, and availability

API keys are not currently issued. Public requests can retrieve cached lyrics without a key, but when key enforcement is enabled, an uncached request returns `401 Unauthorized`. A valid key would bypass rate limits and cache-only restrictions, but there is no supported way for a new plugin user to obtain one today ([authentication documentation](https://lyrics-api-docs.boidu.dev/docs/authentication/)).

This changes the product promise substantially:

- A cache hit can work immediately.
- A cache miss cannot be filled automatically through the public API.
- The documentation's suggested way to prime a miss through the Better Lyrics extension is not an acceptable server-side fallback mechanism.
- The plugin must describe itself as best effort and must not imply that every song can be resolved.

The API uses per-IP token buckets. The documented defaults are 2 requests/second with burst 5 for normal traffic and 10 requests/second with burst 20 for cache-eligible traffic; response headers are authoritative because production settings can differ. `429` includes `Retry-After` ([rate-limit documentation](https://lyrics-api-docs.boidu.dev/docs/rate-limiting/)). A Navidrome server's users share one source IP, so one busy instance can exhaust the quota even if individual listeners are modest.

Documented errors include `422` for missing parameters, `404` for no match, `401` for an authenticated-cache miss, `429` for quota, and upstream/server failures. The API negatively caches not-found results, so rapid repeated lookups are unhelpful ([error handling](https://lyrics-api-docs.boidu.dev/docs/error-handling/), [best practices](https://lyrics-api-docs.boidu.dev/docs/best-practices/)).

The official API repository describes the service as a single CAX21 server in Helsinki with backups and publishes a status page; no contractual SLA was found ([API README](https://github.com/better-lyrics/api/blob/b0be234886b8649196362d388743e3fef8d574e8/README.md#L7-L64), [status page](https://better-lyrics-status.boidu.dev/)). Its live [`/health`](https://lyrics-api.boidu.dev/health) endpoint reported healthy cache and provider state on 2026-08-23, but that is a point-in-time observation, not an availability guarantee.

### Privacy, attribution, and legal uncertainty

The request discloses title, artist, album, and duration to Better Lyrics. In a Navidrome deployment, the API normally sees the server's public IP and user agent rather than each browser user's IP. Better Lyrics' first-party privacy policy says request metadata may include IP address, user agent, and song metadata, that request logs are retained for up to seven days, and that song metadata may be forwarded to third-party APIs ([privacy policy](https://github.com/better-lyrics/better-lyrics/blob/931f25829f6cfd81d0042ca36b4308a0cd38d467/PRIVACY.md#L15-L53)). This disclosure belongs in the plugin README and release notes.

The API source is GPL-3.0 and asks integrations for attribution ([API README](https://github.com/better-lyrics/api/blob/b0be234886b8649196362d388743e3fef8d574e8/README.md#L7-L64)). The Navidrome PDK is distributed from the GPL-3.0 Navidrome repository, and the official Apple Music plugin is also GPL-3.0 ([Navidrome license](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/LICENSE), [Apple Music plugin license](https://github.com/navidrome/apple-music-plugin/blob/60a6100f7aaeca676de71cc7b343e49ef42e2fca/LICENSE)). The safest release posture is therefore to license this plugin GPL-3.0 and implement an original thin client rather than copying Better Lyrics API code. The repository's current MIT license should be resolved before publishing implementation commits.

The API software license does not itself grant rights to redistribute the returned lyrics, promise continued service access, or resolve the terms of upstream lyric providers. The API's TTML provider uses upstream accounts and bearer/media-user tokens ([provider source](https://github.com/better-lyrics/api/blob/b0be234886b8649196362d388743e3fef8d574e8/services/providers/ttml/ttml.go#L55-L167), [deployment variables](https://github.com/better-lyrics/api/blob/b0be234886b8649196362d388743e3fef8d574e8/.env.example#L24-L43)). That creates service-continuity, upstream-terms, and lyric-content-rights risk. This is an engineering risk assessment, not legal advice; public distribution should wait for explicit project-owner comfort with the API's intended third-party use and the repository license.

Navidrome's lyrics response has no provider, source URL, or attribution field, so the plugin cannot make attribution visible inside #5733's sidebar through the current contract. A plugin-only release can attribute Better Lyrics in `README.md` and `manifest.json`; in-product provenance would require a separate Navidrome API/UI change.

## Correct fallback semantics

Navidrome does not ask every source and merge results. It evaluates `LyricsPriority` from left to right and returns the first non-empty result. Tokens beginning with `.` are sidecar extensions, `embedded` selects embedded tags, and other tokens select a plugin by its installed package filename/ID ([priority resolver](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/core/lyrics/lyrics.go#L48-L124), [sidecar source](https://github.com/navidrome/navidrome/blob/23548f40a09f5f8bf9cf1e1bc2d4c2b236777560/core/lyrics/sources.go#L28-L92)). Source errors are logged and resolution continues to the next entry.

If the installed package is `better-lyrics.ndp`, the recommended configuration is:

```ini
LyricsPriority = ".ttml,.yaml,.yml,.elrc,.lrc,.srt,.txt,embedded,better-lyrics"
```

This keeps every local source—sidecar first, then embedded tags—ahead of the network provider. If the literal desired policy is “sidecars, then Better Lyrics, then embedded,” move `better-lyrics` immediately before `embedded`; that is less privacy-preserving and creates avoidable network dependence when a track already contains embedded lyrics.

The plugin should not inspect the music filesystem, search for sidecars, or receive library paths. Navidrome's priority resolver already decides whether local lyrics exist before it calls the plugin. Avoiding library permission gives the module a smaller security boundary and prevents duplicated precedence logic.

PR #5733's frontend hook calls `getLyricsBySongId`, coalesces concurrent requests, and keeps bounded positive and short-lived negative in-memory caches ([PR branch hook](https://github.com/ranokay/navidrome/blob/516700bd275c0f3586ff95d159c1f8d1f29c2c9c/ui/src/audioplayer/useEnhancedLyrics.js#L11-L79)). Those browser caches improve repeat navigation but do not replace provider-side rate handling because different clients and server requests still share the same plugin/API quota.

## Smallest viable architecture

1. **One Go/TinyGo WebAssembly module.** Follow the official plugin shape and call `lyrics.Register` with a provider implementing `GetLyrics`. Pin a compatible Navidrome PDK version in `go.mod` ([official plugin module](https://github.com/navidrome/apple-music-plugin/blob/60a6100f7aaeca676de71cc7b343e49ef42e2fca/go.mod#L1-L9)).
2. **Minimal manifest permissions.** Request only `http.requiredHosts = ["lyrics-api.boidu.dev"]`. Do not request users, library, filesystem, or write permissions. A host `cache` permission is optional for a short positive cache; omit it from the first version unless load testing justifies it.
3. **Deterministic request construction.** Reject empty title/artist before making HTTP calls. Use proper URL query encoding. Send `s`, `a`, optional `al`, and duration seconds `d`, plus an identifiable plugin user agent.
4. **Preserve TTML exactly.** Decode only the response envelope, require non-empty `ttml`, ignore unknown fields and optional `score`, and return the original TTML as one lyrics entry. Do not normalize whitespace or down-convert to LRC.
5. **Conservative status handling.** Treat `200` plus non-empty TTML as success. Treat `404` and the documented unauthenticated `401` cache miss as no lyrics so Navidrome can continue its priority chain. Treat `422` as a request/data bug. Return an error for `429`, `5xx`, network, oversized-body, and malformed-response failures so Navidrome logs the operational cause. Do not synchronously retry: a retry consumes shared quota, and the user can retry after the server-provided interval.
6. **No secret-dependent design yet.** An optional `api_key` configuration can be added when Better Lyrics actually issues keys. Until then, secret storage and authenticated code paths add complexity without enabling users. If introduced later, send it only to the allowlisted HTTPS origin and redact it from logs and errors.
7. **Documentation is part of the feature.** Installation must show the exact `LyricsPriority` line, the Navidrome/#5733 compatibility boundary, cached-only API behavior, metadata disclosure, rate/error behavior, and Better Lyrics attribution.

A local 2026-08-23 compatibility probe fetched a cached TTML response from the documented endpoint and parsed it with this Navidrome checkout's `model.ParseLyrics`: 97 lines and 731 timed cues were retained, with one agent. This validates the raw-TTML pass-through direction against one real response; it does not prove that all provider dialects or songs parse correctly.

## Focused verification plan

- Unit-test query encoding, required fields, optional album/duration, and absence of unsupported identifiers.
- Use a saved, appropriately licensed/minimized fixture to assert byte-for-byte TTML pass-through on `200`.
- Test `404` and cache-miss `401` as empty results; test `422`, `429`, `5xx`, network failure, malformed JSON, and missing/blank `ttml` as the chosen error classes.
- Verify the manifest denies undeclared hosts and does not request library/user permissions.
- Build with TinyGo, package the `.ndp`, and inspect the archive for exactly the expected manifest/module assets.
- Run end to end against a local Navidrome containing #5733: a track with a sidecar must produce no Better Lyrics request; a track without any local lyric source must invoke the plugin and display successfully returned TTML in the sidebar.
- Exercise simultaneous requests to confirm the plugin respects Navidrome's concurrency bound and handles `Retry-After` without a retry storm.

## Open risks and gates

| Risk | Consequence | Gate or mitigation |
|---|---|---|
| PR #5733 is not part of users' Navidrome build | Plugin works through the API but does not create the requested sidebar | Document the dependency; do not claim UI installation |
| No API keys are issued and uncached public misses can be denied | Coverage is inherently incomplete | Ship only as experimental/best effort, or wait for supported keys/public miss access |
| Shared per-server IP quota | One active server can receive `429` across users | Keep local lyrics first, no synchronous retries, optional bounded positive cache |
| Single public service and upstream-account dependencies | Outage or policy change can break the provider | Fail cleanly, keep local sources first, expose operational errors |
| Metadata-only matching | Remasters, live versions, and same-title songs can mismatch | Always send album and duration; never silently rewrite returned timing |
| API response/dialect changes | Parsing may fail after provider changes | Saved compatibility fixture plus periodic end-to-end check |
| No provenance field in Navidrome lyrics | Attribution cannot appear in the sidebar | README/manifest attribution now; separate core/API proposal if in-product provenance is required |
| Track metadata leaves the user's server | Privacy expectation may be violated | Explicit opt-in installation and clear disclosure |
| Repository, API-code, service, and lyric-content rights are not one license question | Public distribution could carry legal or terms risk | Prefer GPL-3.0 for plugin code; copy no API implementation; obtain project-owner/legal comfort before release |
| Current target repository is MIT | Potential mismatch with the GPL PDK/plugin ecosystem | Resolve and record the intended license before substantive implementation |

## Recommendation

Proceed with a small experimental provider only if the repository owner accepts three facts up front: it does not install the sidebar, unauthenticated coverage is cached-only today, and public distribution needs an explicit license/service-rights decision. Technically, the clean design is a stateless TTML pass-through provider selected last in `LyricsPriority`; Navidrome, rather than the plugin, should remain responsible for detecting sidecar and embedded lyrics.
