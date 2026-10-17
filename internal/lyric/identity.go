package lyric

import (
	"strings"
	"unicode"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// Metadata identity helpers shared by every source selector: title tokens
// compare with prefix tolerance, artist names with complete-name semantics.

// TrackArtist resolves the artist metadata a lookup search should use: the
// track artist first, then the individual credit list.
func TrackArtist(track lyrics.TrackInfo) string {
	if artist := strings.TrimSpace(track.Artist); artist != "" {
		return artist
	}

	artists := make([]string, 0, len(track.Artists))
	for _, artist := range track.Artists {
		if name := strings.TrimSpace(artist.Name); name != "" {
			artists = append(artists, name)
		}
	}
	return strings.Join(artists, ", ")
}

// NormalizedTokens lowercases and keeps only alphanumeric tokens so
// metadata comparisons resist punctuation and casing drift.
func NormalizedTokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// TokensMatch reports whether one normalized token list is a prefix of the
// other at least one token long: so "Live Forever (Remastered)" matches a
// request for "Live Forever", while an unrelated same-duration song with a
// different title/artist never qualifies.
func TokensMatch(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	for i, token := range a {
		if token != b[i] {
			return false
		}
	}
	return true
}

// ArtistMatch compares artist fields with complete-name semantics: each
// side splits into collaborator names on common separators, and any exact
// collapsed-name equality matches. Prefix tolerance stays for titles only —
// it would equate "Pink" and "Pink Floyd".
func ArtistMatch(returned, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return true
	}
	returned = strings.TrimSpace(returned)
	if returned == "" {
		return false
	}
	wantedCollapsed := OnlyLettersAndDigits(strings.ToLower(wanted))
	// A wanted name that collapses to no alphanumerics ("???") identifies
	// nothing: matching it would alias every returned artist.
	if wantedCollapsed == "" {
		return false
	}
	// Whole-name equality first: "AC/DC" collapses to one name, while the
	// collaborator split below would break it into "ac" and "dc" that can
	// never equal "acdc".
	if OnlyLettersAndDigits(strings.ToLower(returned)) == wantedCollapsed {
		return true
	}
	wantNames := splitArtistNames(wanted)
	for _, rowName := range splitArtistNames(returned) {
		for _, wantName := range wantNames {
			if rowName == wantName {
				return true
			}
		}
	}
	return false
}

// splitArtistNames splits a collaborator list and collapses each name.
func splitArtistNames(value string) []string {
	names := []string{}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case '&', ',', '、', '/', ';', '·', '•', '|', '+':
			return true
		}
		return false
	}) {
		lower := strings.ToLower(part)
		// "feat."/"ft." keep their role words from ever matching a plain name.
		if cleaned := OnlyLettersAndDigits(lower); cleaned != "" {
			names = append(names, cleaned)
		}
	}
	return names
}

// OnlyLettersAndDigits keeps letters and digits for matching while dropping
// whitespace and punctuation. It is unicode-aware because the hub index is
// mostly non-Latin titles (CJK), which ASCII range checks would erase entirely.
func OnlyLettersAndDigits(value string) string {
	var kept []rune
	for _, letter := range value {
		if unicode.IsLetter(letter) || unicode.IsDigit(letter) {
			kept = append(kept, letter)
		}
	}
	return string(kept)
}
