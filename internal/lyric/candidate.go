package lyric

import (
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
// is a clean miss.
type Candidate struct {
	Response lyrics.GetLyricsResponse
	Quality  TimingQuality
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
	}
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
