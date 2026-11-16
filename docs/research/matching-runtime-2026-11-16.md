# Matching and instrumental runtime receipt — 2026-11-16

This is a local runtime observation, not released-host support. It uses the
changed WASI plugin package at SHA-256
`99564a6430bc3a4f1e7e9baf6c2308bc7e81ee81d23998a2f5f22217487f6cc1`,
the host checkout based on `8ccdc785a2d20ce2775c1a0896ac9b902e132c8a`, and a
disposable database/library under `/private/.../opencode/navidrome-runtime`.

## Combined path

- The production `navidrome` binary was built from the changed host checkout
  with `make build` (embedded production UI).
- The changed `.ndp` was installed in the plugin folder and loaded through the
  real WASI loader; the host log shows `Loaded plugin manifest="ND Better
  Lyrics" capabilities="[Lyrics Lifecycle TaskWorker]"`.
- The real Subsonic router served `getLyricsBySongId?enhanced=true`. A real
  provider result from Better Lyrics supplied 97 word-timed lines.
- A host test plugin (`test-lyrics-originals`, shipped test fixture, not part of
  the production plugin) supplemented one validated identity, one ambiguous
  suggestion and one attributed instrumental assertion. This is supplemental
  UI state, not the production-plugin proof.
- Chromium driven by Playwright opened the real UI, played the scanned track,
  and opened the lyrics sidebar. The sidebar rendered 97 line groups and the
  source menu listed 11 entries including `Native Provider — Suggestion`.
- The ambiguous suggestion was not auto-selected. Selecting it explicitly
  changed the sidebar to its single `line one` entry, proving the existing
  selection path still works for a suggestion.
- With valid lyrics shown, the sidebar displayed the attributed note:
  “LRCLib reports an instrumental recording; the lyrics shown are from the
  selected source.” The assertion did not hide lyric content.
- The enhanced API response separately carried `instrumentalEvidence` and
  per-entry `match`/`autoSaveEligible`; no line-less lyric entry was created.

Screenshots are scratch evidence at
`/private/var/folders/m0/5z6zglhn7jd4vm967sf2wydr0000gn/T/opencode/navidrome-runtime/final-ui.png`
and `final-ui-selected.png`; they are not committed.

This observation does not qualify released hosts, #6465 response caching,
provider availability, or browsers other than the local headless Chromium run.
