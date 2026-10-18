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
		{name: "syllable metadata with timed text", ttml: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><p begin="1"><span begin="1" end="2">word</span></p></tt>`, want: TimingSyllable},
		{name: "word metadata with timed text", ttml: `<tt xmlns:i="urn:itunes" i:timing="Word"><p begin="1"><span begin="1" end="2">word</span></p></tt>`, want: TimingWord},
		{name: "line metadata with timed text", ttml: `<tt xmlns:i="urn:itunes" i:timing="Line"><p begin="1">line</p></tt>`, want: TimingLine},
		{name: "metadata alone cannot grade timing", ttml: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><p>plain</p></tt>`, want: TimingUnsynced},
		{name: "metadata cannot upgrade timed lines", ttml: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><p begin="1">line</p></tt>`, want: TimingLine},
		{name: "empty timed nodes cannot grade timing", ttml: `<tt xmlns:i="urn:itunes" i:timing="Word"><p begin="1"><span begin="1" end="2"/></p></tt>`, want: TimingUnsynced},
		{name: "empty timed spans cannot upgrade lines", ttml: `<tt><p begin="1"><span begin="1" end="2"/>line</p></tt>`, want: TimingLine},
		{name: "timed spans without metadata", ttml: `<tt><p begin="1" end="2"><span begin="1" end="2">word</span></p></tt>`, want: TimingWord},
		{name: "ignores timed metadata spans", ttml: `<tt><head><text><span begin="1">translation</span></text></head><body><p begin="1">line</p></body></tt>`, want: TimingLine},
		{name: "timed lines only", ttml: `<tt><p begin="1" end="2">line</p></tt>`, want: TimingLine},
		{name: "untimed text", ttml: `<tt><p>plain</p></tt>`, want: TimingUnsynced},
		{name: "invalid begin cannot grade timing", ttml: `<tt><p begin="later">line</p></tt>`, want: TimingUnsynced},
		{name: "invalid word timing retains line grade", ttml: `<tt><p begin="1"><span begin="NaN">word</span></p></tt>`, want: TimingLine},
		{name: "reversed range cannot grade timing", ttml: `<tt><p begin="2s" end="1s">line</p></tt>`, want: TimingUnsynced},
		{name: "empty range cannot grade timing", ttml: `<tt><p begin="1" end="1">line</p></tt>`, want: TimingUnsynced},
		{name: "zero duration cannot grade timing", ttml: `<tt><p begin="1s" dur="0s">line</p></tt>`, want: TimingUnsynced},
		{name: "offset units", ttml: `<tt><p begin="1000ms" dur="1s">line</p></tt>`, want: TimingLine},
		{name: "clock time", ttml: `<tt><p begin="01:02:03.456" end="01:02:05">line</p></tt>`, want: TimingLine},
		{name: "minute clock time", ttml: `<tt><p begin="3:05.034" end="3:08.123">line</p></tt>`, want: TimingLine},
		{name: "invalid clock seconds", ttml: `<tt><p begin="1:99.00">line</p></tt>`, want: TimingUnsynced},
		{name: "invalid clock shape", ttml: `<tt><p begin="1:2:03">line</p></tt>`, want: TimingUnsynced},
		{name: "negative time", ttml: `<tt><p begin="-1s">line</p></tt>`, want: TimingUnsynced},
		{name: "timing in metadata cannot grade", ttml: `<tt><head><metadata><p begin="1"><span begin="1">translation</span></p></metadata></head><body><p>plain</p></body></tt>`, want: TimingUnsynced},
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

func TestUntimedTTMLMetadataCannotOutrankTimedLyrics(t *testing.T) {
	t.Parallel()

	untimed := NewCandidate(lyrics.LyricsText{Text: `<tt xmlns:i="urn:itunes" i:timing="Syllable"><body><p>plain lyrics</p></body></tt>`}, "untimed", "ttml")
	timed := NewCandidate(lyrics.LyricsText{Text: "[00:01.00]timed lyrics"}, "timed", "lrc")
	if !untimed.Present() {
		t.Fatal("untimed TTML must remain available as plain lyrics")
	}
	if got := Prefer(untimed, timed).Response.Source.Provider; got != "timed" {
		t.Fatalf("preferred provider = %q, want timed", got)
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
