# Lyrics provider wire fixtures

Probes run 2026-10-16 (UTC) by the implementation agent. A `derived` fixture keeps the
real response structure but is trimmed so files stay small and well-formed; a
`reconstructed` fixture was never fetched live — it is hand-written from the Morphe
reference parser (`MorpheApp/morphe-patches`,
`extensions/music/src/main/java/app/morphe/extension/music/patches/lyrics/requests/`)
and the provider's documented response format.

Notes that affect parser/shaper tests:

- binimum search may return `timing_type: "none"` for a live-match miss and matches by
  approximate duration — the morphe-style duration scoring is exercised in tests.
- The Better Lyrics `/qq/getLyrics` endpoint returns QRC envelopes whose `LyricContent`
  uses `[lineStart,lineDur]Word(wordStartMs,wordDurMs)` tuples with **absolute** word
  times (not QQ Music's raw `QrcInfos` byte format and not KuGou's `<off,dur,0>` relative
  tuples). Word-time tuples therefore come in two families:
  - `[s,d]<off,dur,0>word` relative — KRC (Luna `type:"krc"`, KuGou KRC), and
  - `[s,d]word(abs,dur)` / `[s,d]word(abs,dur,0)` absolute-paren — boidu QRC and NetEase YRC.
- netease lyric pulls for Live Forever / 晴天 / 稻香 / 告白气球 returned **no `yrc`** with
  the plain API on 2026-10-16; YRC parse tests use an inline reconstructed sample.
- musixmatch's anonymous minted token cannot fetch subtitles on 2026-10-16
  (`macro_calls` bodies all null — saved as `subtitles-null.json`); the richsync fixture
  is reconstructed. `word` unit field: `ts`/`te` seconds, `o` offset ms.
- lrclib `get` without `album_name` matched an unrelated instrumental release for
  "Live Forever / Oasis"; the fill fixture is "Shape of You / Ed Sheeran" which includes
  `lyricsfile` and `syncedLyrics`. Score tests therefore cover miss-on-duration.

| File | Status | Song / shape note |
|---|---|---|
| binimum/search.json | live | Live Forever / Oasis — several `timing_type` variants incl. `"none"` |
| binimum/lyrics.ttml | live | `lrc.red` TTML, `itunes`-shaped timing, custom `lrc:` namespace |
| amll/search.json | live | Shape of You / Ed Sheeran (Live Forever had 0 items) |
| amll/get.json | live | Word TTML; `lyrics` string truncated at 9 KB |
| lunabeat/manifest.json | live | `revision` header only |
| lunabeat/songs.json | derived | first 40 of 2783 entries, real header |
| lunabeat/song.ttml | live | "confidence" / Gareth.T — Apple-style `[itunes:timing="Word"]` |
| lyrically/search.json | live | iTunes search for Shape of You — 3 of 5 results kept |
| lyrically/lyrics.json | live-derived | `elrc`, `elrcMultiPerson`, `ttmlContent`, `content`, `plain`, `lrc` keys; truncated |
| musixmatch/token.json | live | `user_token` minted, OAuth app `android-player-v1.0` |
| musixmatch/search.json | live | Shape of You match, 5 candidates |
| musixmatch/subtitles-null.json | live | anonymous token: every macro body null (negative fixture) |
| musixmatch/subtitles.json | reconstructed | richsync frames per documented schema; used by Task tests |
| netease/search.json | live | Live Forever / Oasis, 3 of 10 results; `duration` ms |
| netease/lyric.json | live | line LRC with leading rich-JSON credit lines (`{"t":…,"c":[…]}`), no `yrc` for probed songs |
| luna/search.json | live | Live Forever / Oasis, 2 entries (huge real payload trimmed) |
| luna/detail.json | live | plaintext KRC in `lyric.content` (`type:"krc"`), 40 lines kept |
| petitlyrics/word.xml | live | decoded WSY bytes rebuilt as XML with 5 of 57 lines |
| lyricify/resolve.json | live | Shape of You ISRC `GBAHS1600463` (Live Forever resolve worked too but lyrics.json keys off Shape of You) |
| lyricify/lyrics.json | live | `[type:LyricifyLines]` line-level LYL |
| lrclib/get.json | live | Shape of You — `plainLyrics`, `syncedLyrics`, `lyricsfile`, `hasWordSync:false` |
| lrclib/search.json | live | Live Forever — 20 results |
| betterlyrics/qq.txt | live | boidu `/qq/getLyrics` JSON envelope with QRC `LyricContent` |
