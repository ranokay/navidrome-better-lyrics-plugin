package main

import (
	"strings"
	"testing"
)

func TestParseLyricifyLYL_LinesAndSyllables(t *testing.T) {
	t.Parallel()

	input := "[type:LyricifyLines]\n" +
		"[9731,14858]The club isn't the best place to find a lover so the bar is where I go\n" +
		"[14858,19592]Me and my friends at the weekend and it's something we go\n"

	lines, err := parseLyricifyLYL(input)
	if err != nil {
		t.Fatalf("parseLyricifyLYL() error = %v, want nil", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if lines[0].StartMs != 9731 || lines[0].EndMs != 24589 {
		t.Fatalf("line 1 = %d..%d, want 9731..24589", lines[0].StartMs, lines[0].EndMs)
	}
	if lines[0].Text == "" || lines[0].Words != nil {
		t.Fatalf("line 1 = %#v, want wordless line with text", lines[0])
	}

	wordFile := "[type:LyricifyLines]\n" +
		"[from:AppleSyllable]\n" +
		"[100,400]Hel(100,200) (200,100)lo(300,100)\n"
	syllables, err := parseLyricifyLYL(wordFile)
	if err != nil {
		t.Fatalf("parseLyricifyLYL(syllable) error = %v, want nil", err)
	}
	if len(syllables) != 1 {
		t.Fatalf("syllable lines = %d, want 1", len(syllables))
	}
	if len(syllables[0].Words) != 2 {
		t.Fatalf("syllable words = %#v, want two", syllables[0].Words)
	}
	if syllables[0].Words[0].StartMs != 100 || syllables[0].Words[1].EndMs != 400 {
		t.Fatalf("syllable timing = %#v", syllables[0].Words)
	}
}

func TestParseLyricifyLYL_RendersEnhancedLRC(t *testing.T) {
	t.Parallel()

	input := "[type:LyricifyLines]\n" +
		"[1000,500]syll text\n"

	lines, err := parseLyricifyLYL(input)
	if err != nil {
		t.Fatalf("parseLyricifyLYL() error = %v, want nil", err)
	}
	output := enhancedLRCFromTimedLines(lines)
	if !strings.HasPrefix(output, "[00:01.00]") {
		t.Fatalf("output = %q, want [00:01.00] prefix", output)
	}
}

func TestParseLyricifyLYL_RejectsOtherDialects(t *testing.T) {
	t.Parallel()

	if _, err := parseLyricifyLYL(""); err == nil {
		t.Fatal("parseLyricifyLYL(empty) error = nil, want err")
	}
	if _, err := parseLyricifyLYL("[00:01.10]normal LRC with word markers"); err == nil {
		t.Fatal("parseLyricifyLYL(lrc) error = nil, want err")
	}
}
