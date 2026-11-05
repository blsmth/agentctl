// Package ids provides UUID generation utilities.
package ids

import (
	"crypto/rand"
	"fmt"
)

// NewUUID generates a random UUID v4 string in the standard 8-4-4-4-12 hex format.
// Uses crypto/rand so no external dependency is needed.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to a zeroed UUID rather than panicking.
		return "00000000-0000-4000-8000-000000000000"
	}
	// Set version 4 (random) and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
