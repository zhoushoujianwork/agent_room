package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New returns a compact random identifier with a human-readable prefix.
//
// Room privacy depends entirely on these IDs being unguessable (96 bits of
// entropy), so a failure of the system CSPRNG is fatal rather than something we
// paper over with a predictable fallback.
func New(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("id: crypto/rand unavailable: %v", err))
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b[:]))
}
