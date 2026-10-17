package lyric

import (
	"encoding/xml"
	"io"
	"regexp"
	"strings"
)

var lrcTimestampPattern = regexp.MustCompile(`(?m)^\s*(?:\[[0-9]{1,3}:[0-9]{2}(?:[.:][0-9]{1,3})?\])+`)

// enhancedWordPattern matches the inline word markers of enhanced LRC (LRCLib,
// Karaoke effects and converted word-synced sources).
var enhancedWordPattern = regexp.MustCompile(`<\d{1,3}:\d{2}(?:[.:]\d{1,3})?>`)

// ttmlTimingQuality inspects timing metadata without rewriting the TTML. Invalid
// XML is treated as unsynchronized so malformed provider data cannot outrank a
// valid fallback.
func ttmlTimingQuality(input string) TimingQuality {
	quality, valid := inspectTTMLTimingQuality(input)
	if !valid {
		return TimingUnsynced
	}
	return quality
}

func inspectTTMLTimingQuality(input string) (TimingQuality, bool) {
	decoder := xml.NewDecoder(strings.NewReader(input))
	quality := TimingUnsynced
	insideLine := false
	seenRoot := false
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				return quality, seenRoot
			}
			return TimingUnsynced, false
		}
		switch element := token.(type) {
		case xml.EndElement:
			if element.Name.Local == "p" {
				insideLine = false
			}
			continue
		case xml.StartElement:
			start := element
			if start.Name.Local == "tt" {
				seenRoot = true
			}
			if start.Name.Local == "p" {
				insideLine = true
			}

			if start.Name.Local == "tt" {
				for _, attribute := range start.Attr {
					if attribute.Name.Local != "timing" {
						continue
					}
					switch strings.ToLower(strings.TrimSpace(attribute.Value)) {
					case "syllable":
						quality = TimingSyllable
					case "word":
						if quality < TimingWord {
							quality = TimingWord
						}
					case "line":
						if quality < TimingLine {
							quality = TimingLine
						}
					}
				}
			}

			if quality < TimingLine && start.Name.Local == "p" && hasTimingAttribute(start) {
				quality = TimingLine
			}
			if quality < TimingWord && insideLine && start.Name.Local == "span" && hasTimingAttribute(start) {
				quality = TimingWord
			}
		default:
			continue
		}
	}
}

func hasTimingAttribute(element xml.StartElement) bool {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == "begin" && strings.TrimSpace(attribute.Value) != "" {
			return true
		}
	}
	return false
}
