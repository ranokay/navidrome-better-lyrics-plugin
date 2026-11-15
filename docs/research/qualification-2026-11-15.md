# WASI host qualification protocol

Prepared on 2026-11-15 for audit issue
[#25](https://github.com/ranokay/navidrome-better-lyrics-plugin/issues/25).
Use the [reusable runner](../../scripts/host-qualification/README.md) to obtain a
fresh result for a candidate package. This document records the method and
assertions; the runner's output and gate receipt record the tested package hash,
host revision, exit status, and observed timings.

## Host and isolation

The reviewed host is Navidrome commit
`690a2343888467d5fbe1b22ba97fcb4ce571013c`, which contains the merged response-cache
change #6465. It is a current upstream checkout, rather than a server process
built from the v0.64.2 release tag.

The runner overlays one standalone Go test into that checkout's `plugins`
package. It loads the packaged `.ndp` through the real manifest loader, detects
capabilities, runs lifecycle initialization, and calls the exported lyrics
function through Extism/WASI. Every capability call creates a fresh WASM
instance. The host's real SQLite KV store, quota checks, task database, workers,
and callback dispatch remain active.

All data folders and compiled-WASM caches are temporary. The test substitutes a
deterministic HTTP transport after host loading and before the first lookup.
Requests retain their provider URLs and pass through the host permission and
timeout boundary, but the transport opens no network connections. Responses use
synthetic lyrics. No account, DNS, operating-system, production database, or
personal Navidrome configuration is changed.

The runner snapshots the package bytes before testing, so a concurrent rebuild
cannot change the artifact midway through the scenarios. It checks the host
checkout's Git status and tracked diff before and after execution. Only the
standalone test runs; Navidrome's Ginkgo suite setup does not rebuild its fixture
plugins.

## Run

After committing the candidate code and building its package:

```sh
mise run package-check
mise exec -- python3 scripts/host-qualification/run.py /path/to/navidrome dist/nd-better-lyrics.ndp
```

The host checkout requires its normal Go test dependencies and native SQLite
build prerequisites. The runner uses `-mod=readonly`, the host's `netgo` and
`sqlite_fts5` build tags, and a three-minute test deadline. No host source file is
written: the overlay fixture lives in this repository as `.go.txt`, outside the
plugin module's normal Go package discovery.

Capture the command through the standard gate receipt runner when producing
delivery evidence. Keep the package SHA-256 and exact host revision with that
result. Re-run after production changes or rebuilding a different candidate;
this protocol alone does not establish that a package passed.

## Assertions

| Scenario | Required observation |
|---|---|
| Load and first response | The actual package loads and returns synthetic synchronized lyrics within three seconds after loading. The measured interval includes the WASM capability call, not initial compilation. |
| Positive cross-instance cache | A subsequent capability call returns the same content without another Better Lyrics request. The real task worker also fetches tail-source coverage. |
| Explicit stored expiry | Expiring the source/winner envelopes while retaining host storage permits a fresh, lower-quality replacement. Clock-driven unit tests separately cover remaining-lifetime promotion and replay bounds. |
| Filtered continuation | A queued tail-only payload runs through SQLite dispatch and a fresh WASM callback, preserves its cached sibling, fetches the tail, and records full-order completion. |
| Negative cross-instance cache | Clean misses remain empty and do not repeat HTTP requests in another WASM instance. |
| Configuration and credentials | A new configured credential reauthorizes an anonymous miss. The header reaches Better Lyrics and stays absent from the Unison tail sweep. A fully disabled source list returns empty without HTTP, even with prior positive state. |
| Authenticated redirects | A keyed Better Lyrics request returns a redirect as an upstream failure instead of forwarding its header to another manifest-allowed host. |
| Upgrade and reload | Reload purges removed Musixmatch data, affected LyricsPlus/winner data, and completed markers, persists the purge marker, and retains unrelated state. |
| Quota and control capacity | The host grants 32,000,000 bytes. At that limit minus the reserved 1 MiB, lyric-miss writes stop, a cooldown still persists, and an over-quota direct host write is rejected. |
| Concurrent first use | Two calls return valid lyrics and leave a readable shared salt and usable cache. The run logs any follow-up source requests; it does not assume atomic KV claims. |
| Foreground timeout | Stalled transport calls honor cancellation. The foreground exits near its two-second allowance and below three seconds; background work is cancelled before scenario cleanup. |

The continuation scenario injects the filtered payload directly through the host
queue. Provider regressions separately exercise automatic continuation creation,
shrinking tails, repeated no-progress termination, and pending-slot release.
This avoids treating a short deterministic callback test as evidence for every
long-running provider combination.

## Runtime finding resolved by this work

The initial real-host run reproduced a quota-policy defect that native fake
stores had missed. Navidrome parses manifest `32MB` as 32,000,000 bytes, while the
plugin previously calculated its threshold using 32 binary megabytes. Its
reserved-space threshold therefore exceeded the actual host quota. The
regression fills the real store to `maxSize - 1 MiB` and checks that miss records
cannot consume the reserve. The correction aligns the application's capacity
constant with the existing decimal permission; it does not increase the grant.

## Evidence boundaries

This is direct plugin/host qualification. Calls bypass the core lyrics router,
so these timings do not measure browser paint, playback, Subsonic routing, or
#6465's five-minute response-cache behavior. Live provider wire probes are a
separate check. The inspected upstream host also lacks the fork's source-control
UI contract; provenance display and interactive source controls require a
compatible #5733/#6033 build and their own UI verification.

KV read-then-write claims and task enqueueing lack an atomic compare-and-set or
caller-supplied idempotency primitive in this host. The concurrent scenario
checks usable state and responses, rather than promising exactly one cold
request or proving all race interleavings. The per-source caches and serial task
worker limit repeated provider work; occasional duplicate cold work remains a
host coordination limitation.

## Public wire observations on 2026-11-15

The separate [provider probe](../../scripts/provider-probe/README.md) contacted
all sixteen sources with public metadata for Shape of You / Ed Sheeran /
÷ (Deluxe), 234 seconds. It recorded statuses, sizes and recognized field names,
without retaining or printing lyric bodies, credentials, or query strings.

| Sources | Observation in this bounded sample |
|---|---|
| Better Lyrics, Portato, NetEase, PetitLyrics, LRCLib, Genius | Returned usable lyrics. |
| Unison, BiniLyrics, AMLL, LunaBeat, Legato | Returned clean misses; this is not a coverage verdict for other songs. |
| Apple via Lyrically | iTunes succeeded; the lyrics mirror returned HTTP 502. |
| Lyricify | Resolve endpoint returned HTTP 503. |
| Luna | Search returned HTTP 200 with an empty body and degraded as intended. |
| LyricsPlus | Transport failed before an HTTP response was received. |
| YouTube Music | Initial healthy miss exposed a parser mismatch; the corrected filtered search completed search/watch/browse and returned lyrics in the follow-up probe. |

The YTM correction keeps the existing three-hop chain and strict duration gate.
Filtered search supplies duration in unlinked metadata runs and artist names in
`UC*` links; album `MPRE*` links cannot establish artist identity. Its minimized
fixture uses synthetic IDs and no lyric content. Both captured layout and the
filter were checked against the primary
[ytmusicapi parser](https://github.com/sigma67/ytmusicapi/blob/86fcd17ee3ff0d19bc807558d4e1a637df395143/ytmusicapi/parsers/songs.py).

These observations are dated service evidence. The four degraded services are
handled through existing failure/cooldown policies; one failed sample does not
justify removing a source, changing its default, or weakening identity checks.
Re-run the affected bounded probe when diagnosing a future report. Provider
availability remains independent of the deterministic host qualification.
