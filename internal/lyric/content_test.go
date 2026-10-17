package lyric

import (
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

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
