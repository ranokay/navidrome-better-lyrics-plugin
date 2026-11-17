# Current contracts and follow-ups

Maintained by the plugin maintainers. Snapshot checked on 2026-11-15. Update this
record when an audit issue, dependency, supported host, or upstream prerequisite
changes. The README describes installation and provider behavior; this document
records ownership, compatibility holds, and the evidence still needed.

The audit fixes below are development-branch work awaiting delivery. The latest published
plugin remains v0.4.1. Local commits, passing tests, upstream merge, and a published
plugin release are separate states.

## Implementation sequence

The issue tracker owns acceptance criteria. Historical unchecked plans are not
additional work. Plugin maintainers own each row and update its status after
verification and delivery.

| Issue | State in this snapshot | Exit condition |
|---|---|---|
| [#19: cache freshness and control capacity](https://github.com/ranokay/navidrome-better-lyrics-plugin/issues/19) | Implemented locally, `698728d` | Aged-cache and near-capacity regressions pass; replay cannot renew fetched freshness. |
| [#20: usable payloads and text preservation](https://github.com/ranokay/navidrome-better-lyrics-plugin/issues/20) | Implemented locally, `6e78322`, `db95463` | Metadata-only lyrics are rejected, LRCLib text is preserved, malformed Lyrically data remains retryable, and equivalent YTM JSON behaves identically. |
| [#21: release and build simplification](https://github.com/ranokay/navidrome-better-lyrics-plugin/issues/21) | Implemented locally, `54e77cb` | Future manifest versions pass the version invariant, release tags match, and manual runs expose a package artifact. |
| [#22: dependency and schema updates](https://github.com/ranokay/navidrome-better-lyrics-plugin/issues/22) | Implemented locally, `773459e` | Native/race, vet, module, workflow, schema, and WASI package checks pass while provenance is preserved. |
| [#23: current documentation](https://github.com/ranokay/navidrome-better-lyrics-plugin/issues/23) | Implemented, `1ffd11d` | Archived plans are non-executable; current contracts, owners, and upstream exit conditions are explicit. |
| [#24: payload and timestamp bounds](https://github.com/ranokay/navidrome-better-lyrics-plugin/issues/24) | Implemented, `46634e1` | Reviewed host/application bounds prevent unbounded expansion and wrapped timestamps; captured fixtures and regressions pass. |
| [#25: runtime and provider qualification](https://github.com/ranokay/navidrome-better-lyrics-plugin/issues/25) | Reusable qualification and runtime corrections implemented; results are candidate-specific | Disposable-host commands and outcomes cover installation, KV quota/freshness, tasks/continuations, reload, config, credentials, and latency; refreshed wire evidence is dated and minimized. |

Run the full local gate and [WASI qualification protocol](research/qualification-2026-11-15.md)
for each candidate. A WASI build alone does not establish real host behavior or
current provider availability. A compatible
UI build is needed only for its additional UI/provenance checks, not for
released-host installation and task tests.

## Settled product contracts

- Keep the sixteen source IDs, ordering semantics, and defaults. LunaBeat,
  Apple Music via Lyrically, YouTube Music, and Genius are disabled by default.
  Intentional unofficial-provider support and mirror defaults are settled choices.
- Local sidecar and embedded lyrics retain precedence through `LyricsPriority`.
  An explicitly disabled source stays disabled. Empty/missing source config uses
  defaults; a saved list with every row disabled opts out of network fetching.
- Foreground lookup queries at most three enabled sources, with a two-second
  total budget and 750 ms per source, and stops at the first usable synchronized
  result. A sequential background sweep populates per-source caches.
  Unchanged deferred tails stop after two consecutive no-progress continuations
  and release their pending slot; a later lookup can retry. Cache-generation
  changes invalidate both completed and pending sweep coverage.
- The plugin already returns cached, source-tagged variants, winner first.
  Timing quality ranks syllable, word, line, then plain; configured order breaks
  ties. This work does not require a new plugin fetch-by-source endpoint.
- TTL expiry bounds fetched freshness. Changing credentials retains readable
  positive lyrics while allowing refresh. Keep upgrade purges for removed
  Musixmatch data and affected LyricsPlus/winner caches.
- The manifest's `32MB` quota means 32,000,000 bytes in Navidrome. Lyric writes
  reserve 1 MiB within that grant for control records. HTTP/documents are capped
  at 8 MiB, XML depth at 128, and expanded legacy LunaBeat indexes at 16 MiB.
  Invalid timestamp arithmetic degrades safely instead of wrapping cues.
- Preserve original TTML and convert supported proprietary word timing to
  enhanced LRC. PetitLyrics LSY/line-binary decoding is intentionally unsupported;
  word and plain tiers remain supported. It is not a parked implementation task.
- Navidrome owns player controls. Preserve the approved source selector,
  track/source-local timing offsets, and client-side download decisions.
  Server-side sidecar writes and retain-forever controls are outside the product
  contract, not unfinished plugin work. Keep static Unison attribution.

## Original documents and variant identities

The optional variant exports are additive; `nd_lyrics_get_lyrics` and its response
shape are unchanged, so older hosts and clients keep working. A compatible host
probes `nd_lyrics_get_lyrics_v2`, `nd_lyrics_get_variants` and
`nd_lyrics_get_original` and falls back to the required playback export when they
are absent. The companion host branch (unreleased, stacked on the provenance
branch) adds the capability and authenticated retrieval; released v0.64.2 hosts
and the separate #6465 response-cache work do not provide these endpoints.

- A variant identity is versioned and names source instance, provider recording,
  language, role and playback format. The instance is a keyed HMAC fingerprint
  with at least 128 bits of output; credentials never appear in an identity.
  A provider record id is suffixed with a content-revision digest of the native
  document; providers without a record id use the content digest as the address.
  A changed document therefore cannot silently match an earlier selection.
- Every candidate an adapter returns is kept in a bounded per-source bundle, not
  only the winner or one per-source entry. Playback v2 and the listing expose all
  of them with identities; truncation drops selections, which then report
  unavailable rather than being remapped.
- Native documents are captured before trimming, conversion, credit stripping or
  offsetting. Provenance, not the format name, decides whether bytes are an
  original: source-supplied ELRC/LRC is genuine, a reconstruction is not, and a
  provider conversion is reported separately. QQ QRC preserves the `QrcInfos` XML
  container; the outer boidu JSON envelope and the extracted `LyricContent` rows
  are not originals. LyricsPlus, YouTube Music and Genius expose only ordered JSON
  rows or extracted page text, so their originals are explicitly unavailable.
- Originals live in the plugin KV as separate entries keyed by the exact identity,
  under the existing 1 MiB entry and reserved-control-capacity guards. Their TTL
  equals the candidate's; an expired or evicted artifact triggers one bounded
  refetch of the same source and must match the identity exactly. Disabled
  sources, changed credentials/config and changed documents report unavailable.
- Fixture provenance and SHA-256 for the TTML, QRC and KRC native documents live in
  `internal/source/testdata/fixtures.md`; tests pin those hashes.
- `scripts/host-qualification/README.md` records the released-host baseline and the
  `--originals` runtime mode. The latter requires the companion host branch and
  exercises the real router, auth, WASI plugin, KV and exact native bytes; it is
  not released-host support.

## Recording identity and instrumental evidence

The optional playback-v2 and variant-listing exports carry an independent
recording match and typed instrumental evidence. The required
`nd_lyrics_get_lyrics` response shape is unchanged.

- Recording validity, timing richness and content state are separate axes.
  Validated requires exact base-title and artist identity plus an independent
  corroborator (duration inside the existing two-second band, an exact album, or
  agreeing explicit version markers). Assessed incomplete/contradictory data is
  ambiguous, never silently unassessed. Only sources that expose no recording
  metadata remain unassessed and legacy-playable.
- Duration outside the corpus-observed drift band is ambiguous, never a proven
  mismatch. Mismatch requires an independent contradiction such as a different
  artist or conflicting explicit version markers. Correct recordings can differ
  by up to 4.0 s across releases; wrong live/radio-edit recordings were observed
  within 2 s, so duration alone cannot validate or reject them.
- Ambiguous entries are listed as individually addressable suggestions but are
  never served as playback winner, automatic selection or automatic-save
  eligible. Automatic saving is a future host slice; `autoSaveEligible` is true
  only for validated identity.
- Instrumental evidence is a typed, attributed response-level observation. Empty,
  malformed and error responses never establish it. It never vetoes valid lyrics;
  the UI shows lyrics and an attributed conflict note. Evidence about an
  ambiguous/mismatching recording is not presented as a claim about this track.
- The v5 lyric cache namespace invalidates only the previous plugin-owned
  disposable lyric cache (`lyrics:v4:`) once. Identity salts, credentials,
  configuration and durable records are untouched.

The companion host branch for this slice is stacked on the unreleased
`lyrics-source-provenance` branch (base `8ccdc78`) and is not released-host or
#6465 support. Corpus evidence and provenance:
`docs/research/matching-corpus-2026-11-16.md` and
`internal/lyric/testdata/matching/README.md`.

## Upstream dependencies

The latest released Navidrome is [v0.64.2](https://github.com/navidrome/navidrome/releases/tag/v0.64.2).
The following states were checked live on 2026-11-15; recheck before changing a
compatibility claim or replacing the PDK.

| Dependency | Verified state | Owner and exit condition |
|---|---|---|
| [#5733: responsive sidebar](https://github.com/navidrome/navidrome/pull/5733) | Open, unmerged, head `7905e1f` | Navidrome maintainers/PR author own delivery. Plugin maintainers update the UI requirement after a released host includes it and UI qualification passes. |
| [#6033: provenance and controls](https://github.com/navidrome/navidrome/pull/6033) | Open, unmerged, head `a730db2a` | Navidrome maintainers/PR author own the host/API/UI. Replace the fork only when an upstream PDK retains both response-level and per-entry source metadata and released/compatible host checks pass. |
| [#6032: earlier coalescing proposal](https://github.com/navidrome/navidrome/pull/6032) | Closed without merge | Historical reference only. No pending work depends on this PR. Use #6465 for the actual host behavior. |
| [#6465: plugin response cache/coalescing](https://github.com/navidrome/navidrome/pull/6465) | Merged 2026-11-15; absent from v0.64.2 | Navidrome maintainers own release delivery. Plugin maintainers qualify builds containing it under #25 and update release-specific documentation when shipped. |

Builds containing #6465 cache the entire plugin response per plugin/track for
five minutes, including misses and errors. They coalesce concurrent lookups
with a one-minute timeout. A poll during that cache lifetime does not call the
plugin, so it cannot reveal new sweep results or immediately apply source and
credential changes. The old five-second/sixty-second progressive-fill plan is
not the current contract for this route.

The baseline is eventual refresh on a request after host-cache expiry. Prompt
progressive discovery requires an explicit upstream refresh/invalidation
contract and verification of that route against a running host. Carry that
dependency through #6033 and #25 rather than adding another plugin cache or
promising that client polling bypasses the host cache.

## Dependencies and upgrades

Plugin maintainers own tool/module/schema updates in #22 and subsequent
maintenance changes. Version declarations in `go.mod`, `go.sum`, `mise.toml`,
and workflows remain the source of truth. The reviewed 2026-11-15 choices are:

| Dependency | Choice and reason | Condition for changing the choice |
|---|---|---|
| Go | 1.27.4, current stable security/bugfix release | A newer stable toolchain passes native/race and standard Go WASI package checks. |
| Extism Go PDK | v1.1.3, latest stable | A newer release preserves the host boundary and passes the same checks. |
| Navidrome PDK | Explicit provenance fork revision `a730db2a`, current #6033 branch head | Upstream provides the same optional source fields. The fork's default `@latest` currently omits them, so it is not a compatible replacement. |
| YAML | `go.yaml.in/yaml/v4` v4.0.0 for the plugin parser | New releases preserve supported lyricsfile fixtures, safe error behavior, and one-document validation. |
| Schema validator | `github.com/santhosh-tekuri/jsonschema/v6` v6.0.3, test-only | New versions preserve offline host/config/default validation. |
| Host manifest schema | Immutable v0.64.2 snapshot in `plugin/testdata` | A reviewed released schema changes. The current upstream and provenance-branch schemas are byte-identical to this snapshot; refresh its provenance and validation together when needed. |
| Config schema | JSON Schema Draft 7 as consumed by the host | Change only with demonstrated host/JSONForms support; using the manifest schema's newer dialect here is not an automatic upgrade. |
| Workflow tooling | mise-action v5.2.8, checkout v7.0.1, upload-artifact v7.0.2, all SHA-pinned; actionlint 1.7.12 and shellcheck 0.11.0 in the gate | Review newer release notes and update the applicable pins together with successful workflow checks. Binaryen was unused and removed. |
| Optional PR reviews | `ranokay/agent-workflows/.github/workflows/pr-agent.yml` pinned to `d043a48972b66ca7f4de448ab7cdda263bbf7f50`; PR-Agent v0.50.0 uses OpenCode Go / DeepSeek V4.1 Flash and the `PR_AGENT_OPENCODE_KEY` Actions secret | Plugin maintainers review shared workflow and implementation pins before updates. Keep local reviews and tests required; provider availability is optional. Qualify review/suggestion publishing and maintainer-only comment routing before approving rollout beyond the pilot. |
| Remaining indirect modules | Latest compatible versions for retained module paths | Keep graph overrides only where they advance retained dependencies. Remove redundant pins with tidy; adding an unused new major does not upgrade its old-major consumer. |
| Provider services | Existing catalog hosts and disclosure/default policy | Maintainers refresh bounded wire evidence under #25. Change or remove a source for a verified contract/policy failure with its matching config/cache migration. |

The original `gopkg.in/yaml.v3` path remains in dependency tests at its latest
version, v3.0.1; it no longer parses plugin input. Maintained YAML v3 in other
dependency tests is v3.0.6. These separate module paths cannot be consolidated
by changing this plugin's import alone. Their removal depends on their consumers
upgrading or disappearing from the retained graph.

YAML v4 uses `Load`, which rejects extra YAML documents. Unknown provider fields
remain accepted. This is a deliberate stricter boundary than legacy `Unmarshal`;
preserve its regression tests on future upgrades. Upstream's
[migration guide](https://github.com/yaml/go-yaml/blob/v4.0.0/docs/v3-to-v4-migration.md)
documents the distinction and node-hook compatibility.

For a dependency update, review primary release notes, update retained imports
and pins, tidy, inspect the resulting graph for compatible updates, and run
`mise run check`. Keep the provenance replacement until its exit condition is
met. A rollback restores the reviewed code/imports, module files, tool/workflow
pins, and schema snapshot together in a follow-up change; restoring only a
module version after an API migration is insufficient. These upgrades do not
introduce a persistent cache-schema migration.

### Advisory dispositions

The initial audit produced version-level matches, not a demonstrated exploit:

| Advisory | Disposition in the updated branch |
|---|---|
| [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809), `crypto/x509.Verify` | Fixed by Go 1.27.4; the fix first shipped in 1.27.3. |
| [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859), server `MaxBytesReader` race | Fixed by Go 1.27.4. |
| [GO-2026-6860](https://pkg.go.dev/vuln/GO-2026-6860), `x/net/html.Parse` quadratic work | The affected x/net module was removed by graph cleanup. Its fixed version is v0.62.0; `x/net/html` was not imported by the plugin WASI build. |
| [GO-2026-6866](https://pkg.go.dev/vuln/GO-2026-6866), `x/net/html.Render` raw-text namespace handling | Same removal disposition; fixed upstream in v0.62.0. |

The update checked sixteen retained module/toolchain versions against OSV and
returned zero matches. This is version-inventory evidence, not a successful
`govulncheck` call-graph analysis or a proof that the software has no vulnerabilities.
The embedded YouTube Music web-client key is an intentionally public provider
identifier; the owner explicitly accepted that single scanner detection.
That exception does not apply to user-supplied Better Lyrics credentials.

## Historical evidence

The four documents under `superpowers/plans` and `superpowers/specs` are archived
snapshots. Their commands, personal checkout paths, missing skill references,
and unchecked boxes are not current execution instructions. Preserve their
settled decisions without treating superseded budgets, source counts, or
prerequisites as implementation requirements.

[Feasibility research](research/lyrics-plugin-feasibility.md) records the initial
2026-08-23 investigation. [Latency research](research/latency-2026-10-18.md) and its
raw measurements record one 2026-10-18 host/build cohort. They do not qualify
today's provider endpoints, browser paint, playback, or #6465 cache behavior.
Fresh evidence and any concrete unavailable-environment next action belong to
#25. Test fixtures are development inputs, excluded from `.ndp` packages, and
their capture/synthetic provenance remains alongside the fixture files.
The [2026-11-15 qualification protocol and wire observations](research/qualification-2026-11-15.md)
describe the current harness and the repaired YTM song-search contract.
