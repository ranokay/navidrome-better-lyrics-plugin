package main

import (
	"strings"
	"testing"
)

func TestParseKFamilyLyrics_RelativeAngleTuples(t *testing.T) {
	t.Parallel()

	input := "[opera:1]\n" +
		"[ti:Song]\n" +
		"[offset:0]\n" +
		"[10000,3000]<0,500,0>Hello <500,500,0>world\n" +
		"[15000,1000]wordless line\n"

	lines, err := parseKFamilyLyrics(wordSyncKRC, input)
	if err != nil {
		t.Fatalf("parseKFamilyLyrics() error = %v, want nil", err)
	}
	if len(lines) != 2 {
		t.Fatalf("parsed %d lines, want metadata tags skipped and 2 timed lines", len(lines))
	}
	if !strings.Contains(input, "opera") && len(lines) == 0 {
		t.Fatal("unreachable guard")
	}
	first := lines[0]
	if first.StartMs != 10000 || first.EndMs != 13000 {
		t.Fatalf("line 1 = %d..%d, want 10000..13000", first.StartMs, first.EndMs)
	}
	if len(first.Words) != 2 {
		t.Fatalf("line 1 words = %#v, want two", first.Words)
	}
	if first.Words[0].Text != "Hello " || first.Words[0].StartMs != 10000 || first.Words[0].EndMs != 10500 {
		t.Fatalf("word 1 = %#v, want Hello 10000..10500", first.Words[0])
	}
	if first.Words[1].Text != "world" || first.Words[1].StartMs != 10500 || first.Words[1].EndMs != 11000 {
		t.Fatalf("word 2 = %#v, want world 10500..11000", first.Words[1])
	}
	second := lines[1]
	if second.StartMs != 15000 || len(second.Words) != 0 {
		t.Fatalf("line 2 = %#v, want one wordless timed line at 15000", second)
	}

	output := enhancedLRCFromTimedLines(lines)
	want := "[00:10.00]<00:10.00>Hello <00:10.50>world\n[00:15.00]wordless line"
	if strings.TrimSpace(output) != want {
		t.Fatalf("enhanced LRC = %q, want %q", output, want)
	}
}

func TestParseKFamilyLyrics_SkipsTranslationAndMetadataLines(t *testing.T) {
	t.Parallel()

	input := "[ti:Shape of You]\n" +
		"[ar:Ed Sheeran]\n" +
		"[offset:0]\n" +
		"[1000,2000]line\n" +
		"[tr:2000,1000]translation row\n"

	lines, err := parseKFamilyLyrics(wordSyncQRC, input)
	if err != nil {
		t.Fatalf("parseKFamilyLyrics() error = %v, want nil", err)
	}
	if len(lines) != 1 || lines[0].StartMs != 1000 {
		t.Fatalf("lines = %#v, want only the single timed line", lines)
	}
}

func TestParseKFamilyLyrics_RejectsEmptyInput(t *testing.T) {
	t.Parallel()

	if _, err := parseKFamilyLyrics(wordSyncKRC, ""); err == nil {
		t.Fatal("parseKFamilyLyrics(empty) error = nil, want err")
	}
	if _, err := parseKFamilyLyrics(wordSyncKRC, "[ti:only metadata]"); err == nil {
		t.Fatal("parseKFamilyLyrics(metadata only) error = nil, want err")
	}
}

func TestParseKFamilyLyrics_HandlesQRCEnvelopeContent(t *testing.T) {
	t.Parallel()

	// The raw fixture content produced by boidu /qq/getLyrics, minus its XML
	// envelope: line tuples are absolute; word tuples carry 2 args here.
	input := "[ti:Shape of You]\n" +
		"[0,2380]Shape(0,476) (476,95)of(571,190)\n" +
		"[228364,2284]I'm (228364,144)in (228508,177)love (228685,394)with the shape of you\n"

	lines, err := parseKFamilyLyrics(wordSyncQRC, input)
	if err != nil {
		t.Fatalf("parseKFamilyLyrics() error = %v, want nil", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if lines[0].StartMs != 0 || lines[1].StartMs != 228364 {
		t.Fatalf("line starts = %d/%d, want 0/228364", lines[0].StartMs, lines[1].StartMs)
	}
}

func TestParseKFamilyLyrics_HandlesLunaKRCContent(t *testing.T) {
	t.Parallel()

	input := "[400,4900]<0,1040,0>Beneath <1040,280,0>an <1320,240,0>empty\n"

	lines, err := parseKFamilyLyrics(wordSyncKRC, input)
	if err != nil {
		t.Fatalf("parseKFamilyLyrics() error = %v, want nil", err)
	}
	if len(lines) != 1 || lines[0].StartMs != 400 {
		t.Fatalf("lines = %#v, want single line at 400ms", lines)
	}
	if lines[0].Words[0].StartMs != 400 || lines[0].Words[0].Text != "Beneath " {
		t.Fatalf("first word = %#v, want Beneath at 400ms", lines[0].Words[0])
	}
}

func TestKFamilyOffsetShiftsTimings(t *testing.T) {
	t.Parallel()

	// KRC/QRC/YRC carry a global `[offset:±ms]` tag; dropping it left every
	// line and word window unshifted.
	positive, err := parseKFamilyLyrics(wordSyncQRC, "[offset:500]\n[1000,400]word(0,400)\n")
	if err != nil {
		t.Fatalf("parseKFamilyLyrics(offset +500) error = %v, want nil", err)
	}
	if rendered := enhancedLRCFromTimedLines(positive); !strings.HasPrefix(rendered, "[00:01.50]<00:01.50>word") {
		t.Fatalf("rendered = %q, want the +500ms offset folded into the stamp", rendered)
	}

	negative, err := parseKFamilyLyrics(wordSyncQRC, "[offset:-300]\n[500,400]word(0,400)\n")
	if err != nil {
		t.Fatalf("parseKFamilyLyrics(offset -300) error = %v, want nil", err)
	}
	if rendered := enhancedLRCFromTimedLines(negative); !strings.HasPrefix(rendered, "[00:00.20]<00:00.20>word") {
		t.Fatalf("rendered = %q, want the -300ms offset folded into the stamp", rendered)
	}

	untouched, err := parseKFamilyLyrics(wordSyncQRC, "[offset:0]\n[1000,400]word(0,400)\n")
	if err != nil {
		t.Fatalf("parseKFamilyLyrics(offset 0) error = %v, want nil", err)
	}
	if rendered := enhancedLRCFromTimedLines(untouched); !strings.HasPrefix(rendered, "[00:01.00]") {
		t.Fatalf("rendered = %q, want a zero offset to leave timings alone", rendered)
	}
}

func TestEnhancedLRCWordlessAndWordRenderings(t *testing.T) {
	t.Parallel()

	lines := []timedLine{
		{StartMs: 1000, EndMs: 2000, Text: "wordless", Words: nil},
		{StartMs: 3000, EndMs: 4000, Text: "duplicated in words", Words: []timedWord{{StartMs: 3000, EndMs: 3600, Text: "dup"}}},
	}
	out := enhancedLRCFromTimedLines(lines)
	if !strings.Contains(out, "[00:01.00]wordless") {
		t.Fatalf("output = %q, want wordless text retained", out)
	}
	if strings.Contains(out, "duplicated in words") {
		t.Fatalf("output = %q, want line text suppressed for word-bearing lines (no duplication)", out)
	}
}

func TestParseKFamilyLyrics_QRCRetainsWhitespaceTokens(t *testing.T) {
	t.Parallel()

	input := "[0,2380]Shape(0,476) (476,95)of(571,190)\n"
	lines, err := parseKFamilyLyrics(wordSyncQRC, input)
	if err != nil {
		t.Fatalf("parseKFamilyLyrics() error = %v, want nil", err)
	}
	out := enhancedLRCFromTimedLines(lines)
	if strings.Contains(out, "Shapeof") {
		t.Fatalf("output = %q, want the standalone space token retained", out)
	}
	if !strings.Contains(out, "<00:00.47> ") {
		t.Fatalf("output = %q, want a timed space token between Shape and of", out)
	}
}
