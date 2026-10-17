package lyric

import (
	"errors"
	"strings"
)

// ParseLyricifyLYL parses Lyricify LYL/LYS files: `[startMs,durMs]text` lines with
// optional inline `(startMs,durMs)` tokens for Apple-sourced syllable timing. The
// dialect matches the QRC bracket family (absolute parentheses, text before each
// tuple), so the shared parser does the work after the LYL header check.
func ParseLyricifyLYL(input string) ([]TimedLine, error) {
	if !strings.Contains(input, "[type:LyricifyLines]") {
		return nil, errors.New("input is not a Lyricify LYL/LYS file")
	}
	return ParseKFamily(QRC, input)
}
