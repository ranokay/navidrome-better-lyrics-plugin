package lyric

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ParseRichsync flattens Musixmatch's richsync payload (a JSON array of timed
// chunks: `ts`/`te` seconds, chunks in `l` with text `c` and millisecond `o`
// offsets) into timed lines. Word ends are synthesized as reference clients do:
// the next chunk's start, or `start + minDuration` when nothing bounds the chunk.
func ParseRichsync(body string) ([]TimedLine, error) {
	var chunks []richsyncChunk
	if err := json.Unmarshal([]byte(body), &chunks); err != nil {
		return nil, fmt.Errorf("decode richsync: %w", err)
	}
	var lines []TimedLine
	for _, chunk := range chunks {
		start := int64(math.Round(chunk.TS * 1000))
		end := int64(math.Round(chunk.TE * 1000))
		if end < start {
			end = start
		}
		line := TimedLine{StartMs: start, EndMs: end, Text: chunk.Text()}
		for k, token := range chunk.Tokens {
			// `o` is a fractional seconds offset relative to the chunk's ts:
			// decoding it as milliseconds shifted windows 1000x and fractional
			// values broke the whole decode.
			wordStart := start + int64(math.Round(token.Offset*1000))
			wordEnd := end
			if k+1 < len(chunk.Tokens) {
				wordEnd = start + int64(math.Round(chunk.Tokens[k+1].Offset*1000))
			}
			if wordEnd < wordStart+richsyncMinimumTokenMs {
				wordEnd = wordStart + richsyncMinimumTokenMs
			}
			// Musixmatch sometimes emits an empty-text token; dropping it
			// keeps the neighbouring words' windows intact. A whitespace token
			// stays: rendered word texts concatenate, so it carries the space.
			if token.Text == "" {
				continue
			}
			line.Words = append(line.Words, TimedWord{StartMs: wordStart, EndMs: wordEnd, Text: token.Text})
		}
		if strings.TrimSpace(line.Text) == "" && len(line.Words) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil, errEmptyWordSyncInput
	}
	return lines, nil
}

type richsyncChunk struct {
	TS     float64            `json:"ts"`
	TE     float64            `json:"te"`
	Tokens []richsyncWordJSON `json:"l"`
}

type richsyncWordJSON struct {
	Text   string  `json:"c"`
	Offset float64 `json:"o"`
}

const richsyncMinimumTokenMs = int64(40)

func (c *richsyncChunk) Text() string {
	var out strings.Builder
	for _, token := range c.Tokens {
		out.WriteString(token.Text)
	}
	return out.String()
}
