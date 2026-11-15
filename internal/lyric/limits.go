package lyric

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
)

// MaxDocumentBytes leaves headroom below Navidrome's 10 MiB HTTP truncation
// limit, including for large provider search responses. Cached inputs use the
// same bound before decoding.
const MaxDocumentBytes = 8 * 1024 * 1024

// Provider lyric XML is shallow; this also bounds ancestor work while grading.
const maxXMLDepth = 128

var errDocumentTooLarge = errors.New("lyric document exceeds size limit")

// checkXMLBounds prevents recursive unmarshaling from constructing an excessive
// tree. TTML uses the same limits in its streaming content/timing inspector.
func checkXMLBounds(data []byte) error {
	if len(data) > MaxDocumentBytes {
		return errDocumentTooLarge
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if roots != 1 {
				return errors.New("expected one lyric XML document")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots > 1 {
					return errors.New("expected one lyric XML document")
				}
			}
			depth++
			if depth > maxXMLDepth {
				return errors.New("lyric XML exceeds depth limit")
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(token)) > 0 {
				return errors.New("text outside lyric XML root")
			}
		}
	}
}
