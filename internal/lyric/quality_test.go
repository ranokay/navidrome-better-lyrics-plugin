package lyric

import (
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestTTMLTimingQuality(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ttml string
		want TimingQuality
	}{
		{name: "syllable metadata", ttml: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><p begin="1"/></tt>`, want: TimingSyllable},
		{name: "word metadata", ttml: `<tt xmlns:i="urn:itunes" i:timing="Word"><p begin="1"/></tt>`, want: TimingWord},
		{name: "line metadata", ttml: `<tt xmlns:i="urn:itunes" i:timing="Line"><p begin="1"/></tt>`, want: TimingLine},
		{name: "timed spans without metadata", ttml: `<tt><p begin="1" end="2"><span begin="1" end="2">word</span></p></tt>`, want: TimingWord},
		{name: "ignores timed metadata spans", ttml: `<tt><head><text><span begin="1">translation</span></text></head><body><p begin="1">line</p></body></tt>`, want: TimingLine},
		{name: "timed lines only", ttml: `<tt><p begin="1" end="2">line</p></tt>`, want: TimingLine},
		{name: "untimed text", ttml: `<tt><p>plain</p></tt>`, want: TimingUnsynced},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ttmlTimingQuality(test.ttml); got != test.want {
				t.Fatalf("ttmlTimingQuality() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEnhancedLRCTimingGrading(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format string
		text   string
		want   TimingQuality
	}{
		{name: "word markers grade as word", format: "elrc", text: "[00:01.00]<00:01.10>word<00:01.90>line", want: TimingWord},
		{name: "word marker after line timestamp", format: "elrc", text: "[00:30.00]<00:31.00>continuation", want: TimingWord},
		{name: "timestamp-less word markers do not grade", format: "elrc", text: "plain text <3 heart", want: TimingUnsynced},
		{name: "timestamp-less lrc does not grade", format: "elrc", text: "plain text", want: TimingUnsynced},
		{name: "lrc content with word markers grades as word", format: "lrc", text: "[00:01.00]<00:01.10>word<00:01.90>line", want: TimingWord},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := NewCandidate(lyrics.LyricsText{Text: test.text}, "stub", test.format)
			if !candidate.Present() {
				t.Fatalf("candidate absent for %q (%s)", test.text, test.format)
			}
			if candidate.Quality != test.want {
				t.Fatalf("quality = %v, want %v", candidate.Quality, test.want)
			}
		})
	}
}
