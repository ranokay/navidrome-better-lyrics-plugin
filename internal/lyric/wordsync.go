package lyric

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// TimedWord is one word with its millisecond window.
type TimedWord struct {
	StartMs int64
	EndMs   int64
	Text    string
}

// TimedLine is one displayed line with its window, word timings, and plain text
// for lines that carry no word tuples.
type TimedLine struct {
	StartMs int64
	EndMs   int64
	Text    string
	Words   []TimedWord
}

var errEmptyWordSyncInput = errors.New("no timed lyrics found in input")

// WordSyncMode selects the bracketed word-sync dialect to parse. The three
// upstream services differ in tuple delimiter, relative/absolute timestamps, and
// the side of the tuple the timed text sits on:
//
//	KRC — `[start,dur]<off,dur,0>word`; cumulative offsets relative to the
//	      line start (Kugou KRC and the plain text KRC served by Luna).
//	QRC — `[start,dur]word(wordStart,dur[,…])`; absolute milliseconds with
//	      the text before each tuple (boidu QQ `LyricContent`).
//	YRC — `[start,dur](wordStart,dur,0)word`; absolute milliseconds with
//	      the text after each tuple (NetEase YRC).
type WordSyncMode uint8

const (
	KRC WordSyncMode = iota
	QRC
	YRC
)

// ParseKFamily parses the bracketed word-sync formats into timed lines.
// Metadata and translation rows are skipped; lines without word tuples keep their
// whole text as a wordless line.
func ParseKFamily(mode WordSyncMode, input string) ([]TimedLine, error) {
	if len(input) > MaxDocumentBytes {
		return nil, errDocumentTooLarge
	}
	var lines []TimedLine
	offset, err := kFamilyGlobalOffset(input)
	if err != nil {
		return nil, err
	}
	for raw := range strings.SplitSeq(input, "\n") {
		lineStart, lineDur, body, ok, err := parseBracketLine(strings.TrimRight(raw, "\r"))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		lineEnd, err := addMillis(lineStart, lineDur)
		if err != nil {
			return nil, err
		}
		words, lineText, err := collectWords(body, mode, lineStart)
		if err != nil {
			return nil, err
		}
		if len(words) == 0 && lineText == "" {
			continue
		}
		lines = append(lines, TimedLine{StartMs: lineStart, EndMs: lineEnd, Text: lineText, Words: words})
	}
	if err := shiftTimedLines(lines, offset); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errEmptyWordSyncInput
	}
	return lines, nil
}

// kFamilyGlobalOffset scans metadata rows for the last `[offset:±ms]` tag: KRC,
// QRC, and YRC payloads use it to resynchronise every timing window, and the
// bracket parser would otherwise discard it silently.
func kFamilyGlobalOffset(input string) (int64, error) {
	var offset int64
	for raw := range strings.SplitSeq(input, "\n") {
		row := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if !strings.HasPrefix(row, "[offset:") || !strings.HasSuffix(row, "]") {
			continue
		}
		value := strings.TrimSpace(row[len("[offset:") : len(row)-1])
		ms, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, errors.New("invalid lyric timing offset")
		}
		offset = ms
	}
	return offset, nil
}

// shiftTimedLines applies a global offset to every line and word window,
// clamping negative stamps to zero the way enhanced-LRC stamps require.
func shiftTimedLines(lines []TimedLine, offset int64) error {
	if offset == 0 {
		return nil
	}
	shift := func(ms int64) (int64, error) {
		if offset > 0 && ms > math.MaxInt64-offset {
			return 0, errTimestampRange
		}
		// Input timestamps are nonnegative, so adding a negative signed offset
		// cannot underflow. Preserve the existing zero-clamping behavior.
		return max(int64(0), ms+offset), nil
	}
	for i := range lines {
		var err error
		lines[i].StartMs, err = shift(lines[i].StartMs)
		if err != nil {
			return err
		}
		lines[i].EndMs, err = shift(lines[i].EndMs)
		if err != nil {
			return err
		}
		for j := range lines[i].Words {
			lines[i].Words[j].StartMs, err = shift(lines[i].Words[j].StartMs)
			if err != nil {
				return err
			}
			lines[i].Words[j].EndMs, err = shift(lines[i].Words[j].EndMs)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// parseBracketLine splits `[start,dur]` headers from row bodies. Rows without two
// numeric header fields (metadata `[ti:]`, translations, blanks) are not lyric rows.
func parseBracketLine(row string) (startMs, duration int64, body string, ok bool, err error) {
	if !strings.HasPrefix(row, "[") {
		return 0, 0, "", false, nil
	}
	sep := strings.IndexByte(row, ']')
	if sep < 0 {
		return 0, 0, "", false, nil
	}
	header := row[1:sep]
	fields := strings.Split(header, ",")
	if len(fields) != 2 || !numericTimestampField(fields[0]) || !numericTimestampField(fields[1]) {
		return 0, 0, "", false, nil
	}
	start, errStart := parseMillis(fields[0])
	durationMs, errDuration := parseMillis(fields[1])
	if errStart != nil || errDuration != nil {
		return 0, 0, "", false, errors.New("invalid line timing")
	}
	return start, durationMs, row[sep+1:], true, nil
}

func parseMillis(value string) (int64, error) {
	if !timestampDigits(value) {
		return 0, errors.New("timestamp is not an unsigned integer")
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, errTimestampRange
	}
	return ms, nil
}

func timestampDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func numericTimestampField(value string) bool {
	if len(value) > 0 && (value[0] == '+' || value[0] == '-') {
		value = value[1:]
	}
	return timestampDigits(value)
}

var errTimestampRange = errors.New("lyric timestamp exceeds supported range")

func addMillis(start, duration int64) (int64, error) {
	if start < 0 || duration < 0 || start > math.MaxInt64-duration {
		return 0, errTimestampRange
	}
	return start + duration, nil
}

func validTimingWindow(start, end int64) bool {
	// A missing end remains supported for formats that only carry a start.
	return start >= 0 && end >= 0 && (end == 0 || end >= start)
}

// wordTuple is one scanned `off,dur` pair.
type wordTuple struct {
	offset   int64
	duration int64
}

// tupleDelimiters reports the delimiters and text position for the mode.
func tupleDelimiters(mode WordSyncMode) (open, closeCh byte, relative, textAfter bool) {
	switch mode {
	case KRC:
		return '<', '>', true, true
	case YRC:
		return '(', ')', false, true
	default:
		return '(', ')', false, false
	}
}

// collectWords scans one line body into timed words using the mode's dialect.
func collectWords(body string, mode WordSyncMode, lineStartMs int64) ([]TimedWord, string, error) {
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
			startField, durationField := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
			offset, errOffset := parseMillis(startField)
			duration, errDuration := parseMillis(durationField)
			if numericTimestampField(startField) && numericTimestampField(durationField) &&
				(errOffset != nil || errDuration != nil) {
				return nil, "", errors.New("invalid word timing tuple")
			}
			// A zero duration is a legal tuple: NetEase's YRC marks
			// punctuation that way, and treating it as plain text leaks the
			// raw tuple into the rendered lyric.
			if errOffset == nil && errDuration == nil {
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
		return nil, text, nil
	}

	words := make([]TimedWord, 0, len(tuples))
	// QRC marks space runs as their own timed tokens; dropping them would render
	// "Shapeof", so only the QRC dialect keeps whitespace-only tokens.
	addWord := func(startMs, endMs int64, text string) {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" && (mode != QRC || text == "") {
			return
		}
		words = append(words, TimedWord{StartMs: startMs, EndMs: endMs, Text: text})
	}
	for k, tuple := range tuples {
		start := tuple.offset
		var err error
		if relative {
			start, err = addMillis(lineStartMs, tuple.offset)
			if err != nil {
				return nil, "", err
			}
		} else if start < lineStartMs {
			start = lineStartMs
		}
		end, err := addMillis(start, tuple.duration)
		if err != nil {
			return nil, "", err
		}

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
		return nil, "", nil
	}
	return words, "", nil
}

// EnhancedLRC renders parsed lines as enhanced LRC compatible with
// Navidrome's LRC parser: `[mm:ss.xx]` line stamps and `<mm:ss.xx>` word markers.
func EnhancedLRC(lines []TimedLine) string {
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(bracketLRCStamp(line.StartMs))
		for _, word := range line.Words {
			out.WriteString(inlineLRCStamp(word.StartMs))
			out.WriteString(word.Text)
		}
		if len(line.Words) == 0 {
			// Wordless lines keep their whole text; word-bearing lines already
			// rendered each token above, so appending Text again would duplicate it.
			out.WriteString(line.Text)
		} else {
			// The final empty marker closes the last word before any following
			// silence; the next line's start is not the word's end.
			last := line.Words[len(line.Words)-1]
			out.WriteString(inlineLRCStamp(max(last.StartMs, last.EndMs)))
		}
		out.WriteByte('\n')
	}
	return out.String()
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
