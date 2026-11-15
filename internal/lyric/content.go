package lyric

import (
	"regexp"
	"strings"
)

var lrcMetadataPattern = regexp.MustCompile(`(?im)^[\t ]*(?:\[(?:ar|al|ti|au|length|by|offset|re|ve|tool):[^\]\r\n]*\][\t ]*)+\r?$`)

// hasLyricContent reports whether a payload carries servable lyric text:
// tags and timing stamps alone do not count.
func hasLyricContent(value, format string) bool {
	if len(value) > MaxDocumentBytes {
		return false
	}
	if format == "ttml" {
		_, content, valid := inspectTTMLTimingQuality(value)
		return valid && content
	}
	if format == "lrc" || format == "elrc" {
		value = lrcMetadataPattern.ReplaceAllString(value, "")
		value = lrcTimestampPattern.ReplaceAllString(value, " ")
		value = enhancedWordPattern.ReplaceAllString(value, " ")
	}
	return strings.TrimSpace(value) != ""
}

// EmptyDocument reports a payload with no lyric content that is still
// structurally well-formed (e.g. `<tt></tt>`): such documents are clean
// misses, whereas malformed XML degrades. TTML well-formedness is checked
// structurally; other formats treat parsed-but-contentless as empty.
func EmptyDocument(value, format string) bool {
	if len(value) > MaxDocumentBytes {
		return false
	}
	if format == "ttml" {
		_, content, valid := inspectTTMLTimingQuality(value)
		return valid && !content
	}
	return !hasLyricContent(value, format)
}
