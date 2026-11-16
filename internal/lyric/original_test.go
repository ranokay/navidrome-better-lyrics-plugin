package lyric

import (
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestNewOriginalPreservesSuppliedDocuments(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"ttml", "lrc", "elrc", "qrc", "krc", "yrc", "lyl", "wsy", "lyricsfile", "plain"} {
		document := []byte("exact " + format + " document")
		if format == "ttml" {
			document = []byte("<tt><body><p begin=\"1\" end=\"2\">line</p></body></tt>")
		}
		original, ok := NewOriginal(format, OriginalSupplied, document)
		if !ok || original.Format != format || string(original.Bytes) != string(document) {
			t.Fatalf("format %q: original = %#v/%v, want exact preserved bytes", format, original, ok)
		}
		info := original.Info()
		if !info.Available || info.MediaType == "" || info.Extension == "" {
			t.Fatalf("format %q: info = %#v, want an available typed document", format, info)
		}
	}

	// Enhanced LRC is not blacklisted: a source-supplied document is valid.
	if _, ok := NewOriginal("elrc", OriginalSupplied, []byte("[00:01.00]line")); !ok {
		t.Fatal("source-supplied ELRC must be a valid original")
	}
}

func TestNewOriginalRejectsReconstructionsAndUnknownFormats(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		format     string
		provenance OriginalProvenance
		document   []byte
	}{
		{"reconstructed", "elrc", OriginalReconstructed, []byte("[00:01.00]line")},
		{"provider conversion", "elrc", OriginalProvenance("provider-conversion"), []byte("[00:01.00]line")},
		{"unavailable provenance", "ttml", OriginalUnavailable, []byte("<tt/>")},
		{"unknown format", "srt", OriginalSupplied, []byte("1")},
		{"empty document", "ttml", OriginalSupplied, nil},
		{"oversized document", "ttml", OriginalSupplied, make([]byte, MaxDocumentBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := NewOriginal(tc.format, tc.provenance, tc.document); ok {
				t.Fatalf("format %q provenance %q must be rejected", tc.format, tc.provenance)
			}
		})
	}

	missing := UnavailableOriginalInfo("")
	if missing.Available || missing.Reason != OriginalReasonNoDocument {
		t.Fatalf("unavailable info = %#v, want an explicit reason", missing)
	}
	if OriginalMediaType("bogus") != "" || OriginalExtension("bogus") != "" {
		t.Fatal("unknown formats must not produce headers")
	}
	if !strings.HasPrefix(OriginalMediaType("ttml"), "application/") {
		t.Fatalf("ttml media type = %q", OriginalMediaType("ttml"))
	}
	if got := OriginalMediaType("lyricsfile"); got != "application/yaml" {
		t.Fatalf("lyricsfile media type = %q, want application/yaml", got)
	}
	if got := OriginalExtension("lyricsfile"); got != ".yaml" {
		t.Fatalf("lyricsfile extension = %q, want .yaml", got)
	}
}

func TestCandidateRecordingIdentityFallsBackToContent(t *testing.T) {
	t.Parallel()

	candidate := NewCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "Provider", "lrc")
	first := candidate.RecordingIdentity()
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("recording = %q, want a content digest when the provider has no id", first)
	}
	withProviderID := candidate.WithRecording("rec-1").RecordingIdentity()
	if !strings.HasPrefix(withProviderID, "rec-1@") {
		t.Fatalf("recording = %q, want the provider id prefix plus a revision digest", withProviderID)
	}
	changedWithProviderID := NewCandidate(lyrics.LyricsText{Text: "[00:01.00]different line"}, "Provider", "lrc").WithRecording("rec-1")
	if changedWithProviderID.RecordingIdentity() == withProviderID {
		t.Fatal("a changed document must change the provider-recording address")
	}
	if candidate.WithRecording("").RecordingIdentity() != first {
		t.Fatal("content recording identity must be deterministic")
	}
	changed := NewCandidate(lyrics.LyricsText{Text: "[00:01.00]different line"}, "Provider", "lrc")
	if changed.RecordingIdentity() == first {
		t.Fatal("different content must not share a recording identity")
	}

	document, ok := NewOriginal("ttml", OriginalSupplied, []byte("<tt>native</tt>"))
	if !ok {
		t.Fatal("fixture original rejected")
	}
	withDocument := candidate.WithOriginal("ttml", OriginalSupplied, document.Bytes)
	if withDocument.RecordingIdentity() == first {
		t.Fatal("the native document, not the playback reconstruction, must seed the identity")
	}
}

func TestNewOriginalValidatesTTMLIndependently(t *testing.T) {
	t.Parallel()

	validWithWhitespace := "  <tt><body><p begin=\"1\" end=\"2\">line</p></body></tt>\n"
	original, ok := NewOriginal("ttml", OriginalSupplied, []byte(validWithWhitespace))
	if !ok || string(original.Bytes) != validWithWhitespace {
		t.Fatalf("whitespace-wrapped TTML = %#v/%v, want exact bytes preserved", original, ok)
	}

	overdepth := "<tt><body>" + strings.Repeat("<div>", maxXMLDepth) + "<p begin=\"1\" end=\"2\">line</p>" + strings.Repeat("</div>", maxXMLDepth) + "</body></tt>"
	cases := map[string]string{
		"malformed":   "<tt><body><p begin=\"1\" end=\"2\">line",
		"non-ttml":    "not a ttml document",
		"contentless": "<tt><body><div></div></body></tt>",
		"overdepth":   overdepth,
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := NewOriginal("ttml", OriginalSupplied, []byte(document)); ok {
				t.Fatalf("%s TTML must not be a valid original", name)
			}
			candidate := NewCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "Provider", "elrc").
				WithOriginal("ttml", OriginalSupplied, []byte(document))
			if candidate.Original != nil {
				t.Fatalf("%s TTML must not be attached as supplied", name)
			}
			if got := candidate.OriginalDetails().Reason; got != OriginalReasonInvalidDocument {
				t.Fatalf("%s reason = %q, want the explicit invalid-document reason", name, got)
			}
		})
	}
}

func TestNewCandidateRetainsSourceLanguage(t *testing.T) {
	t.Parallel()

	candidate := NewCandidate(lyrics.LyricsText{Lang: " ja ", Text: "[00:01.00]line"}, "Provider", "lrc")
	if candidate.Language != "ja" {
		t.Fatalf("language = %q, want the adapter-supplied language", candidate.Language)
	}
}
