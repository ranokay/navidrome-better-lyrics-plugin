package lyric

import (
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestTimestampedMetadataCannotBecomeLyrics(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"[00:01.00][ar:Artist]",
		"[ar:Artist][00:01.00]",
		"[00:01.00]<00:01.00>[ti:Song]",
	} {
		if candidate := NewCandidate(lyrics.LyricsText{Text: text}, "fixture", "elrc"); candidate.Present() {
			t.Errorf("metadata-only candidate=%q", text)
		}
		if candidate := NewCandidate(lyrics.LyricsText{Text: text}, "fixture", "plain"); !candidate.Present() {
			t.Errorf("literal plain text was discarded: %q", text)
		}
		withLyrics := text + "\n[00:02.00]real lyrics"
		candidate := NewCandidate(lyrics.LyricsText{Text: withLyrics}, "fixture", "lrc")
		if !candidate.Present() || candidate.Response.Lyrics[0].Text != withLyrics {
			t.Error("valid lyrics must remain byte-for-byte intact")
		}
	}
}

func TestEmptyTTMLDocumentIsNotACandidate(t *testing.T) {
	t.Parallel()

	empty := NewCandidate(lyrics.LyricsText{Text: "<tt></tt>"}, "Stub", "ttml")
	if empty.Present() {
		t.Fatal("an empty TTML document must not become a candidate")
	}
	if !EmptyDocument("<tt></tt>", "ttml") {
		t.Fatal("an empty TTML document is a well-formed empty payload")
	}
	metadataOnly := NewCandidate(lyrics.LyricsText{Text: `<tt xmlns:itunes="urn:itunes"></tt>`}, "Stub", "ttml")
	if metadataOnly.Present() {
		t.Fatal("a metadata-only TTML document must not become a candidate")
	}
	if EmptyDocument("<tt><broken", "ttml") {
		t.Fatal("malformed XML is not a well-formed empty payload")
	}
}

func TestCDATALyricsCountAsContent(t *testing.T) {
	t.Parallel()

	// CDATA carries real lyric text: rejecting it would cache a miss for
	// valid documents.
	cdata := `<tt xmlns:itunes="urn:itunes"><body><div><p begin="1" end="2"><![CDATA[Rock & Roll]]></p></div></body></tt>`
	if !hasLyricContent(cdata, "ttml") {
		t.Fatal("CDATA lyric text must count as servable content")
	}
	candidate := NewCandidate(lyrics.LyricsText{Text: cdata}, "Stub", "ttml")
	if !candidate.Present() {
		t.Fatal("a CDATA-bearing document must produce a candidate")
	}
	if EmptyDocument(cdata, "ttml") {
		t.Fatal("a CDATA-bearing document is not an empty document")
	}
}

func TestTTMLContentIgnoresMetadataNodes(t *testing.T) {
	t.Parallel()

	metadataOnly := `<tt xmlns:itunes="urn:itunes"><head><metadata>Song Title Words Here</metadata></head><body></body></tt>`
	if hasLyricContent(metadataOnly, "ttml") {
		t.Fatal("metadata-only text must not count as lyric content")
	}
	withLyrics := `<tt xmlns:itunes="urn:itunes"><head><metadata>Song Title</metadata></head><body><p begin="1">real line</p></body></tt>`
	if !hasLyricContent(withLyrics, "ttml") {
		t.Fatal("body text must count as lyric content")
	}
	bodyless := `<tt>bare line</tt>`
	if !hasLyricContent(bodyless, "ttml") {
		t.Fatal("documents without a body fall back to any character data")
	}
}

func TestCandidateRequiresLyricsBeyondMetadata(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, text, format string
		present            bool
	}{
		{"LRC tags", "[ar:Artist]\n[ti:Song]\n[00:01.00]", "lrc", false},
		{"ELRC tags", "[offset:100]\n[00:01.00]<00:01.00>", "elrc", false},
		{"CRLF and adjacent tags", "[AR:Artist][TI:Song]\r\n[00:01.00]\r\n", "lrc", false},
		{"bodyless metadata", "<tt><head><metadata>Song</metadata></head></tt>", "ttml", false},
		{"nested metadata", "<tt><metadata><span>Song</span></metadata><p>real lyric</p></tt>", "ttml", true},
		{"bodyless lyric", "<tt>real lyric</tt>", "ttml", true},
		{"plain bracket text", "[ar:Artist]", "plain", true},
		{"plain timestamp text", "[00:01.00]", "plain", true},
		{"LRC lyric after tags", "[ar:Artist]\n[00:01.00]real lyric", "lrc", true},
		{"LRC section", "[00:01.00][Verse: one]", "lrc", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := NewCandidate(lyrics.LyricsText{Text: test.text}, "stub", test.format)
			if candidate.Present() != test.present {
				t.Fatalf("Present() = %v, want %v", candidate.Present(), test.present)
			}
			if !test.present && !EmptyDocument(test.text, test.format) {
				t.Fatal("metadata-only payload should be an empty document")
			}
		})
	}
}
