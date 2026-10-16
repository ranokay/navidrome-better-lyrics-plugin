# Better Lyrics plugin: multi-source lyrics and UI controls

Design date: 2026-10-16
Status: draft for approval

## Intent and why

The plugin today resolves lyrics from three backends (Better Lyrics TTML, Unison, Kugou)
and returns the single best result. The owner wants the plugin to feel like the
Better Lyrics extension for YouTube Music:

- many lyric sources, including word-synced ones, ranked by sync quality;
- admin controls to toggle and reorder sources;
- per-song source switching, a timing offset, and a save/download option in the
  Navidrome lyrics UI (#6033);
- the plugin icon visible in the README.

## Goals

1. Add these lyric sources (researched against the Morphe reference implementation and
   probed live on 2026-10-15/16):
   - `better-lyrics` "Better Lyrics" — `api.betterlyrics.org/getLyrics` (legacy host
     `lyrics-api.boidu.dev` also serves it). Syllable TTML. Already wired.
   - `better-lyrics-portato` "Better Lyrics Portato" — `/qq/getLyrics`. Word-timed QRC
     XML. New: convert QRC to TTML.
   - `better-lyrics-legato` "Better Lyrics Legato" — `/kugou/getLyrics`. Line LRC. Already wired.
   - `unison` "Unison" — `unison.boidu.dev/lyrics`. Syllable/word/line/plain. Already wired.
   - `binimum` "BiniLyrics" — `lyrics-api.binimum.org` search (307-redirects to
     `lrc.red`) then the record's `lyricsUrl`. Word/line TTML in a custom `lrc:` timing
     namespace; rewrite to `itunes:` so Navidrome's TTML parser sees word timing.
   - `amll` "AMLL" — `api.amll.dev` `/v1/lyrics/search` then `/get?id=&format=ttml`.
     Word/line TTML, no conversion.
   - `luna-beat` "LunaBeat" — static GitHub-Pages TTML hub
     (`2755337087.github.io/ttml-hub`): manifest → songs index → TTML file. Word TTML.
     Index (~1.65 MB, revision-gated) is gzip-compressed into the KV store.
   - `apple-lyrically` "Apple Music (Lyrically)" — `itunes.apple.com/search` for the
     iTunes track id, then `lyrics.paxsenix.org/apple-music/lyrics?id=`. Word-synced
     enhanced LRC (elrc) or Apple TTML; elrc passes through unchanged. Third-party mirror.
   - `musixmatch` "Musixmatch" — `apic.musixmatch.com`: anonymous `token.get`
     (`app_id=android-player-v1.0`, intermittently captcha-gated → retried on the next
     lookup), optional admin-pasted user token from config, `track.search`,
     `macro.subtitles.get`. Word richsync → enhanced LRC; line LRC; plain.
   - `netease` "NetEase" — `music.163.com/api/cloudsearch/pc` (form POST) then
     `music.163.com/api/song/lyric/v1`. Word YRC → TTML; line LRC; plain. Unauthenticated
     plain API; no eapi crypto.
   - `luna` "Soda (Luna)" — `api.qishui.com/luna/search/track` then
     `beta-luna.douyin.com/luna/h5/seo_track`. Word-timed plaintext KRC → TTML.
   - `petitlyrics` "PetitLyrics" — `p0.petitlyrics.com/api/GetPetitLyricsData.php`
     (form POST, `clientAppId=p1110417`, `terminalType=10`). Word WSY XML → TTML; the
     line-level LSY binary decode is best-effort and may ship word+plain only.
   - `lrclib` "LRCLib" — `lrclib.net/api/get`, `/api/search` fallback. Line LRC; word
     via `lyricsFile` YAML → enhanced LRC; plain. Identifying User-Agent required;
     429/503 with `Retry-After` are expected.
   - `lyricify` "Lyricify" — `api.credits.fm/v1/resolve/track` (POST
     `{name, artist}` → ISRC) then
     `api.lyricify.app/lyrics/get/mobile/new?username=<random>&isrc=<base64>`.
     Line LYL/LYS; word/syllable when the file carries `[from:AppleSyllable]`.
     New: convert LYL → enhanced LRC.
2. Admin controls through the manifest `config` (JSONForms in Settings → Plugins):
   an ordered, toggleable source list plus optional secrets. The plugin reads them as
   JSON blobs and falls back to built-in defaults when unset.
3. The icon lives at `docs/assets/icon.png` and is shown in the README (done; the
   Navidrome plugin manifest has no icon field, verified against v0.63.2 and master).

## Non-goals

- No Spotify or Deezer: both require a personal account session cookie
  (`sp_dc` / `arl`) that belongs to one human's subscription; wrong for a shared server.
- No Apple Music official endpoint: lyrics require a subscriber `media-user-token`.
  The Lyrically mirror covers Apple without credentials.
- No YouTube Music / YouTube captions: needs the YouTube session and a videoId,
  which a Navidrome plugin has neither of. SimpMusic's API is the same: videoId-keyed
  and now 403-gated to its own clients.
- No UI rendering inside the plugin: a `.ndp` cannot draw UI. Controls that live in the
  player (source switcher, offset, download) belong to #6033.
- No lyrics translation/romanization return lines from NetEase or elsewhere; original
  lyrics only, matching the extension's behavior for originals.

## Constraints

- Navidrome plugin runtime: WASI, 30 s host deadline, currently 6 s per HTTP request,
  15 s cold-lookup budget, max 2 concurrent lyrics calls per plugin instance. HTTP hosts
  must be declared in the manifest. Output must parse with Navidrome's lyric parsers
  (TTML, LRC enhanced/plain, SRT, YAML, plain) — richer formats must be converted.
- The KV store is capped at 32 MB with a 1 MB per-entry cap; LunaBeat's index is
  compressed under that cap.
- Existing behavior to preserve: local sources still win via `LyricsPriority`; caching
  and cooldown semantics; the plugin works on stock Navidrome and shows provenance on
  builds with #6033.

## Decided by the owner

- Include grey-area / unofficial endpoints (Musixmatch, NetEase, Luna, PetitLyrics,
  Lyricify, Lyrically). Lyricify is in as a source; its LYL→LRC conversion shares the
  word-sync pipeline and it is the lowest-priority word source.
- The per-song source switcher and the timing offset go into navidrome PR #6033.
- Highest-quality icon (2048 px export) in the README.
- `apple-lyrically` and `luna-beat` ship **disabled by default** (third-party mirror
  with unknown longevity; community hub with unclear rights). They appear in the
  config list so admins can enable them deliberately; the README says how.
- Phase B save/download is **download-to-device only**; no server-side sidecar write.
- No plugin-side "retain lyrics indefinitely" toggle; the existing 24 h/1 h/5 min cache
  TTLs stay as they are.

## Design calls settled during the builder check

- **Wire specs and fixtures.** The Morphe patch set (`MorpheApp/morphe-patches`,
  `extensions/music/src/main/java/app/morphe/extension/music/patches/lyrics/requests/`)
  is the reference implementation for request shapes and parsers. Plan phase 0 is a
  capture step: live-probe each new endpoint for one known track and save sanitized
  responses as test fixtures before writing converters.
- **Word-sync output format.** Converters emit *enhanced LRC* (word tags
  `<mm:ss.xx>`) rather than generated TTML: it is simpler, Navidrome parses it, and
  `quality.go` gains a small enhanced-LRC sniff (word markers ⇒ `timingWord`) so
  word-LRC candidates rank between line and syllable. Syllable quality stays exclusive
  to genuine TTML (today only Better Lyrics). TTML remains pass-through only.
- **Walk is sequential.** Reuse the existing sequential walker plus expected-cost
  skipping; no intra-lookup fan-out (wasm concurrency + test matrix not worth it).
  Cold misses may starve tail sources; the 5-minute negative TTL re-walks them later
  and the recommended order puts likely-winners first. Documented in the README.
- **`betterLyricsApiKey` mechanics.** The API's own 401 body documents the mechanism:
  header `X-API-Key`. When the key is configured it is sent on Better Lyrics endpoints
  and a `401` becomes a clean miss (full TTL) instead of the cache-only "uncertain"
  case; broader-query retries still apply. Without a key nothing changes. The key is
  never placed in a query string and never logged.
- **Winner cache and reordering.** The track-level winner cache key gains a fingerprint
  of the enabled+ordered source list, so reordering takes effect immediately; stale
  winner entries orphan until TTL expiry (bounded by the 32 MB store cap).

## Architecture (builder-facing)

- Replace the hardcoded provider walk in `plugin/main.go` with a source registry:
  each source is a descriptor `{id, display, defaultEnabled, hosts, cooldownDefaults,
  fetch(ctx) → []candidateVariant}` plus a rank of expected cost so the walker can skip
  sources that cannot fit the remaining budget.
- One shared word-sync converter module produces *enhanced LRC* from: QRC, KRC, YRC,
  Lyricify LYL/LYS, Musixmatch richsync, PetitLyrics WSY (see the settled call above).
  Reuse `quality.go` grading with the new enhanced-LRC word sniff.
- Walking order comes from config; the config list ships in this order
  (better-lyrics → unison → binimum → amll → luna-beat → apple-lyrically →
  better-lyrics-portato → musixmatch → netease → luna → petitlyrics → lrclib →
  better-lyrics-legato → lyricify) with `luna-beat` and `apple-lyrically` disabled by
  default. The walk stops early on a syllable result; otherwise
  it keeps the best candidate across sources under the time budget. The Better Lyrics
  tiered-query specificity behavior is kept for the three boidu endpoints only; other
  sources use single-query lookups (LRCLib gets a search fallback).
- Cooldowns generalize the existing pattern to per-source keys with per-source default
  cooldowns and `Retry-After` handling. Cache keys gain a source dimension
  (`lyrics:v4:<source>:<track>`) so reordering enabled sources can re-rank from cache
  without refetching; the track-level winner cache (24 h / 1 h / 5 min by health) stays.
- Manifest: `permissions.http.requiredHosts` adds `lrc.red`, `api.amll.dev`,
  `2755337087.github.io`, `lyrics.paxsenix.org`, `itunes.apple.com`,
  `apic.musixmatch.com`, `music.163.com`, `api.qishui.com`,
  `beta-luna.douyin.com`, `p0.petitlyrics.com`, `api.credits.fm`,
  `api.lyricify.app`, `lrclib.net`; version bumps to 0.2.0.
- Config schema (draft-07 + uiSchema, JSONForms 2.5.2 semantics):
  - `sources`: array of `{id (enum), enabled (bool)}` rendered as a table with
    `showSortButtons: true`; read back as one JSON blob via `host.ConfigGet("sources")`.
  - `betterLyricsApiKey` and `musixmatchUserToken`: password-format strings, both
    optional; sent only to their own allowlisted hosts and never logged.
- README: source table, per-host privacy disclosure, attribution lines, and an
  explicit "unofficial endpoints can break" note.

## Phase B (navidrome PR #6033) — summary, detailed design later

Requires a checkout of `ranokay/navidrome` branch `lyrics-source-provenance` (work
starts there after the plugin ships; it will get its own design note):

- Per-song source switcher: the lyrics contract currently carries one `LyricsSource`
  per response, so switching between sources needs per-entry source tags and a core
  path that resolves and returns multiple source-tagged variants. The plugin returns
  multiple variants only once the contract can label them.
- Timing offset: a client-side shift applied to parsed lyrics timings, persisted per
  user (or locally), with a stepper in the shared lyrics controls.
- Save/download: a download control that exports the current variant as `.lrc`/`.ttml`
  to the listener's device (client-side only; no server-side file writes).

## Verification

- `mise run check` stays green: fmt, race tests, WASM build, package check.
- Unit tests per converter with saved fixtures (QRC, KRC, YRC, LYL, WSY, richsync,
  LRCLib lyricsfile YAML); per-source request shaping and status handling tests against
  the fake sender harness; config parsing tests incl. missing/invalid config; cooldown
  and budget-exhaustion tests.
- One live smoke run per new source with a known track, recorded in the PR description.
