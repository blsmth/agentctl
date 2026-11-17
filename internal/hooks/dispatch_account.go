package hooks

import (
	"os"
	"strings"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/paths"
)

// parseDispatchAccountFlags returns the --account and --dir values of a
// dispatch command ("" when absent).
func parseDispatchAccountFlags(command string) (account, dir string) {
	segs := dispatchSegments(command)
	var seg []cmdToken
	for _, c := range segs {
		if !segmentHasHelp(c) {
			seg = c
			break
		}
	}
	for i := 0; i < len(seg); i++ {
		tok := seg[i].text
		switch {
		case tok == "--account" && i+1 < len(seg):
			account = seg[i+1].text
			i++
		case strings.HasPrefix(tok, "--account="):
			account = strings.TrimPrefix(tok, "--account=")
		case tok == "--dir" && i+1 < len(seg):
			dir = seg[i+1].text
			i++
		case strings.HasPrefix(tok, "--dir="):
			dir = strings.TrimPrefix(tok, "--dir=")
		}
	}
	return account, dir
}

// resolvePreviewAccount is the seam dispatchPreviewAccount resolves through.
var resolvePreviewAccount = claudehome.ForLaunch

// dispatchPreviewAccount returns the account the dispatch in command will
// run under, for the approval popup: "name (label)", or "error: …" when it
// cannot resolve (the dispatch itself will then fail). "" when no accounts
// are configured.
func dispatchPreviewAccount(targetAgent, command string) string {
	cfg := config.Load()
	if !cfg.HasAccounts() {
		return ""
	}
	flag, dir := parseDispatchAccountFlags(command)
	workDir := paths.ExpandHome(dir)
	if workDir == "" {
		workDir = cfg.ForAgent(targetAgent).Dir
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	parent := claudehome.ParentAccount(os.Getenv("AGENTCTL_WINDOW_ID"))
	r, err := resolvePreviewAccount(cfg, flag, targetAgent, paths.ExpandHome(workDir), parent)
	if err != nil {
		return "error: " + err.Error()
	}
	return formatAccount(cfg, r)
}

// formatAccount renders a resolution as "name (label)".
func formatAccount(cfg *config.Config, r claudehome.Resolution) string {
	name := r.Name
	if name == "" {
		name = r.Dir
	}
	if label := cfg.Accounts[r.Name].Label; label != "" {
		return name + " (" + label + ")"
	}
	return name
}
