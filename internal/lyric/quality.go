package lyric

import (
	"encoding/xml"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var lrcTimestampPattern = regexp.MustCompile(`(?m)^\s*(?:\[[0-9]{1,3}:[0-9]{2}(?:[.:][0-9]{1,3})?\])+`)

// enhancedWordPattern matches the inline word markers of enhanced LRC (LRCLib,
// Karaoke effects and converted word-synced sources).
var enhancedWordPattern = regexp.MustCompile(`<\d{1,3}:\d{2}(?:[.:]\d{1,3})?>`)

// ttmlTimingQuality grades actual timed lyric text without rewriting the TTML.
// Metadata can distinguish syllables from words only after timed spans qualify.
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
	seenRoot := false
	syllableTiming := false
	type timingNode struct {
		inLine   bool
		metadata bool
		hasText  bool
		quality  TimingQuality
	}
	var nodes []timingNode
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				if quality == TimingWord && syllableTiming {
					quality = TimingSyllable
				}
				return quality, seenRoot
			}
			return TimingUnsynced, false
		}
		switch element := token.(type) {
		case xml.EndElement:
			node := nodes[len(nodes)-1]
			nodes = nodes[:len(nodes)-1]
			if node.hasText && node.quality > quality {
				quality = node.quality
			}
		case xml.StartElement:
			node := timingNode{}
			if len(nodes) == 0 {
				if seenRoot || element.Name.Local != "tt" {
					return TimingUnsynced, false
				}
				seenRoot = true
				for _, attribute := range element.Attr {
					if attribute.Name.Local == "timing" && strings.EqualFold(strings.TrimSpace(attribute.Value), "syllable") {
						syllableTiming = true
					}
				}
			} else {
				parent := nodes[len(nodes)-1]
				node.inLine, node.metadata = parent.inLine, parent.metadata
			}
			if element.Name.Local == "head" || element.Name.Local == "metadata" {
				node.metadata = true
			}
			if !node.metadata {
				if element.Name.Local == "p" {
					node.inLine = true
					if hasUsefulTTMLTiming(element) {
						node.quality = TimingLine
					}
				}
				if element.Name.Local == "span" && node.inLine && hasUsefulTTMLTiming(element) {
					node.quality = TimingWord
				}
			}
			nodes = append(nodes, node)
		case xml.CharData:
			if strings.TrimSpace(string(element)) == "" {
				continue
			}
			if len(nodes) == 0 {
				return TimingUnsynced, false
			}
			if !nodes[len(nodes)-1].metadata {
				for i := range nodes {
					nodes[i].hasText = true
				}
			}
		}
	}
}

func hasUsefulTTMLTiming(element xml.StartElement) bool {
	var begin, end, duration string
	for _, attribute := range element.Attr {
		switch attribute.Name.Local {
		case "begin":
			begin = strings.TrimSpace(attribute.Value)
		case "end":
			end = strings.TrimSpace(attribute.Value)
		case "dur":
			duration = strings.TrimSpace(attribute.Value)
		}
	}
	start, valid := ttmlTimeSeconds(begin)
	if !valid {
		return false
	}
	if end != "" {
		stop, valid := ttmlTimeSeconds(end)
		if !valid || stop <= start {
			return false
		}
	}
	if duration != "" {
		seconds, valid := ttmlTimeSeconds(duration)
		if !valid || seconds <= 0 {
			return false
		}
	}
	return true
}

var ttmlOffsetTimePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?|\.[0-9]+)(ms|h|m|s)?$`)
var ttmlClockTimePattern = regexp.MustCompile(`^(?:[0-9]+:[0-9]{2}|[0-9]+):[0-9]{2}(?:\.[0-9]+)?$`)

// ttmlTimeSeconds accepts provider clock and offset time expressions, including
// the bare seconds used by existing Apple-style providers. Frame and tick
// expressions need their document timebase and do not establish a grade here.
func ttmlTimeSeconds(value string) (float64, bool) {
	if match := ttmlOffsetTimePattern.FindStringSubmatch(value); match != nil {
		seconds, err := strconv.ParseFloat(match[1], 64)
		switch match[2] {
		case "ms":
			seconds /= 1000
		case "m":
			seconds *= 60
		case "h":
			seconds *= 3600
		}
		return seconds, err == nil && !math.IsInf(seconds, 0)
	}
	if !ttmlClockTimePattern.MatchString(value) {
		return 0, false
	}
	parts := strings.Split(value, ":")
	var total float64
	for i, part := range parts {
		seconds, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsInf(seconds, 0) || (i > 0 && seconds >= 60) {
			return 0, false
		}
		total = total*60 + seconds
	}
	return total, !math.IsInf(total, 0)
}
