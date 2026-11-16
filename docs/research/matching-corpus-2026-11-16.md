# Recording-matching corpus evidence — 2026-11-16

This is a small, labelled comparison corpus, not a population sample. It makes
no accuracy claim about providers, tracks, or users. The committed metadata
extracts and their provenance are in
`internal/lyric/testdata/matching/README.md`.

## Composition

| Measure | Count |
|---|---|
| Total cases | 16 |
| Provenance-backed cases (eligible for calibration) | 12 |
| Synthetic edge cases (excluded from calibration) | 4 |
| Expected validated | 6 |
| Expected ambiguous | 8 |
| Expected mismatch | 2 |

Categories: live/studio 5, clean/explicit 2, correct metadata drift 2,
ambiguous title 1, different duration 1, edit/remix close 1, cover 1,
missing metadata 2, instrumental identity 1.

## Wrong matches and misses

Two close-duration wrong recordings are represented by explicit markers:

- `Bad Guy (Live)` at 196 s against a 194 s studio request (2 s delta);
- `Mr Brightside (radio edit)` at 223 s against a 222 s request (1 s delta).

Duration alone would accept both; the version markers classify them
`ambiguous`, and neither is auto-playable or auto-save eligible. A markerless
45 s-shorter `bad guy` record is also `ambiguous`, not `mismatch`: duration
alone never proves a different recording.

The only mismatch cases are independent identity contradictions: a cover by a
different artist and explicitly conflicting live/studio markers. The corpus
contains no lyric-content miss used for calibration; empty/error instrumental
behavior is covered by deterministic provider tests, not this corpus.

## Timing tiers

The corpus itself is metadata-only. The deterministic provider behavior test
uses a word-timed ambiguous candidate against a line-timed validated candidate
and asserts the validated line recording wins. Existing parser/quality tests
cover unsynced, line, word and syllable tiers; timing richness does not enter
the identity classification.

## Deterministic latency and request count

The corpus/classifier tests use no network. The provider behavior tests walk
in-process stubs under a fixed clock and assert bounded request counts; the
ambiguous synchronized result does not stop the foreground walk before the
validated source. The plugin retains the existing three-source, two-second,
750 ms-per-source foreground bounds.

## Separately dated live observation

Captured 2026-11-16 with the existing bounded `scripts/provider-probe` command
(one query per source; no credentials):

| Source | Query | Elapsed | Provider replies | Formats |
|---|---|---|---|---|
| LRCLib | Live Forever / Oasis / Definitely Maybe / 277 s | 156 ms | 1 | elrc, lrc, plain |
| BiniLyrics | Live Forever / Oasis / Definitely Maybe / 277 s | 511 ms | 2 | ttml |
| LRCLib | Bad Guy / Billie Eilish / WHEN WE ALL FALL ASLEEP / 194 s | 155 ms | 1 | elrc, lrc, plain |

These are three single-request observations. They do not qualify provider
availability, latency distributions, or today's full provider contracts.
