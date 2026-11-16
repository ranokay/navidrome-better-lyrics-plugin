# Recording-matching corpus provenance

Snapshot captured **2026-11-16** from bounded public LRCLib searches. Raw
responses are not committed because they contain lyric bodies; this corpus keeps
only the recording metadata used by the identity rule.

## Sources

| Capture | Query | Raw SHA-256 |
|---|---|---|
| `Bad_Guy-Billie_Eilish.json` | `q=Bad Guy Billie Eilish` via `https://lrclib.net/api/search` | `ca196870542c0f15a62c9d3c48317669d7d8a291bccdc7e777424ab73d05c1e0` |
| `Creep-Radiohead.json` | `q=Creep Radiohead` | `8abe465602f4d6dbada89dcb989dfe4e4d66b53c6e892b736383e37108048fa2` |
| `Hallelujah-Jeff_Buckley.json` | `q=Hallelujah Jeff Buckley` | `bb5f4e30565ade488f01a85d268448d15df24ce6c45e814451ccb0f7270f5ec0` |
| `Live_Forever-Oasis.json` | `q=Live Forever Oasis` | `b789a5940f01235f58be6ead11f29d031adb5460f2fe672e1b2214b09747caf0` |
| `Mr._Brightside-The_Killers.json` | `q=Mr. Brightside The Killers` | `188b8c47725d83af02d8a690067b422e6b1e368bd7a9649d54ce5b3da77e7838` |
| `Song_2-Blur.json` | `q=Song 2 Blur` | `8adeba6ecebfad7c40c9776cd50c587ddce77002ab4555fcc72d94f7239be038` |
| `hallelujah-cohen.json` | `q=Hallelujah Leonard Cohen` | `063404331e124efc91da7ad98b088af84ec002c5fbc807acabdf89506bc3fb8d` |

Additional live/studio and radio-edit rows were read from the same endpoint
with the queries recorded in `corpus.json`. Each case names its record IDs.

## Calibration use

Only cases without `"synthetic": true` may justify a numeric constant.
Synthetic cases exercise missing fields, contradictory markers and error paths;
they are deliberately excluded from threshold calibration. The corpus is a
small labelled comparison set, not a population sample, and this document makes
no accuracy claim beyond the listed cases.

## Evidence supporting the rule

- Correct recordings in this corpus vary by up to 4.0 s across releases
  (`Hallelujah`: 409 s and 413 s). The 2 s tight band is the existing LRCLib
  contract; the 5 s validation ceiling leaves room for that observed drift.
- A wrong live recording was observed only 2 s away (`Bad Guy (Live)`: 196 s
  vs 194 s), so duration alone cannot reject it; the explicit version marker
  does.
- A wrong radio edit was observed 1 s away (`Mr Brightside (radio edit)`:
  223 s vs 222 s); the marker, not duration, distinguishes it.
- A markerless wrong recording in this corpus is at least 45 s away
  (`bad guy`: 149 s vs 194 s), but duration alone is still classified as
  ambiguous rather than a proven mismatch. The rule does not claim that a
  45 s difference proves a different recording.
