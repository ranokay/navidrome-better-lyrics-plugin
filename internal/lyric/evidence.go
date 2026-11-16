package lyric

import "strings"

// EvidenceKind names a typed, attributed provider observation. Instrumental is
// the only current kind; it is never inferred from an empty or failed response.
type EvidenceKind string

const EvidenceInstrumental EvidenceKind = "instrumental"

// Evidence is a provider assertion about a recording. It is separate from
// playable lyric entries: an assertion can exist without lyrics and never
// vetoes lyrics from another source or the same response.
type Evidence struct {
	Kind      EvidenceKind   `json:"kind"`
	Provider  string         `json:"provider,omitempty"`
	Recording string         `json:"recording,omitempty"`
	Match     RecordingMatch `json:"match"`
	Reason    string         `json:"reason,omitempty"`
}

// EvidenceFor builds a bounded instrumental assertion from a decoded provider
// flag. Callers must only construct this after an explicit provider assertion;
// empty/error paths must not call it.
func EvidenceFor(provider, recording string, info RecordingInfo, match RecordingMatch) Evidence {
	return Evidence{
		Kind:      EvidenceInstrumental,
		Provider:  boundedEvidenceText(provider, 64),
		Recording: boundedEvidenceText(recording, MaxVariantRecordingBytes),
		Match:     match,
		Reason:    boundedEvidenceText("provider declared instrumental", 160),
	}
}

func boundedEvidenceText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit])
}

// SameEvidence deduplicates cached and freshly fetched assertions.
func SameEvidence(left, right Evidence) bool {
	return left.Kind == right.Kind && left.Provider == right.Provider && left.Recording == right.Recording
}
