package lyric

import (
	"fmt"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v4"
)

// ParseLRCLibLyricsFile parses LRCLib's YAML v1 lyricsfile payload (lines with
// optional word timings) into timed lines.
func ParseLRCLibLyricsFile(data []byte) ([]TimedLine, error) {
	if len(data) > MaxDocumentBytes {
		return nil, errDocumentTooLarge
	}
	var doc struct {
		Version string             `yaml:"version"`
		Lines   []lrclibLyricsLine `yaml:"lines"`
	}
	if err := yaml.Load(data, &doc); err != nil {
		return nil, fmt.Errorf("decode lyricsfile: %w", err)
	}
	var lines []TimedLine
	for _, row := range doc.Lines {
		if !validTimingWindow(row.StartMs, row.EndMs) {
			return nil, fmt.Errorf("invalid lyricsfile line timing")
		}
		if row.StartMs == 0 && row.EndMs == 0 && strings.TrimSpace(row.Text) == "" {
			continue
		}
		line := TimedLine{StartMs: row.StartMs, EndMs: row.EndMs, Text: row.Text}
		for _, word := range row.Words {
			if word.StartMs == 0 && (word.Text == "" || word.EndMs == 0) {
				continue
			}
			if !validTimingWindow(word.StartMs, word.EndMs) {
				return nil, fmt.Errorf("invalid lyricsfile word timing")
			}
			line.Words = append(line.Words, TimedWord{StartMs: word.StartMs, EndMs: word.EndMs, Text: word.Text})
		}
		// LRCLib stores the spacing between words only in the line text, so
		// token texts interleave the separators to avoid fused renders
		// ("firstline" for "first line") without repeating the whole line.
		line.Words = lrclibWordSeparators(row.Text, line.Words)
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil, errEmptyWordSyncInput
	}
	return lines, nil
}

// lrclibWordSeparators realigns word tokens against the line text: LRCLib's
// word tokens carry only their own text while the separators live in
// `text`, so rendering straight from the tokens fuses the words. Each gap
// between consecutive token spans becomes trailing text of the preceding
// token; leading and trailing text outside the spans is preserved as well.
// Mismatched or omitted words discard word timing so the original line text
// and line timing remain intact instead of inventing or duplicating lyrics.
func lrclibWordSeparators(text string, words []TimedWord) []TimedWord {
	if len(words) == 0 {
		return words
	}
	cursor := 0
	aligned := make([]TimedWord, 0, len(words))
	for _, word := range words {
		token := word.Text
		if token == "" {
			continue
		}
		idx := strings.Index(text[cursor:], token)
		if idx < 0 {
			return nil
		}
		gap := text[cursor : cursor+idx]
		if strings.ContainsFunc(gap, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
			return nil
		}
		cursor += idx + len(token)
		if len(aligned) == 0 {
			word.Text = gap + token
		} else {
			aligned[len(aligned)-1].Text += gap
		}
		aligned = append(aligned, word)
	}
	remaining := text[cursor:]
	if strings.ContainsFunc(remaining, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) || len(aligned) == 0 {
		return nil
	}
	aligned[len(aligned)-1].Text += remaining
	return aligned
}

type lrclibLyricsLine struct {
	StartMs int64              `yaml:"start_ms"`
	EndMs   int64              `yaml:"end_ms"`
	Text    string             `yaml:"text"`
	Words   []lrclibLyricsWord `yaml:"words"`
}

type lrclibLyricsWord struct {
	Text    string `yaml:"text"`
	StartMs int64  `yaml:"start_ms"`
	EndMs   int64  `yaml:"end_ms"`
}
