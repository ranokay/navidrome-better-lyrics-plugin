package lyric

import (
	"encoding/xml"
	"regexp"
	"strings"
)

var lrcMetadataPattern = regexp.MustCompile(`(?im)^[\t ]*(?:\[(?:ar|al|ti|au|length|by|offset|re|ve|tool):[^\]\r\n]*\][\t ]*)+\r?$`)

// hasLyricContent reports whether a payload carries servable lyric text:
// tags and timing stamps alone do not count.
func hasLyricContent(value, format string) bool {
	if format == "ttml" {
		// Bodyless lyric fragments remain supported, but head/metadata text
		// never counts as lyrics, regardless of whether a body is present.
		decoder := xml.NewDecoder(strings.NewReader(value))
		anyText, bodyText, sawBody := false, false, false
		bodyDepth, metadataDepth := 0, 0
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			switch typed := token.(type) {
			case xml.StartElement:
				if metadataDepth > 0 || strings.EqualFold(typed.Name.Local, "head") || strings.EqualFold(typed.Name.Local, "metadata") {
					metadataDepth++
					continue
				}
				if strings.EqualFold(typed.Name.Local, "body") {
					sawBody = true
					bodyDepth++
				}
			case xml.EndElement:
				if metadataDepth > 0 {
					metadataDepth--
					continue
				}
				if strings.EqualFold(typed.Name.Local, "body") {
					bodyDepth--
				}
			case xml.CharData:
				if metadataDepth > 0 || strings.TrimSpace(string(typed)) == "" {
					continue
				}
				anyText = true
				if bodyDepth > 0 {
					bodyText = true
				}
			}
		}
		if sawBody {
			return bodyText
		}
		return anyText
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
	if hasLyricContent(value, format) {
		return false
	}
	if format == "ttml" {
		return xml.Unmarshal([]byte(value), new(xmlNode)) == nil
	}
	return true
}

// xmlNode is a permissive document root for the well-formedness check.
type xmlNode struct {
	XMLName  xml.Name
	Children []xmlNode `xml:",any"`
}
