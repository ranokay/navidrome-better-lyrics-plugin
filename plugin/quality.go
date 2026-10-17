package main

import (
	"encoding/xml"
	"io"
	"regexp"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

type lyricTimingQuality uint8

const (
	timingUnsynced lyricTimingQuality = iota
	timingLine
	timingWord
	timingSyllable
)

var lrcTimestampPattern = regexp.MustCompile(`(?m)^\s*(?:\[[0-9]{1,3}:[0-9]{2}(?:[.:][0-9]{1,3})?\])+`)

// enhancedWordPattern matches the inline word markers of enhanced LRC (LRCLib,
// Karaoke effects and converted word-synced sources).
var enhancedWordPattern = regexp.MustCompile(`<\d{1,3}:\d{2}(?:[.:]\d{1,3})?>`)

type lyricsCandidate struct {
	response lyrics.GetLyricsResponse
	quality  lyricTimingQuality
}

func newLyricsCandidate(text lyrics.LyricsText, provider, format string) lyricsCandidate {
	if !lyricsTextHasContent(text.Text, format) {
		// A well-formed but empty or metadata-only payload (e.g. `<tt></tt>`)
		// must not become a present candidate that outranks usable fallbacks
		// (Codex).
		return lyricsCandidate{}
	}
	quality := timingUnsynced
	switch format {
	case "ttml":
		var valid bool
		quality, valid = inspectTTMLTimingQuality(text.Text)
		if !valid {
			return lyricsCandidate{}
		}
	case "lrc", "elrc":
		if lrcTimestampPattern.MatchString(text.Text) {
			quality = timingLine
		}
		if enhancedWordPattern.MatchString(text.Text) {
			quality = timingWord
		}
	}
	return lyricsCandidate{
		response: sourcedLyricsResponse(text, provider, format),
		quality:  quality,
	}
}

// lyricsTextHasContent reports whether a payload carries servable lyric text:
// tags and timing stamps alone do not count.
func lyricsTextHasContent(value, format string) bool {
	if format == "ttml" {
		// Only text inside lyric-bearing nodes counts: metadata/head text in
		// an otherwise empty body must not pass (Codex round 13). Documents
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

// emptyLyricsDocument reports a payload with no lyric content that is still
// structurally well-formed (e.g. `<tt></tt>`): such documents are clean
// misses, whereas malformed XML degrades. TTML well-formedness is checked
// structurally; other formats treat parsed-but-contentless as empty.
func emptyLyricsDocument(value, format string) bool {
	if lyricsTextHasContent(value, format) {
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

func (candidate lyricsCandidate) present() bool {
	return len(candidate.response.Lyrics) > 0
}

func preferLyricsCandidate(current, next lyricsCandidate) lyricsCandidate {
	if !next.present() {
		return current
	}
	if !current.present() || next.quality > current.quality {
		return next
	}
	return current
}

// ttmlTimingQuality inspects timing metadata without rewriting the TTML. Invalid
// XML is treated as unsynchronized so malformed provider data cannot outrank a
// valid fallback.
func ttmlTimingQuality(input string) lyricTimingQuality {
	quality, valid := inspectTTMLTimingQuality(input)
	if !valid {
		return timingUnsynced
	}
	return quality
}

func inspectTTMLTimingQuality(input string) (lyricTimingQuality, bool) {
	decoder := xml.NewDecoder(strings.NewReader(input))
	quality := timingUnsynced
	insideLine := false
	seenRoot := false
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				return quality, seenRoot
			}
			return timingUnsynced, false
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
						quality = timingSyllable
					case "word":
						if quality < timingWord {
							quality = timingWord
						}
					case "line":
						if quality < timingLine {
							quality = timingLine
						}
					}
				}
			}

			if quality < timingLine && start.Name.Local == "p" && hasTimingAttribute(start) {
				quality = timingLine
			}
			if quality < timingWord && insideLine && start.Name.Local == "span" && hasTimingAttribute(start) {
				quality = timingWord
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
