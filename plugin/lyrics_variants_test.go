package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

// These golden shapes are the plugin side of the lyricsvariants capability
// contract. The host's qualification run decodes the same export through its
// generated types; this test pins the wire keys independently.
func TestVariantExportsGoldenJSON(t *testing.T) {
	t.Parallel()

	identity := lyric.VariantIdentity{
		Version:   1,
		Instance:  "lrclib:0123456789abcdef0123456789abcdef",
		Recording: "1250",
		Language:  "en",
		Role:      "main",
		Format:    "lrc",
	}
	v2 := getLyricsV2ResponseWire{
		Lyrics: []lyricsTextWire{{
			Lang:    "en",
			Text:    "[00:01.00]line",
			Source:  &lyricsSourceWire{Provider: "LRCLib", Format: "lrc"},
			Variant: &variantWire{Version: 1, Instance: identity.Instance, Recording: "1250", Language: "en", Role: "main", Format: "lrc"},
		}},
		Source: &lyricsSourceWire{Provider: "LRCLib", Format: "lrc"},
	}
	encoded, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"lyrics":[{"lang":"en","text":"[00:01.00]line","source":{"provider":"LRCLib","format":"lrc"},"variant":{"version":1,"instance":"lrclib:0123456789abcdef0123456789abcdef","recording":"1250","language":"en","role":"main","format":"lrc"}}],"source":{"provider":"LRCLib","format":"lrc"}}`
	if string(encoded) != want {
		t.Fatalf("v2 JSON = %s\nwant %s", encoded, want)
	}

	variants := getVariantsResponseWire{Variants: []variantSummaryWire{{
		Identity:      variantWire{Version: 1, Instance: identity.Instance, Recording: "1250", Role: "main", Format: "lrc"},
		Provider:      "LRCLib",
		Language:      "en",
		Role:          "main",
		Format:        "lrc",
		PlaybackIndex: 0,
		Original:      originalWire{Available: true, Format: "lyricsfile", Provenance: "supplied", MediaType: "application/json", Extension: ".json", Size: 12},
	}}}
	encoded, err = json.Marshal(variants)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"identity"`, `"playbackIndex"`, `"original"`, `"available":true`, `"provenance":"supplied"`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("listing JSON %s lacks %s", encoded, key)
		}
	}

	document := []byte("<tt>native</tt>")
	original := getOriginalResponseWire{
		Identity:  variantWire{Version: 1, Instance: identity.Instance, Recording: "1250", Role: "main", Format: "lrc"},
		Available: true,
		Format:    "ttml",
		MediaType: "application/ttml+xml",
		Extension: ".ttml",
		Document:  document,
	}
	encoded, err = json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"document":"`+base64.StdEncoding.EncodeToString(document)+`"`) {
		t.Fatalf("original JSON = %s, want the exact document bytes as base64", encoded)
	}
}
