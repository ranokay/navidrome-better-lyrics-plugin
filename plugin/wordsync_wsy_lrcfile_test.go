package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestParsePetitWSY(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("testdata/petitlyrics/word.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	lines, parseErr := parsePetitWSY(raw)
	if parseErr != nil {
		t.Fatalf("parsePetitWSY() error = %v, want nil", parseErr)
	}
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5 (fixture keeps five lines)", len(lines))
	}
	if !strings.Contains(lines[0].Text, "maybe") || lines[0].StartMs == 0 {
		t.Fatalf("line 1 = %#v, want the opening line with its start time", lines[0])
	}
	if len(lines[0].Words) == 0 {
		t.Fatalf("line 1 words = empty, want word timings")
	}
}

func TestParsePetitWSY_MinimalDocument(t *testing.T) {
	t.Parallel()

	raw := `<wsy><linenum>2</linenum>` +
		`<line><linestring>hello world</linestring><wordnum>2</wordnum>` +
		`<word><starttime>100</starttime><endtime>150</endtime><wordstring>hello</wordstring></word>` +
		`<word><starttime>150</starttime><endtime>400</endtime><wordstring>world</wordstring></word></line>` +
		`<line><linestring>plain row</linestring></line></wsy>`

	lines, err := parsePetitWSY([]byte(raw))
	if err != nil {
		t.Fatalf("parsePetitWSY() error = %v, want nil", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if len(lines[0].Words) != 2 || lines[0].Words[0].StartMs != 100 || lines[0].Words[1].EndMs != 400 {
		t.Fatalf("line 1 words = %#v", lines[0].Words)
	}
	if lines[1].Text != "plain row" || lines[1].Words != nil {
		t.Fatalf("line 2 = %#v, want wordless line", lines[1].Text)
	}
}

func TestParsePetitWSY_RejectsEmptyDocuments(t *testing.T) {
	t.Parallel()

	if _, err := parsePetitWSY([]byte("<wsy></wsy>")); err == nil {
		t.Fatal("parsePetitWSY(empty) error = nil, want err")
	}
	if _, err := parsePetitWSY([]byte("<bad/>")); err == nil {
		t.Fatal("parsePetitWSY(nondialect) error = nil, want err")
	}
}

func TestParseLRCLibLyricsFile(t *testing.T) {
	t.Parallel()

	yamlDoc := "version: '1.0'\n" +
		"metadata:\n" +
		"  title: Song\n" +
		"lines:\n" +
		"- start_ms: 1000\n" +
		"  end_ms: 2500\n" +
		"  text: first line\n" +
		"  words:\n" +
		"  - text: first\n" +
		"    start_ms: 1000\n" +
		"    end_ms: 1500\n" +
		"  - text: line\n" +
		"    start_ms: 1500\n" +
		"    end_ms: 2500\n" +
		"- start_ms: 3000\n" +
		"  end_ms: 3600\n" +
		"  text: second line\n"

	lines, err := parseLRCLibLyricsFile([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("parseLRCLibLyricsFile() error = %v, want nil", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if len(lines[0].Words) != 2 || lines[0].Words[0].StartMs != 1000 || lines[0].Words[1].EndMs != 2500 {
		t.Fatalf("line 1 words = %#v, want timed words from the YAML", lines[0].Words)
	}
	if lines[1].Words != nil || lines[1].Text != "second line" {
		t.Fatalf("line 2 = %#v, want wordless line", lines[1])
	}
}

func TestParseLRCLibLyricsFile_FixtureSmoke(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/lrclib/get.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	var envelope struct {
		Lyricsfile string `json:"lyricsfile"`
	}
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Skipf("fixture not a JSON envelope: %v", err)
	}
	if envelope.Lyricsfile == "" {
		t.Skip("fixture has no lyricsfile payload")
	}
	lines, parseErr := parseLRCLibLyricsFile([]byte(envelope.Lyricsfile))
	if parseErr != nil {
		t.Fatalf("parseLRCLibLyricsFile(fixture) error = %v", parseErr)
	}
	if len(lines) == 0 {
		t.Fatalf("fixture lines = 0, want timed lines")
	}
}
