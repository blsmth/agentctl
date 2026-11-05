package hooks

import (
	"encoding/json"
	"io"
	"os"

	"github.com/blsmth/agentctl/internal/logging"
)

// drainStdin reads and discards all remaining stdin to avoid broken pipes.
func drainStdin() {
	io.Copy(io.Discard, os.Stdin) //nolint:errcheck
}

// guardAgentctl checks if we're running inside an agentctl-managed window.
// If not, it drains stdin and returns false. Callers should return immediately
// when this returns false.
func guardAgentctl() bool {
	if os.Getenv("AGENTCTL") != "1" {
		drainStdin()
		return false
	}
	return true
}

// decodeInput reads and decodes JSON from stdin into dst.
// Returns false if decoding fails (already logged). Callers that
// don't care about decode errors can ignore the return value.
func decodeInput(dst interface{}) bool {
	if err := json.NewDecoder(os.Stdin).Decode(dst); err != nil {
		logging.Log("hook: decode stdin: %v", err)
		return false
	}
	return true
}

// resolveAgentName returns the agent name from env vars, falling back to "agent".
// Resolution order: AGENTCTL_INSTANCE_NAME → AGENTCTL_AGENT_NAME → "agent".
func resolveAgentName() string {
	if name := os.Getenv("AGENTCTL_INSTANCE_NAME"); name != "" {
		return name
	}
	if name := os.Getenv("AGENTCTL_AGENT_NAME"); name != "" {
		return name
	}
	return "agent"
}
