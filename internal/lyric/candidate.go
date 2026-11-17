package lyric

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// TimingQuality grades how richly a payload is timed: unsynced text, timed
// lines, timed words, or timed syllables.
type TimingQuality uint8

const (
	TimingUnsynced TimingQuality = iota
	TimingLine
	TimingWord
	TimingSyllable
)

// Candidate is a servable lyrics payload with its grading; the zero candidate
// is a clean miss. Identity and OriginalSummary are filled by the provider once
// the configured source instance is known; adapters supply the recording hint
// and the native document.
type Candidate struct {
	Response lyrics.GetLyricsResponse
	Quality  TimingQuality

	// Recording is the provider record id, when the wire supplies one.
	Recording string
	Language  string

	// Identity names this exact playback representation. Nil means the
	// candidate predates identity support or no stable identity exists.
	Identity *VariantIdentity
	// Original is the live native document captured by the adapter.
	Original *Original
	// OriginalSummary records a cached original's availability without bytes.
	OriginalSummary OriginalInfo

	// RecordingInfo is the returned recording metadata used for identity
	// assessment. Zero means the source exposes none; the candidate stays
	// unassessed and legacy-playable but is never auto-save eligible.
	RecordingInfo RecordingInfo
	// Match is the recording-identity assessment, independent of timing.
	Match RecordingMatch
	// Evidence carries attributed provider assertions (currently instrumental)
	// even when the candidate has no playable lyrics.
	Evidence []Evidence
}

// Playable reports whether the candidate may be served or selected
// automatically. Ambiguous suggestions remain manually selectable only.
func (candidate Candidate) Playable() bool {
	return candidate.Present() && candidate.Match.Playable()
}

// AutomaticSaveEligible reports whether this candidate may feed the future
// automatic-save path. Only a validated identity qualifies.
func (candidate Candidate) AutomaticSaveEligible() bool {
	return candidate.Present() && candidate.Match.Status == MatchValidated && candidate.Match.AutoSave
}

// NewCandidate grades one source payload; contentless or malformed payloads
// yield the zero candidate so they cannot outrank usable fallbacks.
func NewCandidate(text lyrics.LyricsText, provider, format string) Candidate {
	if len(text.Text) > MaxDocumentBytes {
		return Candidate{}
	}
	quality := TimingUnsynced
	if format != "ttml" && !hasLyricContent(text.Text, format) {
		return Candidate{}
	}
	switch format {
	case "ttml":
		var valid, hasContent bool
		quality, hasContent, valid = inspectTTMLTimingQuality(text.Text)
		if !valid || !hasContent {
			return Candidate{}
		}
	case "lrc", "elrc":
		if lrcTimestampPattern.MatchString(text.Text) {
			quality = TimingLine
		}
		if enhancedWordPattern.MatchString(text.Text) {
			quality = TimingWord
		}
	}
	return Candidate{
		Response: sourcedResponse(text, provider, format),
		Quality:  quality,
		Language: strings.TrimSpace(text.Lang),
		Match:    RecordingMatch{Status: MatchUnassessed},
	}
}

// WithOriginal attaches a verbatim native document captured before conversion.
func (candidate Candidate) WithOriginal(format string, provenance OriginalProvenance, document []byte) Candidate {
	original, ok := NewOriginal(format, provenance, document)
	if !ok {
		reason := OriginalReasonNoDocument
		if len(document) > MaxDocumentBytes {
			reason = OriginalReasonTooLarge
		} else if strings.EqualFold(strings.TrimSpace(format), "ttml") && len(document) > 0 {
			reason = OriginalReasonInvalidDocument
		}
		candidate.Original = nil
		candidate.OriginalSummary = UnavailableOriginalInfo(reason)
		return candidate
	}
	candidate.Original = original
	return candidate
}

// WithOriginalUnavailable records why a candidate has no retrievable original.
func (candidate Candidate) WithOriginalUnavailable(reason string) Candidate {
	candidate.Original = nil
	candidate.OriginalSummary = UnavailableOriginalInfo(reason)
	return candidate
}

// WithRecording records the provider record id used for identity addressing.
func (candidate Candidate) WithRecording(recording string) Candidate {
	candidate.Recording = strings.TrimSpace(recording)
	return candidate
}

// WithRecordingInfo attaches the returned recording metadata used for identity
// assessment.
func (candidate Candidate) WithRecordingInfo(info RecordingInfo) Candidate {
	candidate.RecordingInfo = info
	return candidate
}

// Format is the served playback format.
func (candidate Candidate) Format() string {
	if candidate.Response.Source != nil {
		return strings.ToLower(strings.TrimSpace(candidate.Response.Source.Format))
	}
	for _, text := range candidate.Response.Lyrics {
		if text.Source != nil {
			return strings.ToLower(strings.TrimSpace(text.Source.Format))
		}
	}
	return ""
}

// Provider is the served upstream label.
func (candidate Candidate) Provider() string {
	if candidate.Response.Source != nil {
		return strings.TrimSpace(candidate.Response.Source.Provider)
	}
	for _, text := range candidate.Response.Lyrics {
		if text.Source != nil {
			return strings.TrimSpace(text.Source.Provider)
		}
	}
	return ""
}

// OriginalDetails returns the live or cached original summary.
func (candidate Candidate) OriginalDetails() OriginalInfo {
	if candidate.Original != nil {
		return candidate.Original.Info()
	}
	if candidate.OriginalSummary.Available || candidate.OriginalSummary.Reason != "" {
		return candidate.OriginalSummary
	}
	return UnavailableOriginalInfo(OriginalReasonNoDocument)
}

// RecordingIdentity resolves the recording address. A provider id is kept as
// an opaque prefix, but a content-revision digest is always appended, so a
// provider editing a document cannot resolve under an earlier selection. With
// no provider id the whole address is a content digest. A refetch that returns
// different bytes therefore cannot silently match this selection.
func (candidate Candidate) RecordingIdentity() string {
	digest := candidate.contentDigest()
	if recording := strings.TrimSpace(candidate.Recording); recording != "" {
		base := recording
		if len(base) > 90 {
			// Keep the address bounded when a provider returns a long URL-like
			// id; retain a stable hash of it instead of truncating ambiguously.
			hashed := sha256.Sum256([]byte(base))
			base = fmt.Sprintf("id:%x", hashed[:16])
		}
		return fmt.Sprintf("%s@%x", base, digest[:16])
	}
	return fmt.Sprintf("sha256:%x", digest)
}

// contentDigest covers the exact native document, or the served text when no
// document is known.
func (candidate Candidate) contentDigest() [sha256.Size]byte {
	digest := sha256.New()
	if candidate.Original != nil && len(candidate.Original.Bytes) > 0 {
		digest.Write(candidate.Original.Bytes)
	} else {
		for _, text := range candidate.Response.Lyrics {
			digest.Write([]byte(text.Text))
			digest.Write([]byte{0})
		}
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

// Present reports whether the candidate carries servable lyrics.
func (candidate Candidate) Present() bool {
	return len(candidate.Response.Lyrics) > 0
}

// Prefer keeps the better of two candidates for playback: recording validity
// ranks before timing richness, and ties keep the current one so the earlier
// source in configured order wins. Ambiguous candidates never displace a
// playable one; they remain separately listable suggestions.
func Prefer(current, next Candidate) Candidate {
	if MorePreferred(current, next) {
		return next
	}
	return current
}

// AtLeastAsPreferred reports whether next should replace current on a cache
// refresh: validity first, timing richness second, equal timing allowed.
func AtLeastAsPreferred(current, next Candidate) bool {
	if !next.Present() || !next.Playable() {
		return false
	}
	if !current.Present() {
		return true
	}
	currentRank, nextRank := playbackRank(current), playbackRank(next)
	if nextRank != currentRank {
		return nextRank > currentRank
	}
	return next.Quality >= current.Quality
}

// MorePreferred reports whether next strictly outranks current for playback:
// validity first, timing richness second, earlier position on ties.
func MorePreferred(current, next Candidate) bool {
	if !next.Present() || !next.Playable() {
		return false
	}
	if !current.Present() {
		return true
	}
	currentRank, nextRank := playbackRank(current), playbackRank(next)
	if nextRank != currentRank {
		return nextRank > currentRank
	}
	return next.Quality > current.Quality
}

// PreferSuggestion orders ambiguous suggestions for the listing only. It never
// makes a suggestion playable.
func PreferSuggestion(current, next Candidate) Candidate {
	if !next.Present() {
		return current
	}
	if !current.Present() || next.Quality > current.Quality {
		return next
	}
	return current
}

func playbackRank(candidate Candidate) int {
	if candidate.Match.Status == MatchValidated {
		return 2
	}
	if candidate.Match.Playable() {
		return 1
	}
	return 0
}

func sourcedResponse(text lyrics.LyricsText, provider, format string) lyrics.GetLyricsResponse {
	return lyrics.GetLyricsResponse{
		Lyrics: []lyrics.LyricsText{text},
		Source: &lyrics.LyricsSource{
			Provider: provider,
			Format:   format,
		},
	}
}
