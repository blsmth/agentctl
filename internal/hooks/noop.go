package hooks

import (
	"io"
	"os"

	"github.com/blsmth/agentctl/internal/logging"
)

// Noop is a generic no-op handler for hook events that aren't yet implemented.
// It drains stdin and logs the event name for diagnostics.
func Noop(eventName string) {
	io.Copy(io.Discard, os.Stdin) //nolint:errcheck
	if os.Getenv("AGENTCTL") != "1" {
		return
	}
	logging.Log("hook-%s: no-op (not yet implemented)", eventName)
}
