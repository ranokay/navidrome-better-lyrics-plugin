package lyric

import (
	"encoding/xml"
	"strings"
)

// hasLyricContent reports whether a payload carries servable lyric text:
// tags and timing stamps alone do not count.
func hasLyricContent(value, format string) bool {
	if format == "ttml" {
		// Only text inside lyric-bearing nodes counts: metadata/head text in
		// an otherwise empty body must not pass. Documents
		// without a <body> fall back to any character data so bare lyric
		// roots keep working.
		decoder := xml.NewDecoder(strings.NewReader(value))
		anyText, bodyText, sawBody := false, false, false
		inBody := false
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			switch typed := token.(type) {
			case xml.StartElement:
				if strings.EqualFold(typed.Name.Local, "body") {
					sawBody, inBody = true, true
				}
			case xml.EndElement:
				if strings.EqualFold(typed.Name.Local, "body") {
					inBody = false
				}
			case xml.CharData:
				if strings.TrimSpace(string(typed)) == "" {
					continue
				}
				anyText = true
				if inBody {
					bodyText = true
				}
			}
		}
		if sawBody {
			return bodyText
		}
		return anyText
	}
	text := lrcTimestampPattern.ReplaceAllString(value, " ")
	text = enhancedWordPattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text) != ""
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
