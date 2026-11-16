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

// RecordingIdentity resolves the recording address: the provider id when
// supplied, otherwise a content digest over the exact native document (or the
// served text when no document is known). A refetch that returns different
// bytes therefore cannot silently match this selection.
func (candidate Candidate) RecordingIdentity() string {
	if recording := strings.TrimSpace(candidate.Recording); recording != "" {
		return recording
	}
	digest := sha256.New()
	if candidate.Original != nil && len(candidate.Original.Bytes) > 0 {
		digest.Write(candidate.Original.Bytes)
	} else {
		for _, text := range candidate.Response.Lyrics {
			digest.Write([]byte(text.Text))
			digest.Write([]byte{0})
		}
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil))
}

// Present reports whether the candidate carries servable lyrics.
func (candidate Candidate) Present() bool {
	return len(candidate.Response.Lyrics) > 0
}

// Prefer keeps the better of two candidates; ties keep the current one so the
// earlier source in configured order wins.
func Prefer(current, next Candidate) Candidate {
	if !next.Present() {
		return current
	}
	if !current.Present() || next.Quality > current.Quality {
		return next
	}
	return current
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
