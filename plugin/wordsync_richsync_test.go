package main

import (
	"os"
	"regexp"
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
			body:       `[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":"llo","o":0.12}]},{"ts":14.0,"te":14.5,"l":[{"c":" world","o":0}]}]`,
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

	lines, err := parseRichsync(`[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":"llo","o":0.12}]},{"ts":14.0,"te":14.5,"l":[{"c":" world","o":0}]}]`)
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

func TestParseRichsyncTokenTextContract(t *testing.T) {
	t.Parallel()

	// An empty-text token is dropped; a whitespace token is kept because the
	// renderer concatenates word texts, so the space has to live somewhere.
	lines, err := parseRichsync(`[{"ts":1.0,"te":2.0,"l":[{"c":"He","o":0},{"c":"","o":0.2},{"c":" ","o":0.4},{"c":"llo","o":0.6}]}]`)
	if err != nil {
		t.Fatalf("parseRichsync() error = %v, want nil", err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %#v, want one", lines)
	}
	words := lines[0].Words
	if len(words) != 3 {
		t.Fatalf("words = %#v, want the empty token dropped and the space kept", words)
	}
	if words[0].Text != "He" || words[1].Text != " " || words[2].Text != "llo" {
		t.Fatalf("word texts = %q/%q/%q, want He/ /llo", words[0].Text, words[1].Text, words[2].Text)
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

func TestParseRichsync_FractionalSecondOffsets(t *testing.T) {
	t.Parallel()

	// Musixmatch richsync `o` offsets are fractional seconds relative to the
	// chunk: decrypted as ms they shift words 1000x and fractional values
	// crashed the whole decode.
	lines, err := parseRichsync(`[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":"llo","o":0.12}]}]`)
	if err != nil {
		t.Fatalf("parseRichsync() error = %v, want nil with fractional offsets", err)
	}
	first := lines[0]
	if first.Words[1].StartMs != 12520 {
		t.Fatalf("word 2 start = %d, want 12520 (0.12s after the line start)", first.Words[1].StartMs)
	}

	lines, err = parseRichsync(`[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":1},{"c":"llo","o":1.2}]}]`)
	if err != nil {
		t.Fatalf("parseRichsync() error = %v, want nil with integral offsets", err)
	}
	if lines[0].Words[1].StartMs != 13600 {
		t.Fatalf("word 2 start = %d, want 13600 (1.2s after the line start)", lines[0].Words[1].StartMs)
	}
}

func TestParseRichsyncKeepsWhitespaceTokens(t *testing.T) {
	t.Parallel()

	// Musixmatch can encode a space as its own richsync token; dropping it
	// joins the surrounding words.
	lines, err := parseRichsync(`[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":" ","o":0.4},{"c":"llo","o":0.45}]}]`)
	if err != nil {
		t.Fatalf("parseRichsync() error = %v, want nil", err)
	}
	rendered := enhancedLRCFromTimedLines(lines)
	plain := regexp.MustCompile(`<[0-9:.]+>|\[[0-9:.]+\]`).ReplaceAllString(rendered, "")
	if !strings.Contains(plain, "He ") || strings.Contains(plain, "Hello") {
		t.Fatalf("plain = %q, want the standalone space token retained", plain)
	}
}
