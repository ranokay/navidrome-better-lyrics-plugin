package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseRichsync(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantLines  int
		wantFirst  timedLine
		wantErrNil bool
	}{
		{
			name:       "word chunks with offsets",
			body:       `[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":"llo","o":120}]},{"ts":14.0,"te":14.5,"l":[{"c":" world","o":0}]}]`,
			wantLines:  2,
			wantErrNil: true,
		},
		{
			name:      "empty body errors",
			body:      `[]`,
			wantLines: 0,
		},
		{
			name:      "invalid body errors",
			body:      `not-json`,
			wantLines: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lines, err := parseRichsync(test.body)
			if !test.wantErrNil {
				if err == nil {
					t.Fatalf("parseRichsync() error = nil, want err")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRichsync() error = %v, want nil", err)
			}
			if len(lines) != test.wantLines {
				t.Fatalf("lines = %d, want %d", len(lines), test.wantLines)
			}
		})
	}
}

func TestParseRichsync_WordWindows(t *testing.T) {
	t.Parallel()

	lines, err := parseRichsync(`[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":"llo","o":120}]},{"ts":14.0,"te":14.5,"l":[{"c":" world","o":0}]}]`)
	if err != nil {
		t.Fatalf("parseRichsync() error = %v, want nil", err)
	}
	first := lines[0]
	if first.StartMs != 12400 || first.EndMs != 13900 {
		t.Fatalf("line window = %d..%d, want 12400..13900", first.StartMs, first.EndMs)
	}
	if len(first.Words) != 2 {
		t.Fatalf("words = %#v, want two", first.Words)
	}
	if first.Words[0].Text != "He" || first.Words[0].StartMs != 12400 {
		t.Fatalf("word 1 = %#v, want He at 12400", first.Words[0])
	}
	// unbounded trailing tokens glow to the line end; the reference floor of 40 ms
	// only raises windows that would end before they start.
	if first.Words[1].StartMs != 12520 || first.Words[1].EndMs != 13900 {
		t.Fatalf("word 2 = %#v, want llo at 12520..13900", first.Words[1])
	}
	if second := lines[1]; second.StartMs != 14000 || second.Words[0].Text != " world" || second.Words[0].EndMs != 14500 {
		t.Fatalf("line 2 = %#v, want world window closing at the line end", second)
	}
}

func TestParseRichsyncFixtureSmoke(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("testdata/musixmatch/subtitles.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	lines, parseErr := parseRichsync(string(raw))
	if parseErr != nil {
		t.Fatalf("parseRichsync(fixture) error = %v", parseErr)
	}
	if len(lines) == 0 {
		t.Fatalf("fixture lines = 0, want timed lines")
	}
	if !strings.Contains(enhancedLRCFromTimedLines(lines[:1]), "[") {
		t.Fatal("fixture render has no LRC stamp")
	}
}
