# Multi-Source Lyrics Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the single-backend Better Lyrics Navidrome plugin into a config-driven multi-source resolver (14 sources incl. grey-area endpoints) with a two-stage fetch: fast synchronous pass + background sweep cache.

**Architecture:** A source registry replaces the hardcoded walk in `plugin/main.go`. Each source is a descriptor with a `fetch` function; converters normalize proprietary word-synced formats to enhanced LRC; an in-memory-free KV layout (`lyrics:v4:*`) caches per source and per track with a config fingerprint on the winner key; a `taskqueue` worker sweeps the remaining sources after a cold miss. Sync-quality grading in `quality.go` is reused with one addition (enhanced-LRC word sniff).

**Tech Stack:** Go 1.25 `wasip1/wasm`, Navidrome PDK (host HTTP/KV/Config/TaskQueue/Lifecycle/TaskWorker), `gopkg.in/yaml.v3` (LRCLib lyricsFile only), std `encoding/json` / `encoding/xml`.

**Spec:** `docs/superpowers/specs/2026-10-16-lyrics-sources-and-controls-design.md`

## Global Constraints

- Build/target: `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared` (TinyGo is broken with this PDK — no TinyGo).
- Output texts must parse in Navidrome: TTML, enhanced/plain LRC, plain. Word-synced proprietary formats convert to **enhanced LRC**, never generated TTML (pass-through TTML only).
- Per-request timeout ceiling 6 s (`httpTimeoutMilliseconds`). Stage-1 synchronous pass budget: 8 s. Stage-1 sources: first 3 enabled in config order, syllable early-stop.
- Cache TTLs (v4): winner — syllable 24 h, word/line 1 h, unsynced 5 min, all-miss 5 min; per-source — synced quality 24 h, unsynced 1 h. Winner key includes sha256 fingerprint of the enabled+ordered source list. Store cap/count rules of `cache.go` stay.
- All HTTP hosts go in `permissions.http.requiredHosts`. Secrets (`betterLyricsApiKey`, `musixmatchUserToken`) are sent only to `lyrics-api.boidu.dev` / `apic.musixmatch.com`, only in headers, never logged.
- User-Agent: default `NavidromeBetterLyricsPlugin/<version> (+https://github.com/ranokay/navidrome-better-lyrics-plugin)`; Musixmatch uses `Dalvik/2.1.0 (Linux; U; Android 10)`, Luna uses `com.luna.music/100198030 (Linux; U; Android 15)`.
- Provenance: `Source.Provider` = source display name, `Source.Format` = `ttml`|`elrc`|`lrc`|`plain`. This replaces today's constant `"ttml"`; tests that assert it are updated.
- LyricsPriority, local-source precedence, and README install steps do not change.
- Tests: table-driven, fake sender injected via provider constructor; run `mise exec -- go test -race ./plugin ./...` locally as needed; `mise run check` gates every push/PR.

## Review Focus

1. **Config saves that omit/empty the source list** (`sources: []`, array missing) — plugin must fall back to the full default order, never fetch with zero sources. Pinned by `TestReadProviderConfig_FallsBackToDefaults` (Task 2).
2. **A provider returning garbage** (HTML for a JSON endpoint, empty body, wrong bytes) — must degrade to a logged error and continue the chain, never panic; each source fetcher has a malformed-payload case (Tasks 11–21).
3. **LRCLib throttling** (429 body `{"name":"RateLimited"}`, 503 + Retry-After) — cooldown per source, no retry storm. Pinned by LRCLib cooldown test (Task 14) and sweep cooldown test (Task 6).
4. **LunaBeat index drift/growth** (index fetch fails, exceeds store cap, missing entry) — skip the source and continue the lookup. Pinned by LunaBeat fallback tests (Task 21).
5. **Track metadata edge inputs** (empty title/artist, zero duration) — zero network calls, same as today. Pinned by `TestLyricsProvider_SkipsIncompleteTrackMetadata` (Task 3).

---

### Task 1: Capture live wire fixtures

**Files:**
- Create: `plugin/testdata/binimum/search.json`, `plugin/testdata/binimum/lyrics.ttml`
- Create: `plugin/testdata/amll/search.json`, `plugin/testdata/amll/get.json`
- Create: `plugin/testdata/lunabeat/manifest.json`, `plugin/testdata/lunabeat/songs.json`, `plugin/testdata/lunabeat/song.ttml`
- Create: `plugin/testdata/lyrically/search.json`, `plugin/testdata/lyrically/lyrics.json`
- Create: `plugin/testdata/musixmatch/token.json`, `plugin/testdata/musixmatch/search.json`, `plugin/testdata/musixmatch/subtitles.json`
- Create: `plugin/testdata/netease/search.json`, `plugin/testdata/netease/lyric.json`
- Create: `plugin/testdata/luna/search.json`, `plugin/testdata/luna/detail.json`
- Create: `plugin/testdata/petitlyrics/word.xml`
- Create: `plugin/testdata/lyricify/resolve.json`, `plugin/testdata/lyricify/lyrics.json`
- Create: `plugin/testdata/lrclib/get.json`, `plugin/testdata/lrclib/search.json`
- Create: `plugin/testdata/betterlyrics/qq.txt`
- Create: `plugin/testdata/fixtures.md`

**Interfaces:**
- Produces: real-format fixtures consumed by Tasks 7–21 test steps.
- Consumes: known song "Live Forever" / Oasis / album "(What's the Story) Morning Glory?" (duration 280 s) or any well-covered song; keep one song reused across probes.

- [ ] **Step 1: Probe and save fixtures.** `curl -sS --max-time 10` each endpoint, truncating any response > 20 KB (keep the first lines; note truncation). Endpoints:
  - binimum: `https://lyrics-api.binimum.org/?track=Live%20Forever&artist=Oasis&duration=280` → search.json; then GET a result's `lyricsUrl` → lyrics.ttml
  - amll: `https://api.amll.dev/v1/lyrics/search?musicName=Live%20Forever&artistName=Oasis` → search.json; `https://api.amll.dev/v1/lyrics/get?id=<id>&format=ttml` → get.json
  - lunabeat: `https://2755337087.github.io/ttml-hub/api/v1/manifest.json`; `https://2755337087.github.io/ttml-hub/api/v1/songs.json`; one matched `path` → song.ttml
  - lyrically: `https://itunes.apple.com/search?term=Oasis%20Live%20Forever&media=music&entity=song&limit=5` → search.json; `https://lyrics.paxsenix.org/apple-music/lyrics?id=<trackId>` → lyrics.json
  - musixmatch: `https://apic.musixmatch.com/ws/1.1/token.get?app_id=android-player-v1.0&user_language=en&t=<hex>` (UA `Dalvik/2.1.0 (Linux; U; Android 10)`, header `Cookie: AWSELB=0; AWSELBCORS=0`) → token.json; `.../track.search?q_track=Live%20Forever&q_artist=Oasis&page_size=5&usertoken=<token>&format=json&app_id=android-player-v1.0` → search.json; `.../macro.subtitles.get?namespace=lyrics_richsynched&optional_calls=track.richsync&subtitle_format=lrc&track_id=<id>&f_subtitle_length_max_deviation=40&usertoken=<token>&format=json&app_id=android-player-v1.0` → subtitles.json. If the token endpoint challenges (`hint:"captcha"`), retry once after 60 s; if still blocked, save the error response and write search/subtitles fixtures by hand from the Morphe reference (`MorpheApp/morphe-patches` `MusixmatchProvider.java`) marking them `reconstructed`.
  - netease: `curl -X POST https://music.163.com/api/cloudsearch/pc -d "s=Oasis Live Forever&type=1&offset=0&limit=10"` → search.json; `https://music.163.com/api/song/lyric/v1?id=<id>&lv=-1&kv=-1&tv=-1&rv=-1&yv=-1` → lyric.json
  - luna: `https://api.qishui.com/luna/search/track?aid=386088&app_name=luna&version_code=100198030&package=com.luna.music&device_id=<20hex>&iid=<20hex>&q=Oasis%20Live%20Forever&cursor=0&count=20&_rticket=<epochms>` (UA `com.luna.music/100198030 (Linux; U; Android 15)`) → search.json; `https://beta-luna.douyin.com/luna/h5/seo_track?track_id=<id>&device_platform=web` (desktop Chrome UA) → detail.json. If geo-blocked, reconstruct both from `LunaProvider.java` and mark.
  - petitlyrics: `curl -X POST https://p0.petitlyrics.com/api/GetPetitLyricsData.php -d "clientAppId=p1110417&terminalType=10&lyricsType=3&key_title=Live%20Forever&key_artist=Oasis&key_album=(What's the Story) Morning Glory?"` → word.xml (decode base64 `lyricsData` before saving if the XML wrapper is outside it)
  - lyricify: `curl -X POST https://api.credits.fm/v1/resolve/track -H 'Content-Type: application/json' -d '{"name":"Live Forever","artist":"Oasis"}'` → resolve.json; `https://api.lyricify.app/lyrics/get/mobile/new?username=<random 8 letters>&isrc=<base64 of ISRC>` → lyrics.json
  - lrclib: `https://lrclib.net/api/get?track_name=Live%20Forever&artist_name=Oasis&duration=280` (UA set) → get.json; `https://lrclib.net/api/search?track_name=Live%20Forever&artist_name=Oasis` → search.json. If 429, wait 30 s and retry once, else reconstruct from LRCLIB's documented response.
  - better-lyrics QQ: `https://lyrics-api.boidu.dev/qq/getLyrics?s=Live%20Forever&a=Oasis` → qq.txt (401 means the query is uncached; retry with a very-popular song, e.g. `s=Hello&a=Adelle`… use `s=Shape of You&a=Ed Sheeran`).
- [ ] **Step 2: Write `plugin/testdata/fixtures.md`** — one line per file: captured live or `reconstructed`, capture date, truncation note.
- [ ] **Step 3: Verify** all fixture files exist (`ls plugin/testdata/**/*`) and fixtures.md has an entry per file; `git add plugin/testdata && git commit -m "test: capture lyrics provider wire fixtures"`.

---

### Task 2: Source registry and config reading

**Files:**
- Create: `plugin/sources.go`
- Create: `plugin/sources_test.go`
- Create: `plugin/config_host_wasip1.go`, `plugin/config_host_native.go`

**Interfaces:**
- Produces (used by Tasks 3–6, 11–21):
  - `type configLookup func(key string) (string, bool)`
  - `type lyricsSource struct { ID, Display string; Hosts []string; DefaultEnabled bool; Fetch func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, error) }` — `Fetch` may be nil here; Tasks 11–21 assign real fetchers (the field compiles as `func(*lyricsProvider, lyrics.TrackInfo, time.Time) ([]lyricsCandidate, error)`).
  - `var allSources = []lyricsSource{...}` in config order: `better-lyrics`, `unison`, `binimum`, `amll`, `luna-beat`, `apple-lyrically`, `better-lyrics-portato`, `musixmatch`, `netease`, `luna`, `petitlyrics`, `lrclib`, `better-lyrics-legato`, `lyricify` (display names: "Better Lyrics", "Unison", "BiniLyrics", "AMLL", "LunaBeat", "Apple Music (Lyrically)", "Better Lyrics Portato", "Musixmatch", "NetEase", "Soda (Luna)", "PetitLyrics", "LRCLib", "Better Lyrics Legato", "Lyricify"; `luna-beat` and `apple-lyrically` `DefaultEnabled: false`, all others true).
  - `type providerConfig struct { Sources []lyricsSource; BetterLyricsAPIKey, MusixmatchUserToken string }`
  - `func readProviderConfig(lookup configLookup) providerConfig` — reads `sources` JSON blob (`[{"id":..., "enabled":...}]`), preserves array order, drops unknown ids, appends missing catalog sources with default enabled state in catalog order, empty/missing/invalid → full default set; reads optional `betterLyricsApiKey`, `musixmatchUserToken`.
  - `func defaultConfigLookup() configLookup` — wasip1: `host.ConfigGet`; native: `func(string)(string,bool){return "",false}` (build-tag files, mirroring `cache_host_native.go`).
- Consumes: `lyricsCandidate` (`quality.go`), `lyrics.TrackInfo` (PDK), `host.ConfigGet` (PDK).

- [ ] **Step 1: Write failing tests** in `plugin/sources_test.go`:
  - `TestAllSources_CatalogOrderAndDefaults` — asserts 14 entries in the documented order, ids unique, `luna-beat`/`apple-lyrically` disabled by default, every host string in each `Hosts` field has no scheme.
  - `TestReadProviderConfig_PreservesOrderAndDropsUnknown` — lookup returns `[{"id":"lrclib","enabled":true},{"id":"bogus","enabled":true},{"id":"better-lyrics","enabled":false}]` → `Sources` = `[lrclib, better-lyrics]` in that order.
  - `TestReadProviderConfig_FallsBackToDefaults` — lookup returns `("", false)`, `"[]"`, `"not json"`, and `"[{\"id\":\"lrclib\"}]"` (missing enabled → treated as enabled=false) → every case yields the documented full default set (Review Focus #1) except the last (lrclib explicitly disabled + rest default).
  - `TestReadProviderConfig_ReadsSecrets` — lookup returns `{"betterLyricsApiKey":"k","musixmatchUserToken":"t"}` parsed into the struct fields.
- [ ] **Step 2: Run** `go test ./plugin -run TestReadProviderConfig -run TestAllSources -v` → FAIL (undefined: readProviderConfig).
- [ ] **Step 3: Implement** `plugin/sources.go` + the two build-tag lookup files. Keep the JSON blob read exactly as `host.ConfigGet("sources")` (documented runtime shape).
- [ ] **Step 4: Run** again → PASS all 4 tests.
- [ ] **Step 5: Commit** `git add plugin/sources.go plugin/sources_test.go plugin/config_host_wasip1.go plugin/config_host_native.go && git commit -m "feat: add lyrics source registry and plugin config reader"`.

---

### Task 3: Orchestrator rewrite — stage-1 walk over the registry

**Files:**
- Modify: `plugin/main.go` (replace `fetchLyrics`, rename `betterLyricsProvider`→`lyricsProvider`; add `plugin/httpclient.go` holding the moved helpers `sendRequest`, `providerResponseError`, `providerHTTPError`, `rateLimitResponseError`)
- Modify: `plugin/testhelpers_test.go`, `plugin/provider_test.go`, `plugin/fallback_test.go` (mechanical renames `newBetterLyricsProvider*` → `newLyricsProvider*`)
- Create: `plugin/task_host_wasip1.go`, `plugin/task_host_native.go`

**Interfaces:**
- Consumes: Task 2's `readProviderConfig`, `allSources`; existing `lyricsCandidate`, `preferLyricsCandidate`, `newLyricsCandidate`, cache (`cache.go`), `requestSender`.
- Produces:
  - `type lyricsProvider struct { send requestSender; cache lyricsCacheStore; now func() time.Time; configLookup configLookup; enqueueSweep func(lyrics.TrackInfo) error; sources []lyricsSource }` — `sources` is derived in the constructor via `readProviderConfig`; for source-walk tests add `func newLyricsProviderWithSources(send requestSender, cache lyricsCacheStore, now func() time.Time, sources []lyricsSource, enqueue func(lyrics.TrackInfo) error) *lyricsProvider` (bypasses config; stub `Fetch` funcs injected here).
  - `func newLyricsProviderWithDeps(send requestSender, cache lyricsCacheStore, now func() time.Time, lookup configLookup, enqueue func(lyrics.TrackInfo) error) *lyricsProvider`
  - `func newLyricsProvider(send requestSender) *lyricsProvider` — wires `defaultConfigLookup()`, `defaultSweepEnqueuer()`.
  - `func defaultSweepEnqueuer() func(lyrics.TrackInfo) error` — wasip1: marshal track, `host.TaskEnqueue("lyrics-sweep", payload)` (queue creation at `OnInit`, Task 6); native: return nil.
  - `var activeProvider *lyricsProvider` set in `init()` — `taskworker.Register(activeProvider)` hooks `OnTaskExecute` (Task 6 uses it).
  - Stage-1 walk in `fetchLyrics` replacing the currently hardcoded walk: read winner cache (Task 5 fingerprint contract, plain sha256-join no-op until Task 5), assemble best from fresh per-source caches, else walk the **first 3 enabled** sources (`fetch == nil` source → skipped) with an 8 s budget, syllable early-stop, syllable-timed result returns immediately (preserving today's behavior). Keep lookup-error joining and `lookupHealth` merge semantics.
  - Stage-1 budget above: `stageOneBudget = 8 * time.Second` replaces `lyricsLookupTimeout` for `fetchLyrics`; `lyricsLookupTimeout` is deleted.
- Tests updated: existing fallback tests keep behavior for the 3 currently-implemented sources (better-lyrics TTML tiered read, unison tiered read, kugou fallback); request counts for kugou-only cases unchanged.

- [ ] **Step 1: Write failing test** `TestLyricsProvider_SkipsIncompleteTrackMetadata` (Review Focus #5): track `{Title:"", Artist:"Artist"}` and `{Title:"Song", Artist:""}` → 0 requests, empty lyrics, nil error. Also `TestLyricsProvider_StageOneWalksFirstThreeEnabledSources`: three enabled stub sources return line/line/word candidates in order; provider stops after the syllable source and returns it.
- [ ] **Step 2: Run** `go test ./plugin -run TestLyricsProvider -v` → FAIL.
- [ ] **Step 3: Implement** the orchestrator: move `fetchBetterLyricsTTML`, `fetchUnisonLyrics`, `fetchKugouLyrics` bodies into new files `plugin/sources_betterlyrics.go` (TTML endpoint), `plugin/sources_unison.go`, `plugin/sources_kugou.go` — wired as `Fetch` fields on `allSources` entries `better-lyrics`, `unison`, `better-lyrics-legato` (source `Fetch` signature from Task 2; `better-lyrics-legato` keeps kugou-line-only semantics). `better-lyrics-portato`, `binimum` etc. get nil `Fetch` for now.
- [ ] **Step 4: Run** `go test ./plugin -v` → PASS (all renamed + new tests).
- [ ] **Step 5: Commit** `git add -A plugin && git commit -m "feat: walk sources from a registry in a bounded stage-one pass"`.

---

### Task 4: Enhanced-LRC quality grading

**Files:**
- Modify: `plugin/quality.go`, `plugin/quality_test.go`

**Interfaces:**
- Produces: in `newLyricsCandidate`, format `"elrc"` grades as `timingWord` if the text contains at least one enhanced word marker `<mm:ss(.mmm)?>` else line-timed if `lrcTimestampPattern` matches; `"lrc"` keeps today's line grading; `"plain"` → unsynced. `inspectEnhancedLRCWordTiming(text string) bool` exported for tests.
- Consumes: Task 2 `newLyricsCandidate` switch (extend the `case "lrc":` cluster to `case "lrc", "elrc":`).

- [ ] **Step 1: Write failing tests** (table in `quality_test.go`): elrc with `<00:12.34>` word marker → timingWord; word-less LRC under format `elrc` → timingLine; malformed `<...<` without timestamps → not word; format `lrc` unchanged → timingLine.
- [ ] **Step 2: Run** `go test ./plugin -run TestEnhancedLRC -v` → FAIL.
- [ ] **Step 3: Implement** the sniff: compile `enhancedWordPattern = regexp.MustCompile(`<\d{1,3}:\d{2}(?:[.:]\d{1,3})?>`)`; matched → timingWord.
- [ ] **Step 4: Run** → PASS; also `go test ./plugin` (existing lrc tests unchanged).
- [ ] **Step 5: Commit** `git add plugin/quality.go plugin/quality_test.go && git commit -m "feat: grade enhanced LRC word timing as word-synced"`.

---

### Task 5: Cache v4 — per-source entries, fingerprinted winner

**Files:**
- Modify: `plugin/cache.go`, `plugin/cache_test.go`

**Interfaces:**
- Produces (used by orchestrator, sweep, and every new source's walker path):
  - `func lyricsCacheKeyForSource(track lyrics.TrackInfo, sourceID string) (string, bool)` — `lyrics:v4:source:<id>:<sha256(title 0 artist 0 album 0 duration)>`
  - `func lyricsWinnerCacheKey(track lyrics.TrackInfo, sourceOrder []string) (string, bool)` — `lyrics:v4:winner:<sha256(<order>|<trackhash>)>` (order = id list joined with `,`).
  - `func (p *lyricsProvider) readCachedSourceCandidate(track, sourceID) (lyricsCandidate, bool)` / `cacheSourceCandidate(track, sourceID, candidate)` — envelope `{"quality":<int>,"response":...}` JSON; TTL 24 h when `quality >= timingLine`, 1 h unsynced.
  - `func (p *lyricsProvider) readCachedWinner(track, sourceOrder)` / `cacheWinner(track, sourceOrder, candidate)` — TTL 24 h for syllable, 1 h word/line, 5 min unsynced (shared 1 MB entry cap and storage-headroom guard).
- Consumes: existing `maxCachedLyricsEntryBytes`, `lyricsCacheReservedBytes`, `lyricsCacheMaxBytes`, usage-store guard.
- Note: old `lyrics:v3:` keys simply expire; no migration code.

- [ ] **Step 1: Write failing tests** in `cache_test.go`:
  - key stability: two providers with same track+order → same winner key; order change → different key.
  - TTL wiring: word candidate winner TTL == 3600; syllable == 86400; unsynced == 300; per-source line candidate TTL == 86400.
  - oversized per-source entry skipped (reuse existing cap test shape).
- [ ] **Step 2: Run** `go test ./plugin -run TestLyricsCache -v` → FAIL.
- [ ] **Step 3: Implement** the four functions + rewire `readCachedLyrics`/`cacheLyrics` names→ `readCachedWinnerTrack`-alike calls inside the orchestrator (Task 3's walker now calls these instead of the v3 track key).
- [ ] **Step 4: Run** `go test ./plugin` full → PASS.
- [ ] **Step 5: Commit** `git add plugin/cache.go plugin/cache_test.go && git commit -m "feat: per-source lyrics cache with fingerprinted winner key"`.

---

### Task 6: Background sweep task

**Files:**
- Modify: `plugin/main.go` (OnInit + OnTaskExecute methods), `plugin/task_host_wasip1.go`, `plugin/task_host_native.go`
- Create: `plugin/sweep_test.go`

**Interfaces:**
- Consumes: `taskworker.Register` (PDK lifecycle of exports; build tags keep tests building), `host.TaskCreateQueue`, `host.TaskEnqueue`; Task 5's per-source cache; `readProviderConfig`.
- Produces:
  - `func (p *lyricsProvider) OnInit() error` — `host.TaskCreateQueue("lyrics-sweep", host.QueueConfig{Concurrency: 1, MaxRetries: 0, BackoffMs: 0, DelayMs: 0, RetentionMs: 0})`, error swallowed to nil (task availability must not block lyrics).
  - `func (p *lyricsProvider) OnTaskExecute(req taskworker.TaskExecuteRequest) (string, error)` — unmarshals `{"track": <TrackInfo>}` from `req.Payload`; guard: only `req.QueueName == "lyrics-sweep"`.
  - `func (p *lyricsProvider) runSweep(track lyrics.TrackInfo) string` — for every enabled source (config order), skip if per-source cache is fresh or the source has a provider-wide cooldown active; call `Fetch`; on success cache each returned candidate per source; tolerate individual failures; returns `"swept n cached, m failed, s skipped"`. No wall-clock deadline; per-request 6 s via the existing per-request helper (`requestTimeoutMilliseconds` uses `time.Now()` deadline far in the future — pass `p.now().Add(24*time.Hour)`).
  - Cooldown key: `rate-limit:<sourceID>:global:v1` respected by `runSweep` via `activeCooldown` (generalization of `activeUnisonCooldown`).
  - Enqueue trigger (already in Task 3 orchestrator): after stage-1, when `best < timingSyllable` OR fewer than len(config.Sources) produced candidates → `p.enqueueSweep(track)`; a KV in-flight marker `sweep:<trackhash>` with TTL 5 min dedupes (set before enqueue; cleared by runSweep start — stale marker expiry is the cleanup).

- [ ] **Step 1: Write failing tests** in `sweep_test.go`:
  - `TestSweep_CachesEveryEnabledSourceResult` — 2 stub sources with candidates; runSweep caches both; return message mentions "2".
  - `TestSweep_SkipsFreshEntriesAndCooldownSources` — pre-seed per-source cache for source 1 and `rate-limit:src2:global:v1` cooldown for source 2; no requests made.
  - `TestSweep_ContinuesAfterSourceFailure` — source 1 errors, source 2 succeeds → source 2 still cached, no error.
  - `TestInit_CreatesQueueOnceAndFailsOpen` — `OnInit` on native stub returns nil.
  - Enqueue-dedupe: orchestrator with `enqueue` recorder — second GetLyrics with the miss outcome within TTL does not enqueue twice (in-flight marker on fake cache).
- [ ] **Step 2: Run** `go test ./plugin -run TestSweep -v` → FAIL.
- [ ] **Step 3: Implement** OnInit + OnTaskExecute + runSweep as above; register `lifecycle.Register(activeProvider)` and `taskworker.Register(activeProvider)` in `init()`.
- [ ] **Step 4: Run** `go test ./plugin` → PASS.
- [ ] **Step 5: Commit** `git add -A plugin && git commit -m "feat: background sweep task fills per-source lyrics cache"`.

---

### Task 7: K-family word-sync parser (KRC / QRC / YRC / Luna KRC)

**Files:**
- Create: `plugin/wordsync.go`, `plugin/wordsync_test.go`

**Interfaces:**
- Produces:
  - `type timedWord struct{ StartMs, EndMs int64; Text string }`
  - `type timedLine struct{ StartMs, EndMs int64; Words []timedWord }`
  - `func parseKFamilyLyrics(input string, absoluteWordTimes bool) ([]timedLine, error)` — line tags `[<lineStartMs>,<lineDurMs>]`; word tokens `(<wordMs>,<wordDurMs>,0)word`; `absoluteWordTimes=false` (KRC/QRC/Luna) treats word times as offsets accumulating from line start; `=true` (NetEase YRC) treats them as absolute ms. Skips metadata tags (`[ti:]`, `[ar:]`, `[al:]`, `[offset:]`, `[t...` translation lines). Word text is JSON-decoded (`\u`-escapes occur in YRC).
  - `func enhancedLRCFromTimedLines(lines []timedLine) string` — `[mm:ss.xx]line text` with embedded `<mm:ss.xx>word` markers; timestamps floor-rounded to centiseconds; no metadata tags.
- Consumes: fixed test fixtures from Task 1 (`betterlyrics/qq.txt`, `luna/detail.json` where relevant) plus inline samples from the Morphe parsers.

- [ ] **Step 1: Write failing tests** in `plugin/wordsync_test.go`:
  - relative KRC line `[10000,3000]<0,500,0>Hello <500,500,0>world` → line start 10 s, `Hello` 10.00–10.50, `world` 10.50–11.00.
  - absolute YRC line `[10000,3000](10200,500,0)Hey (10700,500,0)there` → word starts 10.20/10.70.
  - metadata/translation tags ignored; empty input errors; output contains `[00:10.00]<00:10.00>Hello <00:10.50>world`.
- [ ] **Step 2: Run** `go test ./plugin -run TestParseKFamily -v` → FAIL.
- [ ] **Step 3: Implement** the parser + emitter (regex-free scan is fine).
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `git add plugin/wordsync.go plugin/wordsync_test.go && git commit -m "feat: parse KRC/QRC/YRC word-synced lyrics to enhanced LRC"`.

---

### Task 8: Lyricify LYL/LYS parser

**Files:**
- Modify: `plugin/wordsync.go`, `plugin/wordsync_test.go`

**Interfaces:**
- Produces: `func parseLyricifyLYL(input string) ([]timedLine, error)` — header `[type:LyricifyLines]`; lines `[<startMs>,<durMs>]text`; syllable tokens `word(<startMs>,<durMs>)` inside a line (absolute ms); returns error on no timed lines.
- Consumes: Task 7's `timedLine`/emitter.

- [ ] **Step 1: Write failing tests**: `[type:LyricifyLines]\n[1000,2000]word(1000,500)Hello word(1500,500)world` → two word tokens, one line; offset mismatch tolerated; no header → error.
- [ ] **Step 2: Run** → FAIL; **Step 3: implement**; **Step 4: PASS**; **Step 5: Commit** `git commit -m "feat: parse Lyricify LYL lines to timed lines"` (`--` same files as Task 7).

---

### Task 9: Musixmatch richsync parser

**Files:**
- Modify: `plugin/wordsync.go`, `plugin/wordsync_test.go`

**Interfaces:**
- Produces: `func parseRichsync(body string) ([]timedLine, error)` — body is a JSON string of `[{"ts":<sec float>,"te":<sec float>,"l":[{"c":"text","o":<ms offset>}], ...}]`; word start = round(ts*1000)+o when o>0 else word-less timed line; missing word end → start+40 ms minimum as the reference implementation does.
- Consumes: fixture `plugin/testdata/musixmatch/subtitles.json` (from Task 1) — the parser takes only the `richsync_body` string; test drives it with a trimmed version inline plus the fixture under `t.Skip` if absent.

- [ ] **Step 1: Failing tests** (inline JSON: `"[{\"ts\":12.4,\"te\":13.9,\"l\":[{\"c\":\"He\",\"o\":0},{\"c\":\"llo\",\"o\":120}]}]"` → line at 12400, words 12400–12400+40, 12520+40/12520+…) plus no-words line falls back to line timing.
- [ ] **Steps 2–5:** run FAIL → implement → PASS → commit `feat: parse Musixmatch richsync to timed lines`.

---

### Task 10: PetitLyrics WSY parser + LRCLib lyricsfile YAML

**Files:**
- Modify: `plugin/wordsync.go`, `plugin/wordsync_test.go`
- Modify: `go.mod` (`go get gopkg.in/yaml.v3`, `go mod tidy`)

**Interfaces:**
- Produces:
  - `func parsePetitWSY(data []byte) ([]timedLine, error)` — XML `<wsy><line><linestring>text</linestring><word><starttime>ms</starttime><endtime>ms</endtime><wordstring>word</wordstring></word>…</line>…</wsy>`; error when no lines.
  - `func parseLRCLibLyricsFile(data []byte) ([]timedLine, error)` — YAML v1 dict: `lines:` list where each entry has `start_ms`, `end_ms`, `text` (words list optional with word ms) → timedLines; used only when LRCLib reports `hasWordSync: true`.
- Consumes: fixtures `plugin/testdata/petitlyrics/word.xml`, `plugin/testdata/lrclib/get.json`.

- [ ] **Step 1: Failing tests** driving both parsers off the real fixtures (assert first line and first word; assert `parseLRCLibLyricsFile` on a trimmed inline YAML too).
- [ ] **Steps 2–5:** FAIL → implement (yaml.v3 only here) → PASS → commit `feat: parse PetitLyrics WSY and LRCLib lyricsfile to timed lines`.

---

### Task 11: Sources — Better Lyrics Portato (QQ → word)

**Files:**
- Create: `plugin/sources_portato.go`, `plugin/sources_portato_test.go`
- Modify: `plugin/sources.go` (assign `Fetch` on the `better-lyrics-portato` entry)

**Interfaces:**
- Consumes: Task 7 (QRC), the shared `sendJSONRequest` helpers moved in Task 3; endpoint `https://lyrics-api.boidu.dev/qq/getLyrics?s=&a=&al=&d=` (metadata optional-tiers as for TTML).
- Produces: `fetchPortatoLyrics` — `200` + non-empty `{"lyrics": "<QRC >"}` → `parseKFamilyLyrics(…, false)` → `enhancedLRCFromTimedLines` → one `lyricsCandidate{provider: "Better Lyrics Portato", format: "elrc"}`; `401`/`404`/empty → clean miss; `429` → recorded under the shared better-lyrics cooldown keys (same host service); other errors joined. Honors the better-lyrics tier-broadening loop (album, then none).

- [ ] **Step 1: Failing test**: fake sender URL switch on `/qq/getLyrics` serving Task 1's `bundle betterlyrics/qq.txt` wrapped in `{"lyrics":...}` JSON → candidate quality `timingWord`, request count 1..3 per tier rules, 401 → retry broader + uncertain health.
- [ ] **Steps 2–5:** FAIL → implement → PASS → commit `feat: add Better Lyrics Portato (QQ word-synced) source`.

---

### Task 12: Source — BiniLyrics (binimum)

**Files:**
- Create: `plugin/sources_binimum.go`, `plugin/sources_binimum_test.go`
- Modify: `plugin/sources.go`

**Interfaces:**
- Consumes: endpoints `https://lyrics-api.binimum.org/?track=&artist=&album=&duration=`; response `{"results":[{...,"lyricsUrl","timing_type":"word"|"line"}]}`; second GET of `lyricsUrl` (307→`lrc.red` host handling is the host client's redirect; both hosts allowlisted in Task 22).
- Produces: `fetchBinimumLyrics` — best-scoring result (exact album+duration match first, then duration delta ≤2 s) → GET `lyricsUrl` → TTML candidate (`provider "BiniLyrics"`, format `"ttml"`, quality graded by root attr). Pass-through unchanged — **verified 2026-10-16: Navidrome's `model/lyrics_ttml.go` matches timing attributes by bare local name (`begin`/`end`/`timing`), so the custom `lrc:` namespace parses as-is; no rewrite.**
- [ ] **Step 1: Failing tests**: two-request flow with fixture search.json + lyrics.ttml → word-class candidate; no-matching-duration → clean miss; search 404/empty → miss; lyrics fetch 404 → miss.
- [ ] **Steps 2–5:** FAIL → implement → PASS → commit `feat: add BiniLyrics (binimum) source`.

---

### Task 13: Source — AMLL

**Files:**
- Create: `plugin/sources_amll.go`, `plugin/sources_amll_test.go`
- Modify: `plugin/sources.go`

**Interfaces:**
- Consumes: `https://api.amll.dev/v1/lyrics/search?musicName=&artistName=&albumName=` → `{"status":"ok","data":{"items":[{"id",…"appleMusicIds","isrcs"}]}}`; then `https://api.amll.dev/v1/lyrics/get?id=<id>&format=ttml` → `{"status":"ok","data":{…"lyrics":"<TTML>"}}`.
- Produces: `fetchAMLLyrics` — first item (fixtures show no score; keep deterministic order) → TTML candidate (`provider "AMLL"`, `"ttml"`); 4xx/missing/empty → misses; 429 → `rate-limit:amll:global:v1`.

- [ ] **Step 1: Failing tests** with fixtures: search+get → syllable/word-candidate graded from TTML root timing attr; 404 search → miss.
- [ ] **Steps 2–5:** FAIL → implement → PASS → commit `feat: add AMLL TTML source`.

---

### Task 14: Source — LRCLib

**Files:**
- Create: `plugin/sources_lrclib.go`, `plugin/sources_lrclib_test.go`
- Modify: `plugin/sources.go`

**Interfaces:**
- Consumes: `https://lrclib.net/api/get?track_name=&artist_name=&album_name=&duration=`; fallback `https://lrclib.net/api/search?track_name=&artist_name=` — pick candidate with min |duration − track.duration| ≤ 2 s across ≤5 entries; fields `syncedLyrics` (line LRC), `lyricsFile` (YAML, only when `hasWordSync`), `plainLyrics`. Task 10's YAML parser for lyricsFile.
- Produces: `fetchLRCLibLyrics` — prefer `lyricsFile` (→ elrc, quality word) over `syncedLyrics` (→ lrc, line) over `plainLyrics` (plain, unsynced); non-empty `duration` param required for `get`; on `get` miss run `search` once. Headers: plugin UA; `429`/`503` → cooldown `rate-limit:lrclib:global:v1` honoring `Retry-After` (default 30 s).

- [ ] **Step 1: Failing tests** (fixtures): get returns lyricsFile + syncedLyrics → elrc word candidate wins; get 404 then search hit within 2 s → synced LRC line candidate; all durations > 2 s off → miss; 429 → cooldown recorded, request not repeated in-sweep (uses Task 6 cooldown check).
- [ ] **Steps 2–5:** FAIL → implement → PASS → commit `feat: add LRCLib source`.

---

### Task 15: Source — Musixmatch

**Files:**
- Create: `plugin/sources_musixmatch.go`, `plugin/sources_musixmatch_test.go`
- Modify: `plugin/sources.go`

**Interfaces:**
- Consumes: `token.get?app_id=android-player-v1.0&user_language=en&t=<hex>` (UA `Dalvik/2.1.0 (Linux; U; Android 10)`, `Cookie: AWSELB=0; AWSELBCORS=0`) → `{"message":{"header":{"status_code":200},"body":{"user_token":"…"}}}` cached in KV `musixmatch:token:v1` (TTL 10 days, refreshed on 401); `track.search?q_track=&q_artist=&q_duration=&page_size=5&s_track_rating=desc&usertoken=&format=json&app_id=android-player-v1.0` → pick by duration delta ≤2 s then rating order; `macro.subtitles.get?namespace=lyrics_richsynched&optional_calls=track.richsync&subtitle_format=lrc&track_id=&f_subtitle_length_max_deviation=40&usertoken=…` → Task 9 parser on `richsync_body`; fallback `subtitle_body` (LRC → line).
- Produces: `fetchMusixmatchLyrics` — config `musixmatchUserToken` short-circuits minting (no `token.get` call); captcha (`hint:"captcha"`) or 401 → drop the KV token, skip attempt silently this lookup (no retry); candidates: richsync → elrc word; subtitles → lrc line; `lyrics_body` plain. 429 → `rate-limit:musixmatch:global:v1`.

- [ ] **Step 1: Failing tests**: mint-once-then-cache (fake cache asserts one token.get across two lookups); richsync path yields word elrc; captcha body → no lyrics + token dropped; config token → zero `token.get` requests.
- [ ] **Steps 2–5:** FAIL → implement → PASS → commit `feat: add Musixmatch source with token caching`.

---

### Task 16: Sources — NetEase, Luna (K-family), Lyricify

**Files:**
- Create: `plugin/sources_netease.go`, `plugin/sources_luna.go`, `plugin/sources_lyricify.go` (+ one `_test.go` each)
- Modify: `plugin/sources.go`

**Interfaces:**
- NetEase: POST `https://music.163.com/api/cloudsearch/pc` form `s=<title + " " + artist>&type=1&offset=0&limit=10` → `result.songs[{id,duration(ms)}]` pick |Δdur| ≤ 2 s; GET `https://music.163.com/api/song/lyric/v1?id=&lv=-1&kv=-1&tv=-1&rv=-1&yv=-1` → prefer `yrc.lyric` (Task 7, `absoluteWordTimes=true` → elrc word), else `lrc.lyric` (line, but strip `{"t":"…","c":[…]}` JSON-credit lines — drop any line whose first non-space char is `{`), else plain.
- Luna: GET `https://api.qishui.com/luna/search/track?aid=386088&app_name=luna&version_code=100198030&package=com.luna.music&device_id=<20 hex>&iid=<20 hex>&q=<title artist>&cursor=0&count=20&_rticket=<epochms>` (UA Luna) → `result_groups[].data[].entity.track{id,duration(ms)}` pick ≤2 s; GET `https://beta-luna.douyin.com/luna/h5/seo_track?track_id=<id>&device_platform=web` (desktop UA) → `lyric.type` `krc` → Task 7 relative parse → elrc word; `lrc` → line; else unsynced.
- Lyricify: POST `https://api.credits.fm/v1/resolve/track` JSON `{"name":<title>,"artist":<artist>}` (desktop UA, `Accept-Encoding: identity`) → `{isrc}`; GET `https://api.lyricify.app/lyrics/get/mobile/new?username=<5–12 rand lowercase>&isrc=<base64url(isrc)>` → `{text, offset(ms), isInstrumental}`; Task 8 LYL parse → subtract nothing, apply `offset` by adding ms to every timestamp before emission; elrc word candidate.

- [ ] **Step 1: Failing tests** per source (fixtures from Task 1): happy path asserts request shapes (method/headers/UA) and grading; every source: malformed JSON → clean degradation, no panic (Review Focus #2); NetEase duration miss → no lyric call.
- [ ] **Steps 2–5:** FAIL → implement all three → PASS → commit `feat: add NetEase, Luna and Lyricify sources`.

---

### Task 17: Sources — PetitLyrics, Lyrically (Apple Music mirror)

**Files:**
- Create: `plugin/sources_petitlyrics.go`, `plugin/sources_applelyrics.go` (+ tests)
- Modify: `plugin/sources.go`

**Interfaces:**
- PetitLyrics: POST `https://p0.petitlyrics.com/api/GetPetitLyricsData.php` form `clientAppId=p1110417&terminalType=10&lyricsType=3&key_title=&key_artist=&key_album=` → XML `<songs><song>` with `lyricsData` base64; decode → starts with `WSY` → Task 10 parser → elrc word; `LSY`/binary (`MHDR` prefix) → skip (no line decode this release); second call `lyricsType=1` → plain text → plain candidate.
- Lyrically: GET `https://itunes.apple.com/search?term=<title artist>&media=music&entity=song&limit=5` → `results[{trackId, trackTimeMillis}]` pick ≤2 s; GET `https://lyrics.paxsenix.org/apple-music/lyrics?id=<trackId>` → JSON with `elrc`/`elrcMultiPerson`/`ttml`/`plain` fields → prefer `elrc` else `ttml` else `plain`; NON-200 → miss.

- [ ] **Step 1: Failing tests** (fixtures): word path (WSY decode + parse), plain fallback tier 1; binary LSY placeholder → skip without error; Lyrically search→lyrics flow, duration miss → no second call.
- [ ] **Steps 2–5:** FAIL → implement → PASS → commit `feat: add PetitLyrics and Apple-via-Lyrically sources`.

---

### Task 18: Source — LunaBeat (static TTML hub)

**Files:**
- Create: `plugin/sources_lunabeat.go`, `plugin/sources_lunabeat_test.go`
- Modify: `plugin/sources.go`

**Interfaces:**
- Consumes: `https://2755337087.github.io/ttml-hub/api/v1/manifest.json` `{schemaVersion, revision}`; `https://2755337087.github.io/ttml-hub/api/v1/songs.json` (array of `{id,title,artists[],album,path,sha256}`); lyric GET at base + `path`.
- Produces: `fetchLunaBeatLyrics` — KV cache `lunabeat:songs:v1` = gzip(songs.json) + envelope `{"revision":…,"songs":[…]}` TTL 24 h; fetch flow: cached and revision matches manifest → use cache; else refetch songs.json (skip source when > `maxCachedLyricsEntryBytes` after gzip so the store stays healthy); match `title+artist` (NFKC-ish normalize: lowercase, strip punctuation/whitespace) against `songs` with album as tiebreak; → GET TTML file → ttml candidate. Any index failure → source skipped for this lookup (Review Focus #4).

- [ ] **Step 1: Failing tests**: cache round-trip (gzip envelope stored, second lookup zero index requests); revision change → refetch; oversize index → source skipped, lookup continues; match hit → song TTML candidate; match miss → clean miss.
- [ ] **Steps 2–5:** FAIL → implement → PASS → commit `feat: add LunaBeat community TTML source (disabled by default)`.

---

### Task 19: Manifest v0.2.0 — hosts, taskqueue, config schema

**Files:**
- Modify: `plugin/manifest.json`
- Modify: `plugin/plugin_test.go` (schema sanity assertions if needed)

**Interfaces:**
- Produces: `"version": "0.2.0"`; `"config"` block: schema (draft-07) `sources` array-of-objects (items `{id: enum of the 14 ids, enabled: bool}`, `default:` the catalog default order with `luna-beat`/`apple-lyrically` `enabled:false`), `betterLyricsApiKey`, `musixmatchUserToken` (strings); uiSchema `VerticalLayout` with `{"type":"Control","scope":"#/properties/sources","options":{"showSortButtons":true}}` and password-format controls on both secrets. `permissions.http.requiredHosts` = previously allowed hosts + `lrc.red`, `api.amll.dev`, `2755337087.github.io`, `lyrics.paxsenix.org`, `itunes.apple.com`, `apic.musixmatch.com`, `music.163.com`, `api.qishui.com`, `beta-luna.douyin.com`, `p0.petitlyrics.com`, `api.credits.fm`, `api.lyricify.app`, `lrclib.net`; `"permissions.taskqueue": {"reason":"Background sweep refreshes per-source lyric caches for recently played tracks","maxConcurrency":1}`.
- Consumes: manifest schema v0.63.2 ($schema line stays).
- Note: `Hosts` lists in `sources.go` entries must match this allowlist exactly — add a small test asserting every `allSources[i].Hosts` entry is present in the manifest's `requiredHosts` (read via `manifestJSON`).

- [ ] **Step 1: Write failing test** `TestManifestAllowlistsEverySourceHost` in `plugin_test.go`.
- [ ] **Step 2: Run** → FAIL; **Step 3: implement** manifest changes; **Step 4:** `go test ./plugin && mise run check` → PASS (wasm build + package check must stay green); **Step 5: Commit** `git add plugin/manifest.json plugin/plugin_test.go && git commit -m "feat: manifest config schema, taskqueue permission and v0.2.0 hosts"`.

---

### Task 20: README and docs

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-10-16-lyrics-sources-and-controls-design.md` (Status → approved; correct the boidu endpoint note: all three Better Lyrics endpoints stay on `lyrics-api.boidu.dev`, `api.betterlyrics.org` documented as the equivalent new-domain alias)

**Includes:**
- Source table (name / sync level / enabled by default / notes incl. unofficial flags).
- Config how-to: Settings → Plugins → Better Lyrics → the reorderable source table; defaults; two optional secrets (none is required; no key is issued today for Better Lyrics).
- Updated `Provider behavior` section: stage-1 fast pass + background sweep + per-source cache; the picker experience pending #6033.
- Privacy section: metadata now disclosed to every host in the allowlist (per-host one-liner); "unofficial endpoints can change without notice" warning; attribution lines for LRCLib, Unison, AMLL/LunaBeat (community), BiniLyrics, Musixmatch (copyright/backlink fields preserved in provenance notes), Lyrically mirror.
- Note that sources are only contacted for tracks with no local lyrics.
- [ ] **Step 1:** rewrite sections; **Step 2:** `mise run check` → PASS; **Step 3: Commit** `git add README.md docs/superpowers/specs/2026-10-16-lyrics-sources-and-controls-design.md && git commit -m "docs: document multi-source behavior, config and provider disclosures"`.

---

### Task 21: Final wrap-up — live smoke + branch review prep

**Files:** none new; updates to `plugin/*` only if smoke tests reveal defects.

- [ ] **Step 1:** `mise run check` (fmt + race tests + wasm build + package) → PASS from a clean tree.
- [ ] **Step 2:** Live smoke of one lookup per new source (script in the PR description or `docs/` note tracing which fixture endpoints returned data); spawn → record → `mise exec -- go test -race ./plugin -count=1` → PASS; record results in the PR body.
- [ ] **Step 3:** `git log --oneline` review; confirm every commit is atomic and green per the repo gate.
