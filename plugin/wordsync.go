package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"strings"

	"gopkg.in/yaml.v3"
)

// timedWord is one word with its millisecond window.
type timedWord struct {
	StartMs int64
	EndMs   int64
	Text    string
}

// timedLine is one displayed line with its window, word timings, and plain text
// for lines that carry no word tuples.
type timedLine struct {
	StartMs int64
	EndMs   int64
	Text    string
	Words   []timedWord
}

var errEmptyWordSyncInput = errors.New("no timed lyrics found in input")

var errNoLRCLibPayload = errors.New("no usable LRCLib lyrics payload")

// wordSyncMode selects the bracketed word-sync dialect to parse. The three
// upstream services differ in tuple delimiter, relative/absolute timestamps, and
// the side of the tuple the timed text sits on:
//
//	wordSyncKRC — `[start,dur]<off,dur,0>word`; cumulative offsets relative to the
//	              line start (Kugou KRC and the plain text KRC served by Luna).
//	wordSyncQRC — `[start,dur]word(wordStart,dur[,…])`; absolute milliseconds with
//	              the text before each tuple (boidu QQ `LyricContent`).
//	wordSyncYRC — `[start,dur](wordStart,dur,0)word`; absolute milliseconds with
//	              the text after each tuple (NetEase YRC).
type wordSyncMode uint8

const (
	wordSyncKRC wordSyncMode = iota
	wordSyncQRC
	wordSyncYRC
)

// parseKFamilyLyrics parses the bracketed word-sync formats into timed lines.
// Metadata and translation rows are skipped; lines without word tuples keep their
// whole text as a wordless line.
func parseKFamilyLyrics(mode wordSyncMode, input string) ([]timedLine, error) {
	var lines []timedLine
	for _, raw := range strings.Split(input, "\n") {
		lineStart, lineDur, body, ok := parseBracketLine(strings.TrimRight(raw, "\r"))
		if !ok {
			continue
		}
		words, lineText := collectWords(body, mode, lineStart, lineStart+lineDur)
		if len(words) == 0 && lineText == "" {
			continue
		}
		lines = append(lines, timedLine{StartMs: lineStart, EndMs: lineStart + lineDur, Text: lineText, Words: words})
	}
	if len(lines) == 0 {
		return nil, errEmptyWordSyncInput
	}
	return lines, nil
}

// parseBracketLine splits `[start,dur]` headers from row bodies. Rows without two
// numeric header fields (metadata `[ti:]`, translations, blanks) are not lyric rows.
func parseBracketLine(row string) (startMs, duration int64, body string, ok bool) {
	if !strings.HasPrefix(row, "[") {
		return 0, 0, "", false
	}
	sep := strings.IndexByte(row, ']')
	if sep < 0 {
		return 0, 0, "", false
	}
	header := row[1:sep]
	fields := strings.Split(header, ",")
	if len(fields) != 2 {
		return 0, 0, "", false
	}
	start, errStart := parseMillis(fields[0])
	durationMs, errDuration := parseMillis(fields[1])
	if errStart != nil || errDuration != nil {
		return 0, 0, "", false
	}
	return start, durationMs, row[sep+1:], true
}

func parseMillis(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("empty timestamp field")
	}
	var ms int64
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("timestamp %q is not numeric", value)
		}
		ms = ms*10 + int64(digit-'0')
	}
	return ms, nil
}

// wordTuple is one scanned `off,dur` pair.
type wordTuple struct {
	offset   int64
	duration int64
}

// tupleDelimiters reports the delimiters and text position for the mode.
func tupleDelimiters(mode wordSyncMode) (open, closeCh byte, relative, textAfter bool) {
	switch mode {
	case wordSyncKRC:
		return '<', '>', true, true
	case wordSyncYRC:
		return '(', ')', false, true
	default:
		return '(', ')', false, false
	}
}

// collectWords scans one line body into timed words using the mode's dialect.
func collectWords(body string, mode wordSyncMode, lineStartMs, lineEndMs int64) ([]timedWord, string) {
	open, closeCh, relative, textAfter := tupleDelimiters(mode)

	var (
		tuples  []wordTuple
		chunks  []string
		pending strings.Builder
	)
	flushIntro := func() string {
		text := pending.String()
		pending.Reset()
		return text
	}
	for i := 0; i < len(body); {
		if body[i] != open {
			pending.WriteByte(body[i])
			i++
			continue
		}
		relEnd := strings.IndexByte(body[i:], closeCh)
		if relEnd < 0 {
			pending.WriteString(body[i:])
			break
		}
		spanEnd := i + relEnd + 1
		fields := strings.Split(body[i+1:i+relEnd], ",")
		if len(fields) >= 2 {
			offset, errOffset := parseMillis(strings.TrimSpace(fields[0]))
			duration, errDuration := parseMillis(strings.TrimSpace(fields[1]))
			if errOffset == nil && errDuration == nil && duration > 0 {
				chunks = append(chunks, flushIntro())
				tuples = append(tuples, wordTuple{offset: offset, duration: duration})
				i = spanEnd
				continue
			}
		}
		pending.WriteString(body[i:spanEnd])
		i = spanEnd
	}
	chunks = append(chunks, flushIntro())

	if len(tuples) == 0 {
		text := strings.TrimSpace(body)
		return nil, text
	}

	words := make([]timedWord, 0, len(tuples))
	addWord := func(startMs, endMs int64, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		words = append(words, timedWord{StartMs: startMs, EndMs: endMs, Text: text})
	}
	for k, tuple := range tuples {
		start := tuple.offset
		if relative {
			start = lineStartMs + tuple.offset
		} else if start < lineStartMs {
			start = lineStartMs
		}
		end := start + tuple.duration

		if textAfter {
			addWord(start, end, chunks[k+1])
			continue
		}
		text := chunks[k]
		if k == len(tuples)-1 {
			text += chunks[k+1]
		}
		addWord(start, end, text)
	}
	if len(words) == 0 {
		return nil, ""
	}
	return words, ""
}

// enhancedLRCFromTimedLines renders parsed lines as enhanced LRC compatible with
// Navidrome's LRC parser: `[mm:ss.xx]` line stamps and `<mm:ss.xx>` word markers.
func enhancedLRCFromTimedLines(lines []timedLine) string {
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(bracketLRCStamp(line.StartMs))
		for _, word := range line.Words {
			out.WriteString(inlineLRCStamp(word.StartMs))
			out.WriteString(word.Text)
		}
		out.WriteString(line.Text)
		out.WriteByte('\n')
	}
	return out.String()
}

// parseRichsync flattens Musixmatch's richsync payload (a JSON array of timed
// chunks: `ts`/`te` seconds, chunks in `l` with text `c` and millisecond `o`
// offsets) into timed lines. Word ends are synthesized as reference clients do:
// the next chunk's start, or `start + minDuration` when nothing bounds the chunk.
func parseRichsync(body string) ([]timedLine, error) {
	var chunks []richsyncChunk
	if err := json.Unmarshal([]byte(body), &chunks); err != nil {
		return nil, fmt.Errorf("decode richsync: %w", err)
	}
	var lines []timedLine
	for _, chunk := range chunks {
		start := int64(math.Round(chunk.TS * 1000))
		end := int64(math.Round(chunk.TE * 1000))
		if end < start {
			end = start
		}
		line := timedLine{StartMs: start, EndMs: end, Text: chunk.Text()}
		for k, token := range chunk.Tokens {
			wordStart := start + token.Offset
			wordEnd := end
			if k+1 < len(chunk.Tokens) {
				wordEnd = start + chunk.Tokens[k+1].Offset
			}
			if wordEnd < wordStart+richsyncMinimumTokenMs {
				wordEnd = wordStart + richsyncMinimumTokenMs
			}
			if strings.TrimSpace(token.Text) == "" {
				continue
			}
			line.Words = append(line.Words, timedWord{StartMs: wordStart, EndMs: wordEnd, Text: token.Text})
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
	Text   string `json:"c"`
	Offset int64  `json:"o"`
}

const richsyncMinimumTokenMs = int64(40)

func (c *richsyncChunk) Text() string {
	var out strings.Builder
	for _, token := range c.Tokens {
		out.WriteString(token.Text)
	}
	return out.String()
}

// parsePetitWSY parses PetitLyrics' word-synced WSY payload (plain XML with
// per-line and per-word millisecond windows).
func parsePetitWSY(data []byte) ([]timedLine, error) {
	var doc petitWSYDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode WSY: %w", err)
	}
	lines := make([]timedLine, 0, len(doc.Lines))
	for _, row := range doc.Lines {
		line := timedLine{
			StartMs: row.FirstStart(),
			EndMs:   row.LastEnd(),
			Text:    row.Text,
		}
		for _, word := range row.Words {
			if word.StartMs == 0 && word.EndMs == 0 {
				continue
			}
			line.Words = append(line.Words, timedWord{StartMs: word.StartMs, EndMs: word.EndMs, Text: word.Text})
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

type petitWSYDocument struct {
	Lines []petitWSYLine `xml:"line"`
}

type petitWSYLine struct {
	Text  string         `xml:"linestring"`
	Words []petitWSYWord `xml:"word"`
}

type petitWSYWord struct {
	StartMs int64  `xml:"starttime"`
	EndMs   int64  `xml:"endtime"`
	Text    string `xml:"wordstring"`
}

func (l petitWSYLine) FirstStart() int64 {
	for _, word := range l.Words {
		return word.StartMs
	}
	return 0
}

func (l petitWSYLine) LastEnd() int64 {
	if len(l.Words) == 0 {
		return 0
	}
	return l.Words[len(l.Words)-1].EndMs
}

// parseLRCLibLyricsFile parses LRCLib's YAML v1 lyricsfile payload (lines with
// optional word timings) into timed lines.
func parseLRCLibLyricsFile(data []byte) ([]timedLine, error) {
	var doc struct {
		Version string             `yaml:"version"`
		Lines   []lrclibLyricsLine `yaml:"lines"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode lyricsfile: %w", err)
	}
	var lines []timedLine
	for _, row := range doc.Lines {
		if row.StartMs == 0 && row.EndMs == 0 && strings.TrimSpace(row.Text) == "" {
			continue
		}
		line := timedLine{StartMs: row.StartMs, EndMs: row.EndMs, Text: row.Text}
		line.Text = row.Text
		for _, word := range row.Words {
			if word.StartMs == 0 && (word.Text == "" || word.EndMs == 0) {
				continue
			}
			line.Words = append(line.Words, timedWord{StartMs: word.StartMs, EndMs: word.EndMs, Text: word.Text})
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil, errEmptyWordSyncInput
	}
	return lines, nil
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

// parseLyricifyLYL parses Lyricify LYL/LYS files: `[startMs,durMs]text` lines with
// optional inline `(startMs,durMs)` tokens for Apple-sourced syllable timing. The
// dialect matches the QRC bracket family (absolute parentheses, text before each
// tuple), so the shared parser does the work after the LYL header check.
func parseLyricifyLYL(input string) ([]timedLine, error) {
	if !strings.Contains(input, "[type:LyricifyLines]") {
		return nil, errors.New("input is not a Lyricify LYL/LYS file")
	}
	return parseKFamilyLyrics(wordSyncQRC, input)
}

// bracketLRCStamp renders `[mm:ss.xx]`, flooring milliseconds to centiseconds.
func bracketLRCStamp(ms int64) string {
	return lrcStamp(ms, '[', ']')
}

func inlineLRCStamp(ms int64) string {
	return lrcStamp(ms, '<', '>')
}

func lrcStamp(ms int64, open, closeCh byte) string {
	if ms < 0 {
		ms = 0
	}
	cs := ms / 10
	seconds := cs / 100
	minutes := seconds / 60
	seconds %= 60
	cs %= 100
	return fmt.Sprintf("%c%02d:%02d.%02d%c", open, minutes, seconds, cs, closeCh)
}
