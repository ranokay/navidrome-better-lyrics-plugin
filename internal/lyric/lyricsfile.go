package lyric

import (
	"fmt"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// ParseLRCLibLyricsFile parses LRCLib's YAML v1 lyricsfile payload (lines with
// optional word timings) into timed lines.
func ParseLRCLibLyricsFile(data []byte) ([]TimedLine, error) {
	var doc struct {
		Version string             `yaml:"version"`
		Lines   []lrclibLyricsLine `yaml:"lines"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode lyricsfile: %w", err)
	}
	var lines []TimedLine
	for _, row := range doc.Lines {
		if row.StartMs == 0 && row.EndMs == 0 && strings.TrimSpace(row.Text) == "" {
			continue
		}
		line := TimedLine{StartMs: row.StartMs, EndMs: row.EndMs, Text: row.Text}
		line.Text = row.Text
		for _, word := range row.Words {
			if word.StartMs == 0 && (word.Text == "" || word.EndMs == 0) {
				continue
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
// Tokens that cannot be located in the text fall back to single-space
// separation between unwrapped boundaries.
func lrclibWordSeparators(text string, words []TimedWord) []TimedWord {
	if len(words) == 0 {
		return words
	}
	cursor := 0
	aligned := true
	for i := range words {
		token := words[i].Text
		if token == "" {
			continue
		}
		if idx := strings.Index(text[cursor:], token); idx >= 0 {
			gap := text[cursor : cursor+idx]
			cursor += idx + len(token)
			if i == 0 {
				words[i].Text = gap + token
				continue
			}
			words[i-1].Text += gap
			continue
		}
		if i > 0 && !endsWithWhitespace(words[i-1].Text) && !startsWithWhitespace(token) {
			words[i-1].Text += " "
		}
		// One failed lookup forfeits the remainder append: the unaligned
		// token's text still lives in the remaining slice, so appending it
		// would duplicate the line.
		aligned = false
	}
	if aligned {
		if remaining := text[cursor:]; remaining != "" {
			words[len(words)-1].Text += remaining
		}
	}
	return words
}

func endsWithWhitespace(value string) bool {
	runes := []rune(value)
	return len(runes) > 0 && unicode.IsSpace(runes[len(runes)-1])
}

func startsWithWhitespace(value string) bool {
	runes := []rune(value)
	return len(runes) > 0 && unicode.IsSpace(runes[0])
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
