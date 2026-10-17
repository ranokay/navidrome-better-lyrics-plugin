# Phase B: per-song lyrics controls (Navidrome PR #6033) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the Phase B contract (per-entry lyrics source, all source-tagged variants in one response) and the three player controls (source switcher, timing offset, download) on top of `navidrome/navidrome#6033`.

**Architecture:** The plugin capability moves `LyricsSource` to each `LyricsText`; the adapter merges entry → response → plugin defaults and appends parsed entries in response order, so the existing core walk, legacy `getLyrics`, Jellyfin, and the priority rules stay untouched. The UI keeps the full structured response, groups main entries into source variants, and derives selection/layers/offset locally; controls extend the existing `LyricsLayerControls` row.

**Tech Stack:** Go (Ginkgo/Gomega, extism/WASM testdata plugin), React 17 + Material-UI v4, vitest + @testing-library/react + @testing-library/react-hooks.

**Spec:** `navidrome-better-lyrics-plugin/docs/superpowers/specs/2026-10-17-phase-b-navidrome-lyrics-controls-design.md` (read it before starting; it carries the settled calls and non-goals).

## Global Constraints

- Target repo/checkout: `/Users/ranokay/Downloads/workspaces/navidrome`, branch `lyrics-source-provenance`. Commits land on this branch (they update PR #6033). Do not create a new branch.
- Navidrome commit style: Conventional Commits, DCO sign-off on every commit (`git commit --signoff`), explicit paths (never `git add -A`), atomic commits.
- Leave the pre-existing dirty files alone: `ui/package-lock.json` (modified) and root `package-lock.json` (untracked). Never stage them.
- No design notes or Phase B docs inside the navidrome diff; docs stay in the plugin repo.
- Go tests run through `make test PKG=<pkg>` (adds build tags). Plugin tests need `plugins/testdata/test-lyrics.ndp`, which is untracked and built by `make -C plugins/testdata test-lyrics.ndp` after any change to `plugins/testdata/test-lyrics/` or `plugins/pdk/go/`.
- UI: `cd ui && npm run test -- <file>` (vitest, `--watch=false`), `npm run lint`, `npm run check-formatting`, `npm run prettier`. ESLint runs with `--max-warnings 0`.
- i18n: new strings go in `ui/src/i18n/en.json` only; `make test-i18n` must stay green.
- Existing behavior that must not change: `LyricsPriority` first-non-empty walk; `LyricList.Main()` as the winner for `getLyrics`/Jellyfin; non-enhanced `getLyricsBySongId` shape; embedded/sidecar source stamping; existing UI language/timing pick for a single source.
- The Subsonic e2e suite is deliberately not extended: it builds `lyrics.NewLyrics(ds, nil)` with no plugin host, and the real WASM boundary is covered by the adapter tests. Say so in the final report.

## Review Focus

Input classes and failure modes the spec implies; each is pinned by a test in the owning task.

1. **Several main entries from ONE source** (embedded multi-language tags, TTML translation tracks): must stay a single variant, with today's language/timing pick — no duplicate "Embedded lyrics" entries, no locale regression. (Task 3)
2. **A plugin that omits per-entry sources** (shipped v0.3.0): byte-identical behavior to today. (Task 1)
3. **A selected variant that disappears after a refresh or was saved for a source the track no longer has**: degrades to auto without blanking the panel or throwing. (Tasks 6, 7)
4. **Offset extremes**: repeated 500 ms steps clamp at ±5000 ms; offsetting never breaks the karaoke timeline. (Tasks 4, 7)
5. **Downloaded files**: exact, parser-compatible LRC (`[mm:ss.xx]`, `<mm:ss.xx>`, `hh:mm:ss.xx` past 99 min) and TTML (clock times, escaped text) — an exported file must be re-importable. (Task 5)

---

### Task 1: Per-entry source on the wire (capability + PDK + adapter)

**Files:**
- Modify: `plugins/capabilities/lyrics.go`
- Modify: `plugins/capabilities/lyrics.yaml`
- Regenerate (do not hand-edit): `plugins/pdk/go/lyrics/lyrics.go`, `plugins/pdk/go/lyrics/lyrics_stub.go`, `plugins/pdk/rust/nd-pdk-capabilities/src/lyrics.rs`
- Modify: `plugins/testdata/test-lyrics/main.go`
- Modify: `plugins/lyrics_adapter_test.go`
- Rebuild: `plugins/testdata/test-lyrics.ndp` (untracked artifact)

**Interfaces:**
- Produces (capability wire contract; JSON below is authoritative):

```json
{
  "lyrics": [
    { "lang": "eng", "text": "…", "source": { "provider": "NetEase", "format": "elrc" } }
  ],
  "source": { "provider": "…", "format": "…" }
}
```

- `LyricsText` gains `Source *LyricsSource`; `GetLyricsResponse.Source` stays as the legacy default. `lyrics[]` is in preference order: the first entry that parses to a main-kind lyric is the winner.
- Produces (adapter helper): `func lyricsSourceFor(entry capabilities.LyricsText, defaults *capabilities.LyricsSource, pluginName string) *model.LyricsSource` — returns `{Type: model.LyricsSourcePlugin, Name: pluginName, Provider: entry.Provider||defaults.Provider, Format: lower(entry.Format||defaults.Format)}` (trimmed).
- Produces (testdata plugin config key): `variants` — JSON array of `{"lang","text","provider","format"}`. When set, the test plugin returns exactly those entries in order, each with a per-entry source when `provider` or `format` is present; `source_provider`/`source_format` keep working as the response-level default.

- [ ] **Step 1: Add the capability field and regenerate the PDK**

Add the field + doc comments to `plugins/capabilities/lyrics.go` and `plugins/capabilities/lyrics.yaml` (`LyricsText.source`, nullable ref to `LyricsSource`; keep `GetLyricsResponse.source` documented as the legacy default). Then run `make gen` and confirm the only generated diffs are the three PDK files above.

- [ ] **Step 2: Teach the testdata plugin variants**

Extend `plugins/testdata/test-lyrics/main.go`: read `pdk.GetConfig("variants")`, `json.Unmarshal` into `[]struct{ Lang, Text, Provider, Format string }`, and when the array is non-empty return it as `Lyrics` with per-entry `Source` (mirroring the existing response-level `source`), before the format-based branch. Rebuild: `make -C plugins/testdata test-lyrics.ndp`.

- [ ] **Step 3: Write the failing adapter tests**

In `plugins/lyrics_adapter_test.go`, inside `Describe("GetLyrics")`:

```go
It("labels each variant with its own source", func() {
    manager, _ := createTestManagerWithPlugins(map[string]map[string]string{
        "test-lyrics": {"variants": `[{"text":"[00:01.00]Alpha line","provider":"Alpha","format":"lrc"},
                                      {"text":"[00:02.00]Beta line","provider":"Beta","format":"lrc"}]`},
    }, "test-lyrics"+PackageExtension)
    p, ok := manager.LoadLyricsProvider("test-lyrics")
    Expect(ok).To(BeTrue())

    result, err := p.GetLyrics(GinkgoT().Context(), &model.MediaFile{ID: "track-1", Title: "Test Song", Artist: "Test Artist"})
    Expect(err).ToNot(HaveOccurred())
    Expect(result).To(HaveLen(2))
    Expect(result[0].Source).To(Equal(&model.LyricsSource{Type: model.LyricsSourcePlugin, Name: "Test Lyrics", Provider: "Alpha", Format: "lrc"}))
    Expect(result[1].Source).To(Equal(&model.LyricsSource{Type: model.LyricsSourcePlugin, Name: "Test Lyrics", Provider: "Beta", Format: "lrc"}))
})
```

Plus two specs:
- "falls back to the response source for entries without their own" — `variants` with one labelled and one bare entry, plus `source_provider=Fallback`, `source_format=lrc`; the bare entry gets `Fallback`.
- "keeps the plugin identity when no source is given anywhere" — two bare variants; both sources equal `{Type: plugin, Name: "Test Lyrics"}`.

- [ ] **Step 4: Run them to verify they fail**

Run: `make test PKG=./plugins/...`
Expected: the three new specs fail (all entries share the response-level source / no per-entry label).

- [ ] **Step 5: Implement `lyricsSourceFor` and per-entry stamping in `plugins/lyrics_adapter.go`**

Replace the single `source` variable with a per-entry resolution inside the `resp.Lyrics` loop; keep parsing, skip-empty, and append order exactly as today.

- [ ] **Step 6: Run the adapter tests again**

Run: `make test PKG=./plugins/...`
Expected: PASS, including the pre-existing response-level provenance spec.

- [ ] **Step 7: Commit**

```bash
git add plugins/capabilities/lyrics.go plugins/capabilities/lyrics.yaml plugins/pdk/go/lyrics/lyrics.go plugins/pdk/go/lyrics/lyrics_stub.go plugins/pdk/rust/nd-pdk-capabilities/src/lyrics.rs plugins/testdata/test-lyrics/main.go plugins/lyrics_adapter_test.go
git commit --signoff -m "feat(plugins): label each lyrics entry with its source"
```

---

### Task 2: Pin variant passthrough in core and Subsonic

**Files:**
- Modify: `core/lyrics/lyrics_test.go`
- Modify: `server/subsonic/lyrics_test.go`

**Interfaces:**
- Consumes: Task 1's per-entry sources (as `model.Lyrics.Source`).
- No production code change is expected; these pin the contract end to end. If a test fails, fix production code in the owning package, not the test.

- [ ] **Step 1: Write the core passthrough spec**

In the `Context("plugin sources")` block of `core/lyrics/lyrics_test.go`:

```go
It("returns every source-tagged variant in order", func() {
    conf.Server.LyricsPriority = "test-lyrics-plugin"
    mockLoader.lyrics = model.LyricList{
        {Lang: "eng", Synced: true, Line: []model.Line{{Value: "first"}}, Source: &model.LyricsSource{Type: model.LyricsSourcePlugin, Name: "test-lyrics-plugin", Provider: "NetEase", Format: "elrc"}},
        {Lang: "eng", Synced: true, Line: []model.Line{{Value: "second"}}, Source: &model.LyricsSource{Type: model.LyricsSourcePlugin, Name: "test-lyrics-plugin", Provider: "LRCLib", Format: "lrc"}},
    }
    svc := lyrics.NewLyrics(nil, mockLoader)
    list, err := svc.GetLyrics(ctx, &mf)
    Expect(err).To(BeNil())
    Expect(list).To(HaveLen(2))
    Expect(list[0].Source.Provider).To(Equal("NetEase"))
    Expect(list[1].Source.Provider).To(Equal("LRCLib"))
    main, ok := list.Main()
    Expect(ok).To(BeTrue())
    Expect(main.Line[0].Value).To(Equal("first"))
})
```

- [ ] **Step 2: Write the Subsonic serialization spec**

In `server/subsonic/lyrics_test.go`, add a spec storing two main entries with distinct `Source` values (one `{plugin,"Better Lyrics","NetEase","elrc"}`, one `{plugin,"Better Lyrics","LRCLib","lrc"}`) in the mock media file's `Lyrics` JSON, then assert:

- `getLyricsBySongId?id=1&enhanced=true` returns two `StructuredLyrics`, both `Kind: "main"`, each with its own `responses.LyricsSource`;
- `getLyricsBySongId?id=1` (non-enhanced) returns both entries with `Source` nil and no `Kind` (v1 shape unchanged).

- [ ] **Step 3: Run both**

Run: `make test PKG="./core/lyrics/... ./server/subsonic/..."`
Expected: PASS. Investigate and fix immediately if not.

- [ ] **Step 4: Commit**

```bash
git add core/lyrics/lyrics_test.go server/subsonic/lyrics_test.go
git commit --signoff -m "test(lyrics): pin multi-source variant passthrough and serialization"
```

---

### Task 3: Collect and select lyric variants (UI data layer)

**Files:**
- Modify: `ui/src/audioplayer/lyrics.js`
- Modify: `ui/src/audioplayer/lyrics.test.js`

**Interfaces:**
- Produces:

```js
export const variantKey = (source) => string          // (provider || name || type).trim().toLowerCase(); '' → 'unknown'
export const variantSync = (lyric) => 'word' | 'line' | 'plain'
export const collectLyricVariants = (structuredLyrics) => Array<{ key, source, sync, lyric }>
export const selectLyricLayers = (structuredLyrics, preferredLanguage, selectedSourceKey = null)
  => ({ main, translation, pronunciation })
```

- Behavior: variants group content-bearing main-kind entries by `variantKey`, in first appearance order; when no entry has `kind: main`, all content-bearing entries are candidates (today's fallback). `selectedSourceKey` selects that variant's entries for `main`; `null` means the first variant (the plugin winner); an unknown key falls back to the first variant. Within the chosen variant, the existing `pickPreferredLyric` (language → timed) picks the entry. `translation`/`pronunciation` prefer entries whose source matches the selected variant, else fall back to today's global pick. The 3-arg signature must stay compatible with existing 2-arg calls.

- [ ] **Step 1: Write the failing tests** in `ui/src/audioplayer/lyrics.test.js`

```js
it('groups main entries from the same source into one variant', () => {
  const lyrics = [
    { kind: 'main', lang: 'eng', synced: true, line: [{ start: 0, value: 'Hello' }], source: { type: 'embedded' } },
    { kind: 'main', lang: 'jpn', synced: true, line: [{ start: 0, value: 'こんにちは' }], source: { type: 'embedded' } },
  ]
  const variants = collectLyricVariants(lyrics)
  expect(variants).toHaveLength(1)
  expect(variants[0].key).toBe('embedded')
})

it('lists plugin variants in response order with their sync levels', () => {
  // three entries: NetEase elrc with cues (word), LRCLib lrc timed (line), Genius plain (plain)
  expect(collectLyricVariants(lyrics).map((v) => [v.key, v.sync])).toEqual([
    ['netease', 'word'], ['lrclib', 'line'], ['genius', 'plain'],
  ])
})

it('selects the requested variant and keeps within-variant language preference', () => { /* selectedSourceKey 'lrclib' → main is the LRCLib entry; auto → first variant */ })

it('scopes translation layers to the selected source with a global fallback', () => { /* translation entries for NetEase and other sources → selecting netease yields the NetEase translation; selecting lrclib (no translation) yields the global language pick */ })
```

- [ ] **Step 2: Run to verify failure**

Run: `cd ui && npm run test -- src/audioplayer/lyrics.test.js`
Expected: FAIL — `collectLyricVariants is not a function`.

- [ ] **Step 3: Implement** the helpers and the `selectLyricLayers` rework in `ui/src/audioplayer/lyrics.js`.

- [ ] **Step 4: Run the file**

Run: `cd ui && npm run test -- src/audioplayer/lyrics.test.js`
Expected: PASS, including the pre-existing language/timing tests (they call `selectLyricLayers` with two arguments).

- [ ] **Step 5: Commit**

```bash
git add ui/src/audioplayer/lyrics.js ui/src/audioplayer/lyrics.test.js
git commit --signoff -m "feat(ui): collect and select lyrics source variants"
```

---

### Task 4: Offset math and local preferences

**Files:**
- Modify: `ui/src/audioplayer/lyrics.js`
- Create: `ui/src/audioplayer/lyricsPreferences.js`
- Modify: `ui/src/audioplayer/lyrics.test.js`
- Create: `ui/src/audioplayer/lyricsPreferences.test.js`

**Interfaces:**
- Produces (`lyrics.js`):

```js
export const LYRICS_OFFSET_FINE_MS = 50
export const LYRICS_OFFSET_COARSE_MS = 500
export const LYRICS_OFFSET_LIMIT_MS = 5000
export const clampLyricsOffset = (value) => number        // NaN → 0, otherwise clamped to ±LIMIT
export const stepLyricsOffset = (current, delta) => number // clampLyricsOffset(current + delta)
export const formatLyricsOffset = (ms) => string           // '-0.05 s' | '0.00 s' | '+1.25 s'
export const hasNudgeableTiming = (lyric) => boolean       // hasTimedLines(lyric) || hasCueTiming(lyric)
export const buildKaraokeLines = (structuredLyric, userOffsetMs = 0) => lines  // line times = server offset + user offset
```

- Produces (`lyricsPreferences.js`), storage keys exactly:

```js
export const readPreferredSource = (trackId) => string | null   // localStorage 'lyrics.source:<trackId>'
export const writePreferredSource = (trackId, sourceKey) => void // null/'' removes the key
export const readLyricsOffset = (trackId, sourceKey) => number   // 'lyrics.offset:<trackId>:<sourceKey>', 0 when absent/invalid
export const writeLyricsOffset = (trackId, sourceKey, offsetMs) => void // 0 removes the key
```

All storage access is try/catch-guarded (private mode degrades silently), matching `getPreferredLyricLanguage`.

- [ ] **Step 1: Write the failing tests**

`lyrics.test.js`: `clampLyricsOffset` (0, ±4999, ±5001 → ±5000, NaN → 0); `stepLyricsOffset` walking `+500` five times from 3000 stops at 5000; `formatLyricsOffset(-50) === '-0.05 s'`, `(0)`, `(1250) === '+1.25 s'`; `hasNudgeableTiming` false for plain, true for line/word; `buildKaraokeLines` composes server `offset: 500` with `userOffsetMs: -300` → line start 200, and `userOffsetMs` alone shifts cue word starts.

`lyricsPreferences.test.js`:
```js
it('stores and reads the preferred source per track', () => {
  writePreferredSource('song-1', 'netease')
  expect(readPreferredSource('song-1')).toBe('netease')
  expect(localStorage.getItem('lyrics.source:song-1')).toBe('netease')
  writePreferredSource('song-1', null)
  expect(readPreferredSource('song-1')).toBeNull()
})

it('stores the offset per track and source', () => {
  writeLyricsOffset('song-1', 'netease', -250)
  expect(readLyricsOffset('song-1', 'netease')).toBe(-250)
  expect(readLyricsOffset('song-1', 'lrclib')).toBe(0)
  writeLyricsOffset('song-1', 'netease', 0)
  expect(localStorage.getItem('lyrics.offset:song-1:netease')).toBeNull()
})

it('degrades silently when storage throws', () => { /* spy on localStorage.setItem to throw → writes don't throw, reads return defaults */ })
```

- [ ] **Step 2: Run to verify failure** — `cd ui && npm run test -- src/audioplayer/lyrics.test.js src/audioplayer/lyricsPreferences.test.js`

- [ ] **Step 3: Implement** `lyricsPreferences.js` and the `lyrics.js` additions (`buildKaraokeLines` signature change is source-compatible: existing callers pass one argument).

- [ ] **Step 4: Run to verify pass** (same command), then the full `lyrics.test.js`.

- [ ] **Step 5: Commit**

```bash
git add ui/src/audioplayer/lyrics.js ui/src/audioplayer/lyrics.test.js ui/src/audioplayer/lyricsPreferences.js ui/src/audioplayer/lyricsPreferences.test.js
git commit --signoff -m "feat(ui): add lyrics offset math and local preferences"
```

---

### Task 5: Source labels and download serialization

**Files:**
- Create: `ui/src/audioplayer/lyricsSource.js`
- Create: `ui/src/audioplayer/lyricsExport.js`
- Create: `ui/src/audioplayer/lyricsSource.test.js`
- Create: `ui/src/audioplayer/lyricsExport.test.js`

**Interfaces:**
- Produces (`lyricsSource.js`) — move `formatSourceValue` and `getSourceName` **verbatim** from `ui/src/audioplayer/LyricsLayerControls.jsx` (Task 7 deletes the local copies):

```js
export const formatSourceValue = (value) => string
export const getSourceName = (source, labels) => string  // embedded/sidecar/plugin labels, else provider/name/type
```

- Produces (`lyricsExport.js`):

```js
export const lyricsFileExtension = (format) => 'ttml' | 'lrc' | 'txt'
  // 'ttml' → 'ttml'; 'lrc' | 'elrc' | 'srt' → 'lrc'; anything else/unknown → 'txt'
export const buildLyricsFileName = ({ artist, title, label, format }) => string
  // '<artist> - <title> (<label>).<ext>' with [\\/:*?"<>|] stripped, whitespace collapsed, fallbacks 'Unknown artist'/'Unknown title'/'Lyrics'
export const serializeLyrics = (lyric, format) => string
export const downloadLyrics = (lyric, { format, artist, title, label }) => void // Blob + temporary anchor, revokes the object URL
```

Serializer rules (exact output pinned by tests):
- `lrc`: one `[mm:ss.xx]` stamp per line (floor to centiseconds); when the lyric has cue lines for that index, the line body is the cues joined as `<mm:ss.xx>value` (cue values already carry trailing spaces); tracks of 100+ minutes switch to `[hh:mm:ss.xx]` stamps. No `[offset:]` tag — timestamps are absolute.
- `ttml`: `<?xml version="1.0" encoding="UTF-8"?>` + `<tt xmlns="http://www.w3.org/ns/ttml" xmlns:itunes="http://music.apple.com/lyric-ttml-internal" xml:lang="<lang>">`, `<body><div>`, one `<p begin="hh:mm:ss.mmm" end="…">` per line with `<span begin end>` per cue when cues exist, closing tags in order. `itunes:timing="Word"` on `<tt>` only when cues exist. All text XML-escaped (`&`, `<`, `>`, `"`). `lang` omitted when it is `xxx` or empty.
- `txt`: line values joined by `\n`.

- [ ] **Step 1: Write the failing tests**

`lyricsSource.test.js`: `formatSourceValue('ttml') === 'TTML'`, `'plain' === 'Plain text'`, `'apple-lyrically' === 'Apple Lyrically'`; `getSourceName({type:'embedded'}, {})` → `'Embedded lyrics'`; plugin with provider/name; plugin without provider → name.

`lyricsExport.test.js`:
```js
it('maps source formats to download extensions', () => {
  expect(lyricsFileExtension('ttml')).toBe('ttml')
  expect(lyricsFileExtension('elrc')).toBe('lrc')
  expect(lyricsFileExtension('srt')).toBe('lrc')
  expect(lyricsFileExtension('plain')).toBe('txt')
})

it('builds a sanitized Artist - Title (source).ext filename', () => {
  expect(buildLyricsFileName({ artist: 'AC/DC: Live', title: 'Back? in Black', label: 'LRCLib', format: 'elrc' }))
    .toBe('ACDC Live - Back in Black (LRCLib).lrc')
})

it('serializes line and word timing to enhanced LRC', () => {
  // one line [8000,11000] "Hello world", cues [{start:8000,value:'Hello '},{start:9500,value:'world'}]
  // → '[00:08.00]<00:08.00>Hello <00:09.50>world'
})

it('switches to hh:mm:ss stamps past 99 minutes', () => { /* 6_000_500 ms → '[01:40:00.50]' */ })

it('serializes a word-timed variant to TTML with escaped text and clock times', () => {
  // expect exact string: '<p begin="00:00:08.000" end="00:00:11.000"><span begin="00:00:08.000" end="00:00:09.500">A &amp; B</span></p>' inside the minimal document, itunes:timing="Word" present
})

it('serializes plain lyrics as text lines', () => { /* 'one\ntwo' */ })
```

- [ ] **Step 2: Run to verify failure** — `cd ui && npm run test -- src/audioplayer/lyricsSource.test.js src/audioplayer/lyricsExport.test.js`

- [ ] **Step 3: Implement** both modules.

- [ ] **Step 4: Run to verify pass; then run `lyrics.test.js` and `LyricsLayerControls.test.jsx`** (the move must not break the controls for now — they still hold their copies until Task 7).

- [ ] **Step 5: Commit**

```bash
git add ui/src/audioplayer/lyricsSource.js ui/src/audioplayer/lyricsSource.test.js ui/src/audioplayer/lyricsExport.js ui/src/audioplayer/lyricsExport.test.js
git commit --signoff -m "feat(ui): add lyrics source labels and download serializers"
```

---

### Task 6: Variants and progressive refresh in `useEnhancedLyrics`

**Files:**
- Modify: `ui/src/audioplayer/useEnhancedLyrics.js`
- Modify: `ui/src/audioplayer/useEnhancedLyrics.test.js`

**Interfaces:**
- Produces:

```js
useEnhancedLyrics({ trackId, updatedAt, disabled = false, requested = true, selectedSourceKey = null })
  => { variants, layers, loading, refreshing, error, retryAfterSeconds, retry, refresh }
```

- `variants` = `collectLyricVariants(structuredLyrics)`; `layers` = `selectLyricLayers(structuredLyrics, preferredLanguage, selectedSourceKey)`; `layers` stays `emptyLyricLayers` (same frozen object) whenever there are no usable entries, so the existing identity assertion keeps holding.
- The cache stores the full `structuredLyrics` array (per cacheKey), so changing `selectedSourceKey` re-derives without a fetch.
- `refresh()` re-requests and **keeps the current variants/layers visible** with `refreshing: true`; on success swaps in the new array; on failure keeps the old content silently (no `error`). A refresh while a request is in flight is coalesced (no second fetch). `retry()` and the cooldown countdown keep today's semantics.

- [ ] **Step 1: Write the failing tests** in `useEnhancedLyrics.test.js` (extend `responseFor` to accept several entries)

```js
it('exposes variants in response order with auto selection', () => { /* two sources → variants[0].key netease; layers.main.line[0].value from netease */ })

it('re-derives layers for a selected source without refetching', () => { /* rerender selectedSourceKey 'lrclib' → layers change, getLyricsBySongId still called once */ })

it('falls back to auto when the selected source disappears', () => { /* select 'lrclib', then a refresh resolves with only netease → layers.main from netease, no error */ })

it('refreshes variants without blanking the current lyrics', async () => {
  // initial response: one source; then refresh deferred; expect refreshing true and layers unchanged
  // resolve with two sources → expect variants length 2, refreshing false, same call count +1
})

it('keeps current lyrics when a refresh fails', () => { /* refresh rejects → layers unchanged, error stays null */ })
```

- [ ] **Step 2: Run to verify failure** — `cd ui && npm run test -- src/audioplayer/useEnhancedLyrics.test.js`

- [ ] **Step 3: Implement** the hook rework: cache `structuredLyrics`, derive `variants`/`layers` with `useMemo`, add `refresh`/`refreshing` (a refresh token that re-runs the request path without clearing state; reuse `acquireLyricsRequest`/`inFlight` so refreshes coalesce).

- [ ] **Step 4: Run to verify pass** — same command; all pre-existing specs (cache-by-track, provenance, clear-while-loading, disabled, cooldown retry) must pass.

- [ ] **Step 5: Commit**

```bash
git add ui/src/audioplayer/useEnhancedLyrics.js ui/src/audioplayer/useEnhancedLyrics.test.js
git commit --signoff -m "feat(ui): refresh lyrics variants progressively"
```

---

### Task 7: Controls and player wiring (one commit — the feature must be usable at the commit boundary)

**Files:**
- Modify: `ui/src/audioplayer/LyricsLayerControls.jsx`
- Modify: `ui/src/audioplayer/LyricsLayerControls.test.jsx`
- Modify: `ui/src/audioplayer/usePlayerLyrics.jsx`
- Modify: `ui/src/audioplayer/usePlayerLyrics.test.jsx`
- Modify: `ui/src/audioplayer/LyricsPanel.jsx`
- Modify: `ui/src/audioplayer/LyricsSidebar.jsx`
- Modify: `ui/src/i18n/en.json`

**Interfaces:**
- `LyricsLayerControls` replaces the `source` prop with:

```js
variants = [],                   // [{ key, source, sync }] from collectLyricVariants
selectedSourceKey = null,        // null = auto
onSelectSource = noop,           // (key | null)
onSourceMenuOpenChange = noop,   // (open: boolean) — drives progressive refresh
refreshing = false,
offsetMs = 0,
offsetEnabled = false,
onOffsetChange = noop,           // (ms)
onDownload = noop,
```

Existing props (`placement`, translation/pronunciation toggles, `active`, `labels`, `testId`) stay. The source button (replacing the info button, `data-testid="lyrics-source-button"`) opens a MUI `Menu` with `role="menuitemradio"` items: "Auto (best match)" (checked when `selectedSourceKey == null`) followed by one item per variant showing `getSourceName(source, labels)`, the format via `formatSourceValue`, and the sync label (`word`/`line`/`plain`). A header row shows `labels.sourceTitle` and a small `CircularProgress` when `refreshing`. Opening/closing calls `onSourceMenuOpenChange`. Import `formatSourceValue`/`getSourceName` from `./lyricsSource` and delete the local copies. Offset control (disabled unless `offsetEnabled`): trigger shows `formatLyricsOffset(offsetMs)`; popover buttons −coarse/−fine/value/+fine/+coarse and Reset (disabled at 0), each with a step-naming aria-label, calling `onOffsetChange(stepLyricsOffset(offsetMs, delta))` / `onOffsetChange(0)`. Download `IconButton` (`aria-label` from `labels.download`) calls `onDownload`.

- `usePlayerLyrics` owns the state and passes it through `desktopLyricsProps` and the mobile surface:
  - `selectedSourceKey` seeded from `readPreferredSource(trackId)` on track change; `selectSource(key)` writes it back;
  - `resolvedVariantKey` = selected key if present in `variants`, else `variants[0]?.key ?? null`;
  - `offsetMs` re-read via `readLyricsOffset(trackId, resolvedVariantKey)` when track or resolved key changes; `changeOffset(ms)` writes back; `offsetEnabled` = `hasNudgeableTiming(lyricLayers.main)`;
  - `onSourceMenuOpenChange(open)` starts a 5 s `setInterval` calling `refresh()` while open, capped at 12 ticks (60 s), cleared on close/unmount/track change;
  - `downloadSelected()` calls `downloadLyrics(lyricLayers.main, { format: lyricLayers.main?.source?.format, artist: lyricLayers.main?.displayArtist, title: lyricLayers.main?.displayTitle, label: getSourceName(lyricLayers.main?.source, labels) })`.
- `LyricsPanel` gains `offsetMs = 0` and passes it to all three `buildKaraokeLines` memos (deps updated).
- `LyricsSidebar` forwards the new props to `LyricsLayerControls` (it already receives `desktopLyricsProps` spread from `LyricsLayoutFrame`) and `offsetMs` to `LyricsPanel`.
- `en.json` (`player.*`) gains: `autoLyricsSourceText`, `refreshingLyricsSourcesText`, `wordSyncedLyricText`, `lineSyncedLyricText`, `plainLyricText`, `lyricsOffsetText`, `lyricsOffsetEarlierText`, `lyricsOffsetLaterText`, `lyricsOffsetResetText`, `downloadLyricsText` (interpolation uses `%{seconds}`).

- [ ] **Step 1: Write the failing component tests** in `LyricsLayerControls.test.jsx` (update its `renderControls` helper for the new props)

```js
it('lists auto and every variant with badges, checked state following the selection')
it('calls onSelectSource with the variant key and null for auto')
it('calls onSourceMenuOpenChange(true) when the source menu opens')
it('steps and clamps the offset through the fine and coarse buttons and resets to zero')
it('disables the offset control when the selected variant has no timing')
it('calls onDownload when the download button is pressed')
```

- [ ] **Step 2: Write the failing hook tests** in `usePlayerLyrics.test.jsx` (extend the existing mock setup rather than rewriting it)

```js
it('restores the saved source choice per track and persists changes')
it('falls back to auto when the saved source is missing from the response')
it('keys the offset by track and resolved source')
it('refreshes once per five seconds while the source menu is open and stops at the cap')  // vi.useFakeTimers
it('triggers a download for the selected variant with its format and display fields')
```

- [ ] **Step 3: Run to verify failure**

Run: `cd ui && npm run test -- src/audioplayer/LyricsLayerControls.test.jsx src/audioplayer/usePlayerLyrics.test.jsx`

- [ ] **Step 4: Implement** the component, the `usePlayerLyrics` state/wiring, the `LyricsPanel` offset prop, `LyricsSidebar` forwarding, and the i18n keys.

- [ ] **Step 5: Run the touched suites and lint**

Run: `cd ui && npm run test -- src/audioplayer/LyricsLayerControls.test.jsx src/audioplayer/usePlayerLyrics.test.jsx src/audioplayer/LyricsPanel.test.jsx src/audioplayer/LyricsSidebar.test.jsx src/audioplayer/MobileKaraokeLyricsPortal.test.jsx && npm run lint`
Expected: PASS (update any test that asserted the old `source` prop pass-through to the controls).

- [ ] **Step 6: Commit**

```bash
git add ui/src/audioplayer/LyricsLayerControls.jsx ui/src/audioplayer/LyricsLayerControls.test.jsx ui/src/audioplayer/usePlayerLyrics.jsx ui/src/audioplayer/usePlayerLyrics.test.jsx ui/src/audioplayer/LyricsPanel.jsx ui/src/audioplayer/LyricsSidebar.jsx ui/src/i18n/en.json
git commit --signoff -m "feat(ui): add per-song lyrics source, offset and download controls"
```

---

### Task 8: Branch verification and PR state

**Files:** none (verification only).

- [ ] **Step 1: Rebuild the testdata plugin and run the Go suites**

```bash
make -C plugins/testdata test-lyrics.ndp
make test PKG="./plugins/... ./core/lyrics/... ./server/subsonic/..."
```

Expected: all PASS.

- [ ] **Step 2: Full gates**

```bash
make test                    # Go suite
make test-js                 # vitest (all UI)
make test-i18n               # translation validation
cd ui && npm run lint && npm run check-formatting
```

Expected: all green. Report any check that cannot run with the reason.

- [ ] **Step 3: Regeneration must be clean**

```bash
make gen && git status --short
```

Expected: no diff beyond the files committed in Task 1 (leave the pre-existing `package-lock.json` entries untouched).

- [ ] **Step 4: Report the PR state**

Summarize the commits and gate results for the user. The report must state: the new contract (per-entry `source`, variants winner-first), that the plugin follow-up PR is now unblocked, that the Subsonic e2e was deliberately not extended (no plugin host in that suite; the WASM adapter test covers the boundary), and what PR #6033's description should say. Ask before editing the PR body, and do not push without explicit authorization.
