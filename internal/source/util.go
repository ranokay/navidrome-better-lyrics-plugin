package source

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex returns n cryptographically random bytes as hex text; sources use
// it for request nonces and imitation client identifiers.
func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
