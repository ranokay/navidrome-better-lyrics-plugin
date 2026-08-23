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

type lyricsCandidate struct {
	response lyrics.GetLyricsResponse
	quality  lyricTimingQuality
}

func newLyricsCandidate(text lyrics.LyricsText, provider, format string) lyricsCandidate {
	quality := timingUnsynced
	switch format {
	case "ttml":
		var valid bool
		quality, valid = inspectTTMLTimingQuality(text.Text)
		if !valid {
			return lyricsCandidate{}
		}
	case "lrc":
		if lrcTimestampPattern.MatchString(text.Text) {
			quality = timingLine
		}
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
