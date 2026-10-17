package lyric

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// ParsePetitWSY parses PetitLyrics' word-synced WSY payload (plain XML with
// per-line and per-word millisecond windows).
func ParsePetitWSY(data []byte) ([]TimedLine, error) {
	var doc petitWSYDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode WSY: %w", err)
	}
	lines := make([]TimedLine, 0, len(doc.Lines))
	for _, row := range doc.Lines {
		line := TimedLine{
			StartMs: row.FirstStart(),
			EndMs:   row.LastEnd(),
			Text:    row.Text,
		}
		for _, word := range row.Words {
			if word.StartMs == 0 && word.EndMs == 0 {
				continue
			}
			line.Words = append(line.Words, TimedWord{StartMs: word.StartMs, EndMs: word.EndMs, Text: word.Text})
		}
		if strings.TrimSpace(line.Text) == "" && len(line.Words) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil, errEmptyWordSyncInput
	}
	return lines, nil
}

type petitWSYDocument struct {
	Lines []petitWSYLine `xml:"line"`
}

type petitWSYLine struct {
	Text  string         `xml:"linestring"`
	Words []petitWSYWord `xml:"word"`
}

type petitWSYWord struct {
	StartMs int64  `xml:"starttime"`
	EndMs   int64  `xml:"endtime"`
	Text    string `xml:"wordstring"`
}

func (l petitWSYLine) FirstStart() int64 {
	for _, word := range l.Words {
		return word.StartMs
	}
	return 0
}

func (l petitWSYLine) LastEnd() int64 {
	if len(l.Words) == 0 {
		return 0
	}
	return l.Words[len(l.Words)-1].EndMs
}
