package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
)

// runBackendToken implements the hidden `agentctl backend-token <name>`
// command, the target of the apiKeyHelper that BuildAgentSettingsJSON writes
// for nous backends. Claude Code runs it before API calls and treats any
// extra stdout as part of the key, so the contract is strict: on success
// stdout is exactly "<key>\n"; on failure stdout stays empty, a short message
// goes to stderr, and the exit code is 1. main dispatches it before any
// bootstrap or logging, and nothing here writes to stdout but the key.
func runBackendToken(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: agentctl backend-token <name>")
		return 1
	}
	name := args[0]
	if err := config.Init(); err != nil {
		_, _ = fmt.Fprintf(stderr, "agentctl backend-token: %v\n", err)
		return 1
	}
	b, err := config.Load().ResolveBackend(name)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentctl backend-token: %v\n", err)
		return 1
	}
	if !b.IsNous() {
		_, _ = fmt.Fprintf(stderr, "agentctl backend-token: backend %q has no stored credential (kind %q)\n", name, b.Kind)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	key, err := backendauth.Resolve(ctx, name, b)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentctl backend-token: %v\n", err)
		return 1
	}
	_, _ = io.WriteString(stdout, key+"\n")
	return 0
}
