package source

import (
	"crypto/rand"
	"encoding/hex"
)

// maxCachedLyricsEntryBytes bounds one KV entry a source persists itself,
// mirroring the provider cache's per-entry cap.
const maxCachedLyricsEntryBytes = 1 * 1024 * 1024

// randomHex returns n cryptographically random bytes as hex text; sources use
// it for request nonces and imitation client identifiers.
func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
