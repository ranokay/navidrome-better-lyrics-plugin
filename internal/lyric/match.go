package lyric

import (
	"math"
	"regexp"
	"strings"
	"sync"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// MatchStatus is the recording-identity assessment, independent of timing
// richness and content state. Unassessed is reserved for sources that expose no
// recording metadata at all; assessed incomplete data is ambiguous.
type MatchStatus string

const (
	MatchUnassessed MatchStatus = "unassessed"
	MatchValidated  MatchStatus = "validated"
	MatchAmbiguous  MatchStatus = "ambiguous"
	MatchMismatch   MatchStatus = "mismatch"
)

// The tight band is the existing two-second provider contract. The drift bound
// is corpus evidence: correct recordings in the matching corpus differ by up to
// 4.0 s across releases; the first markerless wrong recording is 45 s away.
// Duration is a validation ceiling only: it never proves a different recording.
const (
	validatedDurationSeconds = 2.0
	recordingDriftSeconds    = 5.0
	maxMatchReasons          = 8
)

// RecordingInfo is the provider-supplied recording metadata used for identity
// assessment. Empty fields mean the source did not return them.
type RecordingInfo struct {
	Title    string  `json:"title,omitempty"`
	Artist   string  `json:"artist,omitempty"`
	Album    string  `json:"album,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// Empty reports whether the source returned no recording metadata.
func (info RecordingInfo) Empty() bool {
	return strings.TrimSpace(info.Title) == "" && strings.TrimSpace(info.Artist) == "" &&
		strings.TrimSpace(info.Album) == "" && info.Duration <= 0
}

// RecordingMatch is the bounded, reasoned identity assessment. It carries no
// confidence score: the corpus does not calibrate one.
type RecordingMatch struct {
	Status        MatchStatus `json:"status"`
	Reasons       []string    `json:"reasons,omitempty"`
	DurationDelta float64     `json:"durationDelta,omitempty"`
	Recording     string      `json:"recording,omitempty"`
	AutoSave      bool        `json:"autoSaveEligible"`
}

// Playable reports whether the candidate may be served or selected
// automatically. Ambiguous suggestions remain manually selectable only.
func (match RecordingMatch) Playable() bool {
	return match.Status == MatchValidated || match.Status == MatchUnassessed
}

// RequestRecordingInfo builds the comparison side from the playback track.
func RequestRecordingInfo(track lyrics.TrackInfo) RecordingInfo {
	return RecordingInfo{
		Title:    strings.TrimSpace(track.Title),
		Artist:   TrackArtist(track),
		Album:    strings.TrimSpace(track.Album),
		Duration: float64(track.Duration),
	}
}

// AssessRecording compares returned metadata with the request. Exact title and
// artist are necessary but not sufficient: corroboration (duration, album or
// agreeing explicit version markers) is required before validation. Duration
// alone never produces a mismatch.
func AssessRecording(request, returned RecordingInfo) RecordingMatch {
	match := RecordingMatch{Status: MatchAmbiguous}
	reasons := make([]string, 0, maxMatchReasons)
	add := func(reason string) {
		if len(reasons) < maxMatchReasons {
			reasons = append(reasons, reason)
		}
	}

	returnedTitle := strings.TrimSpace(returned.Title)
	returnedArtist := strings.TrimSpace(returned.Artist)
	if returnedTitle == "" || returnedArtist == "" {
		add("missing-recording-metadata")
		if request.Duration > 0 && returned.Duration > 0 {
			match.DurationDelta = math.Abs(returned.Duration - request.Duration)
		}
		match.Reasons = reasons
		return match
	}

	requestBase := baseTitleTokens(request.Title)
	returnedBase := baseTitleTokens(returnedTitle)
	if len(requestBase) == 0 || len(returnedBase) == 0 || !sameTokens(requestBase, returnedBase) {
		// A shorter/prefix title is not the same recording, but it is not
		// enough to prove a different one either; keep it a suggestion.
		if TokensMatch(requestBase, returnedBase) || TokensMatch(NormalizedTokens(request.Title), NormalizedTokens(returnedTitle)) {
			add("title-prefix")
			match.Reasons = reasons
			return match
		}
		add("title-mismatch")
		match.Status = MatchMismatch
		match.Reasons = reasons
		return match
	}
	if !ArtistMatch(returnedArtist, request.Artist) {
		add("artist-mismatch")
		match.Status = MatchMismatch
		match.Reasons = reasons
		return match
	}

	requestMarkers := versionMarkers(request.Title, request.Album)
	returnedMarkers := versionMarkers(returnedTitle, returned.Album)
	markerRelation := compareMarkers(requestMarkers, returnedMarkers)
	switch markerRelation {
	case markersConflict:
		add("version-conflict")
		match.Status = MatchMismatch
		match.Reasons = reasons
		return match
	case markersOneSided:
		add("version-unconfirmed")
	case markersAgree:
		add("version-agrees")
	}

	if request.Duration > 0 && returned.Duration > 0 {
		match.DurationDelta = math.Abs(returned.Duration - request.Duration)
		if match.DurationDelta > recordingDriftSeconds {
			add("duration-outside-corpus-drift")
			match.Reasons = reasons
			return match
		}
	}

	// Exact title and artist are established above. Validation additionally
	// needs one independent corroborator; duration inside the tight band is one,
	// an equal album is another, and agreeing explicit version markers are a
	// third. A missing corroborator stays ambiguous rather than claiming
	// discrimination the metadata cannot support.
	corroborated := false
	if request.Duration > 0 && returned.Duration > 0 && match.DurationDelta <= validatedDurationSeconds {
		corroborated = true
		add("duration-within-2s")
	}
	if request.Album != "" && returned.Album != "" && sameCollapsed(request.Album, returned.Album) {
		corroborated = true
		add("album-exact")
	}
	if markerRelation == markersAgree {
		corroborated = true
	}
	if !corroborated {
		add("insufficient-corroboration")
		match.Reasons = reasons
		return match
	}
	if markerRelation == markersOneSided {
		match.Reasons = reasons
		return match
	}

	match.Status = MatchValidated
	match.AutoSave = true
	match.Reasons = reasons
	return match
}

type markerRelation uint8

const (
	markersNone markerRelation = iota
	markersAgree
	markersOneSided
	markersConflict
)

// compareMarkers distinguishes explicit agreement from one-sided or
// contradictory markers. A blank request marker is not a studio assertion.
func compareMarkers(request, returned map[string]bool) markerRelation {
	if len(request) == 0 && len(returned) == 0 {
		return markersNone
	}
	if len(request) == 0 || len(returned) == 0 {
		return markersOneSided
	}
	for marker := range request {
		if !returned[marker] {
			return markersConflict
		}
	}
	for marker := range returned {
		if !request[marker] {
			return markersConflict
		}
	}
	return markersAgree
}

// versionMarkers extracts recording-variant markers. Content rating
// (clean/explicit) and edition markers (remaster/deluxe) are deliberately not
// recording identity.
func versionMarkers(title, album string) map[string]bool {
	lower := strings.ToLower(qualifierText(title) + " " + album)
	markers := make(map[string]bool)
	for _, marker := range recordingMarkers {
		if markerPattern(marker).MatchString(lower) {
			markers[marker] = true
		}
	}
	return markers
}

// qualifierText keeps only the parts of a title that can carry a version
// qualifier: bracketed segments and a dash suffix. A plain title such as
// "Live Forever" is a song name, not a live-recording assertion.
func qualifierText(title string) string {
	parts := bracketedSuffix.FindAllString(title, -1)
	if match := dashSuffix.FindString(title); match != "" {
		parts = append(parts, match)
	}
	return strings.Join(parts, " ")
}

var recordingMarkers = []string{
	"live", "studio", "acoustic", "unplugged", "remix", "remixed", "radio edit",
	"edit", "extended", "instrumental", "karaoke", "demo", "cover", "drumless",
	"a cappella", "acappella", "orchestral", "rehearsal", "session", "mono",
	"stereo", "sped up", "slowed", "nightcore", "reprise", "alternate take",
}

var markerPatternCache sync.Map

func markerPattern(marker string) *regexp.Regexp {
	if pattern, ok := markerPatternCache.Load(marker); ok {
		return pattern.(*regexp.Regexp)
	}
	pattern := regexp.MustCompile(`(?:^|[^a-z0-9])` + regexp.QuoteMeta(marker) + `(?:$|[^a-z0-9])`)
	actual, _ := markerPatternCache.LoadOrStore(marker, pattern)
	return actual.(*regexp.Regexp)
}

var bracketedSuffix = regexp.MustCompile(`\s*[\(\[\{][^\)\]\}]*[\)\]\}]`)
var dashSuffix = regexp.MustCompile(`\s+[-–—]\s+.*$`)

// baseTitleTokens removes version qualifiers before comparing the song title.
// It intentionally does not preserve prefix tolerance: a shorter title is not
// the same recording.
func baseTitleTokens(value string) []string {
	base := strings.TrimSpace(value)
	base = cutTitleQualifier(base)
	return NormalizedTokens(base)
}

func cutTitleQualifier(value string) string {
	cut := len(value)
	for _, marker := range []string{" (", " [", " {", " - ", " – ", " — "} {
		if index := strings.Index(value, marker); index >= 0 && index < cut {
			cut = index
		}
	}
	return strings.TrimSpace(value[:cut])
}

func sameTokens(left, right []string) bool {
	if len(left) != len(right) || len(left) == 0 {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameCollapsed(left, right string) bool {
	return OnlyLettersAndDigits(strings.ToLower(left)) == OnlyLettersAndDigits(strings.ToLower(right))
}
