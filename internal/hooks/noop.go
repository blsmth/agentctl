package hooks

import (
	"io"
	"os"
)

// Noop is a generic no-op handler for hook events that aren't yet implemented.
// It drains stdin and logs nothing: it fires for several events on every
// session, so a line per call would be pure noise.
func Noop(_ string) {
	io.Copy(io.Discard, os.Stdin) //nolint:errcheck
}
