package lyric

import (
	"fmt"
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestOversizedLyricDocumentsAreRejected(t *testing.T) {
	oversized := strings.Repeat("x", (8<<20)+1)
	if NewCandidate(lyrics.LyricsText{Text: oversized}, "source", "plain").Present() {
		t.Fatal("oversized document became a candidate")
	}
	if _, err := ParseKFamily(QRC, "[0,1000]"+oversized); err == nil {
		t.Fatal("oversized word-synced document parsed successfully")
	}
	if _, err := ParseLRCLibLyricsFile([]byte("lines:\n- start_ms: 0\n  end_ms: 1\n  text: lyric\n#" + oversized)); err == nil {
		t.Fatal("oversized YAML document parsed successfully")
	}
}

func TestDeepXMLIsRejectedBeforeCandidateOrRecursiveDecode(t *testing.T) {
	const depth = 140
	empty := "<tt>" + strings.Repeat("<span>", depth) + strings.Repeat("</span>", depth) + "</tt>"
	if EmptyDocument(empty, "ttml") {
		t.Fatal("over-depth XML must be a failure, not a cacheable empty document")
	}
	text := strings.Replace(empty, strings.Repeat("</span>", depth), "lyric"+strings.Repeat("</span>", depth), 1)
	if NewCandidate(lyrics.LyricsText{Text: text}, "source", "ttml").Present() {
		t.Fatal("over-depth TTML became a candidate")
	}
	wsy := "<wsy>" + strings.Repeat("<extra>", depth) + strings.Repeat("</extra>", depth) + "<line><linestring>lyric</linestring></line></wsy>"
	if _, err := ParsePetitWSY([]byte(wsy)); err == nil {
		t.Fatal("over-depth WSY parsed successfully")
	}
	allowed := "<tt>" + strings.Repeat("<span>", 127) + "lyric" + strings.Repeat("</span>", 127) + "</tt>"
	if !NewCandidate(lyrics.LyricsText{Text: allowed}, "source", "ttml").Present() {
		t.Fatal("XML at the depth limit must remain supported")
	}
}

func TestKFamilyRejectsTimestampOverflow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		mode  WordSyncMode
		input string
	}{
		{"digits", QRC, "[18446744073709551616,100]lyric"},
		{"invalid line after valid line", QRC, "[0,100]valid\n[18446744073709551616,100]invalid"},
		{"line end", QRC, "[9223372036854775807,1]lyric"},
		{"relative word start", KRC, "[1000,100]<9223372036854775807,1,0>lyric"},
		{"word end", QRC, "[1000,100]lyric(9223372036854775807,1)"},
		{"word digits", YRC, "[1000,100](18446744073709551616,1,0)lyric"},
		{"offset", QRC, "[offset:9223372036854775807]\n[1000,100]lyric(1000,100)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if lines, err := ParseKFamily(tc.mode, tc.input); err == nil {
				t.Fatalf("overflow parsed into timed lines: %#v", lines)
			}
		})
	}
	if _, err := ParseLyricifyLYL("[type:LyricifyLines]\n[9223372036854775807,1]lyric"); err == nil {
		t.Fatal("LYL accepted overflowing line timing")
	}
}

func TestStructuredWordFormatsRejectInvalidWindows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		start, end int64
	}{
		{"negative start", -1, 100},
		{"negative end", 1, -1},
		{"reversed", 100, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yaml := fmt.Sprintf("lines:\n- start_ms: %d\n  end_ms: %d\n  text: lyric\n", tc.start, tc.end)
			if _, err := ParseLRCLibLyricsFile([]byte(yaml)); err == nil {
				t.Fatal("YAML accepted invalid line timing")
			}
			xml := fmt.Sprintf("<wsy><line><linestring>lyric</linestring><word><starttime>%d</starttime><endtime>%d</endtime><wordstring>lyric</wordstring></word></line></wsy>", tc.start, tc.end)
			if _, err := ParsePetitWSY([]byte(xml)); err == nil {
				t.Fatal("WSY accepted invalid word timing")
			}
		})
	}
}

func TestKFamilyNegativeOffsetClampsWithoutWrapping(t *testing.T) {
	t.Parallel()
	lines, err := ParseKFamily(QRC, "[offset:-9223372036854775807]\n[1000,100]lyric(1000,100)")
	if err != nil {
		t.Fatal(err)
	}
	if got := EnhancedLRC(lines); got != "[00:00.00]<00:00.00>lyric<00:00.00>\n" {
		t.Fatalf("negative offset = %q, want zero-clamped timing", got)
	}
}
