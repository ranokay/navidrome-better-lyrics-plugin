package lyric

import (
	"testing"
)

func TestArtistMatchRequiresCompleteNames(t *testing.T) {
	t.Parallel()

	// Prefix tolerance belongs to titles only: "Pink" must not match
	// "Pink Floyd".
	if ArtistMatch("Pink Floyd", "Pink") {
		t.Fatal("Pink matched Pink Floyd")
	}
	if !ArtistMatch("Pink Floyd", "Pink Floyd") {
		t.Fatal("exact artist names must match")
	}
	// Collaborator splitting still works in both directions.
	if !ArtistMatch("Oasis", "Oasis & Someone") {
		t.Fatal("a listed collaborator must match the plain artist")
	}
	if !ArtistMatch("Simon & Garfunkel", "Simon & Garfunkel") {
		t.Fatal("identical collaboration lists must match")
	}
	if ArtistMatch("Oasis Worship", "Oasis") {
		t.Fatal("Oasis matched Oasis Worship")
	}
}

func TestArtistMatchFailsClosedWithoutNameTokens(t *testing.T) {
	t.Parallel()

	// A name that collapses to no alphanumerics identifies nothing: it must
	// not alias every returned artist (an empty collaborator split used to
	// accept any row).
	if ArtistMatch("Oasis", "???") {
		t.Fatal("a punctuation-only wanted artist matched Oasis")
	}
	if ArtistMatch("???", "???") {
		t.Fatal("two punctuation-only artists collapsed into a match")
	}
	if ArtistMatch("???", "Oasis") {
		t.Fatal("a punctuation-only returned artist matched Oasis")
	}
}

func TestArtistMatchPunctuatedNames(t *testing.T) {
	t.Parallel()

	// Whole-name equality must precede collaborator splitting: AC/DC and
	// Simon & Garfunkel collapse to single names and matched before the
	// shared matcher arrived.
	cases := []struct{ returned, wanted string }{
		{"AC/DC", "AC/DC"},
		{"Simon & Garfunkel", "Simon & Garfunkel"},
		{"Simon & Garfunkel", "Garfunkel"},
		// Variant punctuation: the whole-name path catches what splitting
		// would miss.
		{"ACDC", "AC/DC"},
	}
	for _, tc := range cases {
		if !ArtistMatch(tc.returned, tc.wanted) {
			t.Fatalf("ArtistMatch(%q, %q) = false, want true", tc.returned, tc.wanted)
		}
	}
}

func TestNormalizedTokensAndPrefixMatching(t *testing.T) {
	t.Parallel()

	// Punctuation and casing drift must not defeat title matching, and a
	// remaster suffix must match through prefix tolerance.
	if !TokensMatch(NormalizedTokens("Live Forever (Remastered)"), NormalizedTokens("Live Forever")) {
		t.Fatal("a remastered title must match the base title")
	}
	if TokensMatch(NormalizedTokens("Yesterday"), NormalizedTokens("Live Forever")) {
		t.Fatal("unrelated titles must not match")
	}
	if got := OnlyLettersAndDigits("ac/dc!"); got != "acdc" {
		t.Fatalf("OnlyLettersAndDigits() = %q, want punctuation dropped", got)
	}
}
