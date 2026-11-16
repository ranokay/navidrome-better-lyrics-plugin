# Recording matching and instrumental runtime receipt — 2026-11-17

This is a local runtime observation, not released-host support. It uses the
actual changed WASI plugin and the actual changed host/router/auth/UI. A
test-only host build overlay replaces only the provider HTTP transport with
controlled responses; it does not replace the plugin, router, auth, WASI loader,
KV, or UI.

## Bound artifacts

- Plugin committed head: `f5b88676d0c4dc8f9ccf55cdca5ff2c0b4a16315`
  (`feat/issue-29-validated-matches`).
- Host committed head: `08fe30f67194853fbe3adffa0652264be22f0083`
  (`feat/lyrics-recording-identity`).
- Plugin package SHA-256:
  `dcb3c537eb488b2f77a6c7aeaece334267051bc43855d08db2c962a92e340cac`.
- Test host binary SHA-256:
  `6263a733ee407f221d195a33619e5b28739fca3b88717105027d560040a4a3fa`.
- Overlay transport: `runtime-fixture/host_httpclient_fixture.go`
  `c5cdb6029643e14e054b5ff285a9ea3e0601db53bc59499f7dcbb667faedae3e`;
  fixture transport `runtime-fixture/zz_fixture_transport.go`
  `1aecad9917658cde6aa27f96a2dd58293e27748ba8afbc42049be77368c92d7d`.
- The supplemental test plugin was loaded by the host but excluded from
  `LyricsPriority`; every result below came from `nd-better-lyrics`.

## Controlled scenarios

All runs used the real UI with the scanned track Shape of You / Ed Sheeran /
÷ (Deluxe), the real Subsonic router and auth, and the changed plugin.

| Scenario | Provider fixture | Observed result |
|---|---|---|
| Ambiguous only | LRCLib exact `Shape of You (Live)` at 236 s, 2 s from the 234 s request; search 404 | v1 had no playback; sidebar stayed empty; source menu contained one `LRCLib — Suggestion`; explicit selection rendered its `live suggestion line` and one line group |
| Evidence only | LRCLib exact `instrumental:true`; search 404 | no lyric entries or source menu; empty state read “LRCLib reports this recording is instrumental.” |
| Valid + assertion conflict | LRCLib validated synced lyric; Lyricify `isInstrumental:true` | one line group plus “Lyricify reports an instrumental recording; the lyrics shown are from the selected source.” |
| Outage | LRCLib get/search HTTP 500 | “Lyrics unavailable”; no instrumental evidence note |
| Original retrieval | Better Lyrics TTML response | one line group; `getLyricsVariants` listed original `ttml` as available; `getLyricsOriginal` returned exact TTML bytes (`108` bytes) with `Content-Type: application/ttml+xml`, SHA-256 `6fbb10262928c1f4a6303353acfb5cf89b7a86a1982eebec73eefcfd1e92553f` |

## Receipts

- Scenario JSON: `navidrome-runtime/probe/ambiguous.json`, `evidence.json`,
  `conflict.json`, `outage.json`, `original.json`.
- Original listing: `navidrome-runtime/probe/original-list.json`; exact bytes:
  `navidrome-runtime/probe/original-bytes.ttml`; headers:
  `navidrome-runtime/probe/original-headers.txt`.
- Screenshots: `navidrome-runtime/scenario-ambiguous.png`,
  `scenario-evidence.png`, `scenario-conflict.png`, `scenario-outage.png`,
  `scenario-original.png`.
- Host logs: `navidrome-runtime/server-ambiguous.log`,
  `server-evidence.log`, `server-conflict.log`, `server-outage.log`,
  `server-original.log`; each shows the changed `nd-better-lyrics` package
  loaded.
- Fixtures: `runtime-fixture/ambiguous.json` `172d3d03…`,
  `evidence.json` `ad9ac323…`, `conflict.json` `280af500…`,
  `outage.json` `74d4e564…`, `original.json` `694a3d5c…`.

The all-scenario run was a single local headless Chromium session per
scenario, with a fresh browser context and cleared plugin KV. It does not
qualify released hosts, #6465 response caching, real provider availability, or
other browsers.
