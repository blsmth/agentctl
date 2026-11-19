package hooks

import (
	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
)

// resolvePreviewAccount is the seam previewAccount resolves through.
var resolvePreviewAccount = claudehome.ForLaunch

// previewAccount returns the account a dispatch of targetAgent into workDir
// will run under, for the approval popup: "name (label)", or "error: …" when
// it cannot resolve (the dispatch itself will then fail). "" when no accounts
// are configured.
func previewAccount(cfg *config.Config, flag, targetAgent, workDir string) string {
	if !cfg.HasAccounts() {
		return ""
	}
	// The hook runs with Claude Code's own env, whose AGENTCTL_WINDOW_ID may
	// be inherited from another window: resolve from the pane instead.
	parent := claudehome.ParentAccount(resolveHookWindowID())
	r, err := resolvePreviewAccount(cfg, flag, targetAgent, workDir, parent)
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
