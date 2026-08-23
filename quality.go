package main

import (
	"encoding/xml"
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

type lyricsCandidate struct {
	response lyrics.GetLyricsResponse
	quality  lyricTimingQuality
}

func newLyricsCandidate(text lyrics.LyricsText, provider, format string) lyricsCandidate {
	quality := timingUnsynced
	switch format {
	case "ttml":
		quality = ttmlTimingQuality(text.Text)
	case "lrc":
		quality = timingLine
	}
	return lyricsCandidate{
		response: sourcedLyricsResponse(text, provider, format),
		quality:  quality,
	}
}

func (candidate lyricsCandidate) present() bool {
	return len(candidate.response.Lyrics) > 0
}

func preferLyricsCandidate(current, next lyricsCandidate) lyricsCandidate {
	if !current.present() || next.quality > current.quality {
		return next
	}
	return current
}

// ttmlTimingQuality inspects timing metadata without rewriting the TTML. The
// original string must remain byte-for-byte intact because whitespace can
// separate timed syllables.
func ttmlTimingQuality(input string) lyricTimingQuality {
	decoder := xml.NewDecoder(strings.NewReader(input))
	quality := timingUnsynced
	insideLine := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return quality
		}
		switch element := token.(type) {
		case xml.EndElement:
			if element.Name.Local == "p" {
				insideLine = false
			}
			continue
		case xml.StartElement:
			if element.Name.Local == "p" {
				insideLine = true
			}
			start := element

			if start.Name.Local == "tt" {
				for _, attribute := range start.Attr {
					if attribute.Name.Local != "timing" {
						continue
					}
					switch strings.ToLower(strings.TrimSpace(attribute.Value)) {
					case "syllable":
						return timingSyllable
					case "word":
						quality = timingWord
					case "line":
						quality = timingLine
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
