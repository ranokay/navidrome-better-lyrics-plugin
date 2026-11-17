# Recording-matching corpus evidence — 2026-11-16

This is a small reference-labelled comparison corpus, not a population sample. It makes no accuracy claim about providers, tracks, or users. The
committed metadata extracts and their provenance are in
`internal/lyric/testdata/matching/README.md`.

## Composition (measured from the committed corpus)

| Measure | Count |
|---|---|
| Total cases | 17 |
| Provenance-backed cases | 13 |
| Synthetic edge cases (excluded from calibration) | 4 |
| Reference label `same` (all cases) | 7 |
| Reference label `different` (all cases) | 7 |
| Reference label `uncertain` (all cases) | 3 |
| Classifier `validated` | 6 |
| Classifier `ambiguous` | 9 |
| Classifier `mismatch` | 2 |
| Timing tier `line` | 13 |
| Timing tier `word` | 4 |

Categories include live/studio, edit/remix, radio edit, cover, clean/explicit,
missing metadata, ambiguous titles, instrumental identity and explicit marker
conflict. The added remix case is
`Mr. Brightside (The Lindbergh Palace radio edit)` at 249 s against a 222 s
studio request.

## Independent labels and measured outcomes

Reference labels were assigned by the implementer from the captured provider
metadata and release context. They are not human-reviewed annotations,
independent third-party ground truth, or population evidence. They are recorded
separately from the classifier's expected status so threshold checks do not
reuse the classifier's own prediction. Among provenance-backed cases:

- correct `same` deltas are 0–4.0 s; the 4.0 s case is deliberately expected
  `ambiguous` because the available metadata cannot confirm it;
- the smallest markerless `different` delta is 45 s;
- `wrongMatches` (reference label `different` but classifier `validated`) = **0**;
- `conservativeMisses` (reference label `same` but not auto-playable) = **1**, the
  correct 4.0 s drift case;
- synthetic cases are excluded from these calibration counts.

The deterministic public-provider corpus test drove all 17 cases through one
stub fetch each, then replayed through caches. It asserted: validated cases
play and are auto-save eligible; ambiguous cases never appear in v1, appear as
one non-save suggestion in v2/listing, and survive cache replay; mismatch cases
are absent; typed evidence survives; and the second public v2 call made no
additional source request. Measured in-process: **17 source requests for 17
cases, elapsed 6.4 ms** (`TestPublicMatchingCorpusBehavior`). These are harness
measurements, not provider latency.

Existing parser/quality tests cover unsynced, line, word and syllable tiers.
The corpus provider test uses word-timed ambiguous suggestions and line-timed
validated results; timing richness never changes the classification.

## Separately dated live observation

Captured 2026-11-16 with the bounded `scripts/provider-probe` command (one
query per source; no credentials):

| Source | Query | Elapsed | Provider replies | Formats |
|---|---|---|---|---|
| LRCLib | Live Forever / Oasis / Definitely Maybe / 277 s | 156 ms | 1 | elrc, lrc, plain |
| BiniLyrics | Live Forever / Oasis / Definitely Maybe / 277 s | 511 ms | 2 | ttml |
| LRCLib | Bad Guy / Billie Eilish / WHEN WE ALL FALL ASLEEP / 194 s | 155 ms | 1 | elrc, lrc, plain |

These are three single-request observations. They do not qualify provider
availability, latency distributions, or today's full provider contracts.
