package lyric

import "strings"

// OriginalProvenance records where a document came from. Provenance, not the
// format name, decides whether bytes may be labelled an original: an ELRC
// document supplied by a source is genuine, while a reconstruction never is.
type OriginalProvenance string

const (
	OriginalSupplied           OriginalProvenance = "supplied"
	OriginalProviderConversion OriginalProvenance = "provider-conversion"
	OriginalReconstructed      OriginalProvenance = "reconstructed"
	OriginalUnavailable        OriginalProvenance = "unavailable"
)

// Reasons reported when a variant has no retrievable original.
const (
	OriginalReasonNoDocument      = "source supplies no native document"
	OriginalReasonConversionOnly  = "provider supplies only a converted representation"
	OriginalReasonReconstructed   = "only an API envelope or reconstruction was available"
	OriginalReasonTooLarge        = "document exceeds the cache entry limit"
	OriginalReasonInvalidDocument = "provider supplied no valid native document"
	OriginalReasonCacheRefused    = "cache refused the document"
)

// Original is one exact source-native document captured before conversion.
type Original struct {
	Format     string             `json:"format"`
	Provenance OriginalProvenance `json:"provenance"`
	Bytes      []byte             `json:"document,omitempty"`
}

// OriginalInfo is the bounded listing summary for an original.
type OriginalInfo struct {
	Available  bool               `json:"available"`
	Format     string             `json:"format,omitempty"`
	Provenance OriginalProvenance `json:"provenance,omitempty"`
	MediaType  string             `json:"mediaType,omitempty"`
	Extension  string             `json:"extension,omitempty"`
	Size       int                `json:"size,omitempty"`
	Reason     string             `json:"reason,omitempty"`
}

// NewOriginal validates a document that must be preserved verbatim.
func NewOriginal(format string, provenance OriginalProvenance, bytes []byte) (*Original, bool) {
	format = strings.ToLower(strings.TrimSpace(format))
	if !documentFormat(format) {
		return nil, false
	}
	switch provenance {
	case OriginalSupplied, OriginalProviderConversion:
	default:
		return nil, false
	}
	if len(bytes) == 0 || len(bytes) > MaxDocumentBytes {
		return nil, false
	}
	// TTML originals must be independently valid documents: a provider can
	// return a valid converted representation alongside a malformed, empty or
	// overly deep native document. Validation trims only insignificant outer
	// whitespace; the stored bytes stay exact.
	if format == "ttml" && !ValidTTMLDocument(string(bytes)) {
		return nil, false
	}
	return &Original{Format: format, Provenance: provenance, Bytes: bytes}, true
}

// ValidTTMLDocument reports a bounded, well-formed TTML document with lyric
// content. It validates the document itself, independent of which playback
// representation was selected.
func ValidTTMLDocument(document string) bool {
	if len(document) > MaxDocumentBytes {
		return false
	}
	_, content, valid := inspectTTMLTimingQuality(strings.TrimSpace(document))
	return valid && content
}

// Info summarizes a present or missing original.
func (original *Original) Info() OriginalInfo {
	if original == nil {
		return UnavailableOriginalInfo(OriginalReasonNoDocument)
	}
	return OriginalInfo{
		Available:  true,
		Format:     original.Format,
		Provenance: original.Provenance,
		MediaType:  OriginalMediaType(original.Format),
		Extension:  OriginalExtension(original.Format),
		Size:       len(original.Bytes),
	}
}

// UnavailableOriginalInfo builds an explicit unavailable summary.
func UnavailableOriginalInfo(reason string) OriginalInfo {
	if strings.TrimSpace(reason) == "" {
		reason = OriginalReasonNoDocument
	}
	return OriginalInfo{Available: false, Reason: reason}
}

// OriginalMediaType maps a validated document format to a safe media type.
func OriginalMediaType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "ttml":
		return "application/ttml+xml"
	case "lrc", "elrc", "krc", "yrc", "lyl":
		return "text/plain; charset=utf-8"
	case "qrc":
		return "application/xml"
	case "wsy":
		return "application/octet-stream"
	case "lyricsfile":
		return "application/json"
	case "plain":
		return "text/plain; charset=utf-8"
	}
	return ""
}

// OriginalExtension maps a validated document format to a safe extension.
func OriginalExtension(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "ttml":
		return ".ttml"
	case "lrc", "elrc":
		return ".lrc"
	case "qrc":
		return ".qrc"
	case "krc":
		return ".krc"
	case "yrc":
		return ".yrc"
	case "lyl":
		return ".lyl"
	case "wsy":
		return ".wsy"
	case "lyricsfile":
		return ".json"
	case "plain":
		return ".txt"
	}
	return ""
}

// documentFormat lists every native document format the plugin can preserve.
func documentFormat(format string) bool {
	switch format {
	case "ttml", "lrc", "elrc", "qrc", "krc", "yrc", "lyl", "wsy", "lyricsfile", "plain":
		return true
	}
	return false
}
