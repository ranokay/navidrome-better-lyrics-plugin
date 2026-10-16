package main

import (
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestEnhancedLRCTimingGrading(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format string
		text   string
		want   lyricTimingQuality
	}{
		{name: "word markers grade as word", format: "elrc", text: "[00:01.00]<00:01.10>word<00:01.90>line", want: timingWord},
		{name: "word marker after line timestamp", format: "elrc", text: "[00:30.00]<00:31.00>continuation", want: timingWord},
		{name: "timestamp-less word markers do not grade", format: "elrc", text: "plain text <3 heart", want: timingUnsynced},
		{name: "timestamp-less lrc does not grade", format: "elrc", text: "plain text", want: timingUnsynced},
		{name: "lrc format keeps line grading", format: "lrc", text: "[00:01.00]<00:01.10>word<00:01.90>line", want: timingLine},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := newLyricsCandidate(lyrics.LyricsText{Text: test.text}, "stub", test.format)
			if !candidate.present() {
				t.Fatalf("candidate absent for %q (%s)", test.text, test.format)
			}
			if candidate.quality != test.want {
				t.Fatalf("quality = %v, want %v", candidate.quality, test.want)
			}
		})
	}
}
