# Phase B: Navidrome-side per-song lyrics controls

Design date: 2026-10-17
Status: approved in conversation (owner answered the open calls on 2026-10-17)
Target: `ranokay/navidrome` branch `lyrics-source-provenance` (PR navidrome/navidrome#6033)
Companion: this repo's [2026-10-16 spec](2026-10-16-lyrics-sources-and-controls-design.md), "Phase B contract requirement". The plugin follow-up PR serves variants only after the contract below ships.

## Intent and why

The plugin now walks 17 admin-ordered sources, caches each source's result under its own
key (`lyrics:v4:source:<source>:<track>`), and a background sweep fills that cache after
the fast synchronous pass. So per-source variants for a track already exist cheaply in
the plugin KV store; what is missing is a way to label and deliver them, and controls in
the Navidrome player to use them:

- pick a source per song, from the sources that actually returned lyrics for that track,
  with an "auto" option that follows the plugin's winner;
- nudge lyric timing per song when a source is early or late;
- download the selected variant to the device in the format the source reported.

## Settled by the owner (2026-10-17)

- **One payload, all variants.** The plugin returns every cached source-tagged variant for
  a track in one `GetLyrics` response, winner first. No fetch-by-source endpoint.
- **Controls live in the existing bottom control row** (`LyricsLayerControls`), so the
  desktop sidebar and the mobile karaoke portal get them from the same component.
- **Offset persistence keying is track + resolved source.** "Auto" resolves to the variant
  it currently shows; the stored key is that concrete source.
- **The chosen source is persisted per track** in local storage; "auto" is the default.
- **Download is serialized client-side** from the structured lines the UI already has.
  The exported file is faithful to the source (the source's own offset is applied to
  timestamps) and does **not** bake in the listener's local nudge.
- Phase B covers PR #6033 only. Server-side sidecar writes, retain-forever toggles, and
  server-side offset persistence stay out of scope.

## Contract change: per-entry lyrics source

Source of truth: `plugins/capabilities/lyrics.yaml` plus the Go capability struct; the
Go and Rust PDK files are generated (`make gen`).

### Wire shape

```json
{
  "lyrics": [
    {
      "lang": "eng",
      "text": "<tt …>…</tt>",
      "source": { "provider": "Better Lyrics", "format": "ttml" }
    },
    {
      "lang": "xxx",
      "text": "[00:12.34]…",
      "source": { "provider": "LRCLib", "format": "lrc" }
    }
  ],
  "source": { "provider": "Better Lyrics", "format": "ttml" }
}
```

Semantics, in order of precedence:

1. `lyrics[]` is in the plugin's preference order. **The first entry that parses to a
   main-kind lyric is the plugin's winner**, which is what "auto" shows and what every
   single-lyric consumer keeps serving.
2. `LyricsText.source` is optional and labels that entry's upstream. When absent, the
   response-level `source` applies; when that is absent too, only the plugin identity
   applies. This keeps existing plugins (response-level source only) byte-for-byte valid.
3. Navidrome fills `type: plugin` and `name` (plugin display name) itself; plugins supply
   only `provider` and `format`, trimmed, `format` lower-cased.
4. `provider` doubles as the stable selection key for "auto"/switcher persistence. It is
   the human-readable upstream label, unique across the plugin's catalogue. No `id` field
   is added: a renamed provider only resets a local preference.
5. `format` stays a free-form hint (`ttml`, `lrc`, `elrc`, `plain`, …). It drives the
   download serialization and the source details display.

### Navidrome behavior

- `plugins/lyrics_adapter.go`: resolve the source per `LyricsText` (entry → response →
  plugin defaults), parse each text with the existing content sniffing, stamp the parsed
  entries, append in response order. Entries that parse to nothing are skipped without
  dropping later variants.
- `core/lyrics`: no change. The `LyricsPriority` walk still returns the first source with
  a non-empty list; a plugin's variants therefore always come from that one plugin call.
  Variants are never merged across priority entries.
- Single-lyric consumers are unchanged by construction: legacy `getLyrics` and Jellyfin
  `Lyrics` both serve `LyricList.Main()` (first main = winner), and the priority-ordered
  first-non-empty rule is untouched.
- Subsonic `getLyricsBySongId` needs no change: `enhanced=true` already emits `source`
  per `structuredLyric`, and the response is an array, so N mains with distinct sources
  serialize as-is. The non-enhanced v1 shape stays as it is today (no `source`, no
  `kind`), still per entry.
- Until the plugin's follow-up PR serves variants, every existing plugin (including
  v0.3.0) produces one entry with the response-level source: the UI shows a single
  variant and "auto".

## UI behavior

### Variant collection and selection

- A **variant is a source**, not a lyric entry. Group the main-kind entries with content
  by source key (`source.provider || source.name || source.type`), preserving first
  appearance order. This matters because one source can legitimately emit several main
  entries — a media file with English and Spanish embedded `LYRICS` tags is one variant,
  not two — and it keeps keys stable for persistence. No positional suffixes.
- Each variant shows a label (provider, else plugin name, else a type label), a format
  chip from `source.format`, and a sync badge derived client-side: **word** when the
  entry has cue lines, **line** when it has timed lines, **plain** otherwise. Syllable
  granularity is not distinguishable after parsing, so it is not badged.
- Which entry within the selected variant is shown: the existing language/timing pick
  (`pickPreferredLyric`) restricted to that variant's entries. In the single-variant case
  this is exactly today's behavior, so the embedded multi-language and language-matching
  tests keep passing.
- Selection: explicit key when it is still present, otherwise the first variant — the
  plugin's ranking, winner first. Language preference does **not** re-rank plugin
  variants (that would override the plugin's winner choice); it only picks within a
  variant and drives translation/pronunciation layers.
- The selector lists only sources that actually returned lyrics for the track, plus
  "auto"; it fills in progressively (below).

### Layer scoping

- Translation and pronunciation entries prefer the source of the selected variant, then
  fall back to today's language-driven global pick. A plugin that wants its translation
  layers to travel with its main layer tags them with the same source; until then the
  fallback preserves current behavior exactly.
- The per-song offset applies to every displayed layer of the track; it is one playback
  correction, not one per layer.

### Progressive fill

- Opening the source menu triggers a refresh: re-fetch `getLyricsBySongId`, keep the
  currently displayed lyrics until the new response arrives, then swap in the updated
  variant list. While the menu stays open, refresh every 5 s, stopping after 60 s or on
  close. In-flight refreshes coalesce; the existing single-flight/abort machinery in
  `useEnhancedLyrics` is reused. The selection key survives a refresh when it is still
  present.
- The plugin's `GetLyrics` answers these refreshes from its cache (winner cache plus
  per-source entries), so a poll is cheap; a plugin call that lands on a cold track is
  only possible before the first response.

### Timing offset

- Fine step 50 ms, coarse step 500 ms, clamped to ±5000 ms; a reset returns to 0.
- Applied at render-time line building (`buildKaraokeLines`) as
  `server offset + user offset`, so the karaoke timeline needs no separate handling.
- The control is disabled for variants without line/cue timing (plain text).
- Persisted at `lyrics.offset:<trackId>:<variantKey>`; "auto" resolves to the concrete
  variant it shows, so each source carries its own correction.
- Keyboard accessible: buttons for ±coarse/±fine/reset with step-naming `aria-label`s,
  and the current value shown on the trigger.

### Download

- The selected variant serializes from the structured lines (the UI never sees raw
  source text):
  - `ttml` → a minimal TTML document, `<span>` per cue when word timing exists,
    `itunes:timing="Word"`, absolute timestamps that include the source's own offset but
    not the local nudge;
  - `lrc` / `elrc` / `srt` → LRC with `[mm:ss.xx]` lines and `<mm:ss.xx>` word markers
    when the entry has cues;
  - anything else (plain, unknown) → plain text, one line per lyric line.
- Extension follows the same mapping (`.ttml` / `.lrc` / `.txt`).
- Filename: `Artist - Title (Label).ext`, path-hostile characters stripped, whitespace
  collapsed; `Artist`/`Title` from the lyric's display fields, else the track's;
  `Label` is the variant label.
- Download is a Blob + anchor, no server round trip.

### Persistence

- `lyrics.source:<trackId>` — chosen variant key; absent means auto.
- `lyrics.offset:<trackId>:<variantKey>` — milliseconds.
- All storage access is try/catch-guarded like `getPreferredLyricLanguage`; private-mode
  failures degrade to session behavior.

## Non-goals

- No server-side sidecar write, no retain-forever toggle, no server-side offset store.
- No variant merge across `LyricsPriority` entries (first plugin with lyrics still wins).
- No raw-text endpoint through core: downloads are re-serialized client-side.
- No new `id` field on the wire source; the provider label is the key.

## Test plan

Go (Ginkgo, `make test`):

- `plugins/lyrics_adapter_test.go`, against the real compiled `test-lyrics` WASM plugin
  (extend it with config-driven multi-variant output):
  - per-entry sources stamped per variant, plugin name/type filled in;
  - entries without their own source fall back to the response-level source;
  - response order preserved, first entry stays winner; an empty variant does not drop
    later ones.
- `core/lyrics/lyrics_test.go` (fake loader): a multi-variant plugin list passes through
  the walk unchanged and in order; `Main()` still yields the winner.
- `server/subsonic/lyrics_test.go`: two mains with distinct sources serialize as two
  `structuredLyrics` with their sources under `enhanced=true`, one without.
- `server/subsonic/e2e`: **not extended**. The suite builds `lyrics.NewLyrics(ds, nil)`
  with no plugin host, so a variant flow there would have to fake the plugin; the real
  plugin boundary is covered by the adapter test above. Recorded here so the omission is
  deliberate.

UI (vitest, `make test-js`):

- `lyrics.test.js`: variant collection (grouping by source, order, keys, content filter),
  selection resolution (explicit vs auto), within-variant language/timing pick retained,
  layer scoping (translation/pronunciation prefer the selected source, else fall back),
  offset math (server + user, clamps). The existing embedded multi-language and
  language-matching tests stay and must keep passing.
- `lyricsExport.test.js`: LRC with word markers, TTML document shape, plain text,
  filename sanitization, extension mapping.
- `lyricsPreferences.test.js`: source/offset get+set, storage-failure tolerance.
- `LyricsLayerControls.test.jsx`: menu lists auto + variants with badges and checked
  state; selection callback; offset stepper steps/clamp/reset/disabled; download
  callback; keyboard activation via the existing MUI patterns.
- `useEnhancedLyrics.test.jsx`: refresh keeps current layers while loading and swaps in
  updated variants; selection change does not refetch; progressive poll bounds.

## Verification

- `make test` (Go), `make test-js` (vitest --watch=false), `make test-i18n` if any
  translation file is touched, `npm run check-formatting` + eslint in `ui/`, `make gen`
  regenerates PDK without diff beyond the intended change.
- PR #6033 diff stays reviewable: contract change isolated to the capability + adapter +
  testdata plugin; UI work in new/extended files; no design notes inside the navidrome
  diff (they live here).

## Calls made during the builder check (mine, unless overruled)

- **Variants are sources, not entries**; the existing language/timing pick survives inside
  a variant. A file with several embedded `LYRICS` tags must not turn into several
  "embedded" variants, and the existing UI tests for embedded language selection are
  behavior to preserve, not replace.
- **Layer scoping with fallback**: translation/pronunciation prefer the selected
  variant's source, else today's global language pick. The fallback means nothing changes
  for v0.3.0 responses.
- **Unknown or non-serializable formats download as plain text**; `srt` downloads as
  `.lrc` (timed), matching the ttml/lrc/plain mapping the owner specified.

## Risks

- **Payload growth**: worst case one variant per enabled source (17), each capped at
  1 MB plugin-side. Accepted; the UI caches and re-fetches only while the menu is open.
- **Progressive fill has no completion signal**: polling is time-bounded, not
  event-driven. If a "sweep complete" hint ever appears in the contract, the poll can be
  replaced by it.
- **Provider rename resets a saved source choice**; acceptable for a browser-local
  preference.
