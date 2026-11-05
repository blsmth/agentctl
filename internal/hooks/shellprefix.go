package hooks

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
)

//go:embed shell-prefix.sh
var shellPrefixScript string

// ShellPrefixScriptContent returns the contents of the shell-prefix.sh bootstrap
// script, with its shebang line replaced by an absolute path to a resolved
// modern bash (see resolveBash). The agentctlPath parameter is unused -- the
// script resolves the agentctl binary itself via the AGENTCTL_BIN env var set
// at launch time.
func ShellPrefixScriptContent(_ string) string {
	return withResolvedShebang(shellPrefixScript, resolvedBash())
}

// withResolvedShebang replaces only the first line of script with a shebang
// pointing at bashPath. It never touches anything else -- in particular the
// explanatory comment block that follows the shebang, which must survive
// intact.
func withResolvedShebang(script, bashPath string) string {
	nl := strings.IndexByte(script, '\n')
	if nl == -1 {
		return "#!" + bashPath
	}
	return "#!" + bashPath + script[nl:]
}

// EnsureShellPrefixScript creates (or overwrites) .agentctl/shell-prefix.sh
// and ensures the plugin directories exist. It is called at monitor startup and
// during agentctl init so the script is always current.
func EnsureShellPrefixScript() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("EnsureShellPrefixScript: home dir: %w", err)
	}

	// Write the bootstrap script into the runtime dir. Every Bash command an
	// agent runs goes through it, so it must never be written through a
	// symlink a cloned repo planted (safefs refuses those), and is 0700.
	runtimeDir := paths.RuntimeDir()
	if runtimeDir == "" {
		return fmt.Errorf("EnsureShellPrefixScript: runtime dir not initialised")
	}
	scriptPath := filepath.Join(runtimeDir, "shell-prefix.sh")
	content := ShellPrefixScriptContent("")
	if err := safefs.WriteFile(runtimeDir, scriptPath, []byte(content), 0o700); err != nil {
		return fmt.Errorf("EnsureShellPrefixScript: write script: %w", err)
	}

	// Ensure plugin directories exist under ~/.claude/agents/agentctl/.
	agentctlDir := filepath.Join(home, ".claude", "agents", "agentctl")
	for _, sub := range []string{"shell-hooks.d", "env.d"} {
		if err := os.MkdirAll(filepath.Join(agentctlDir, sub), 0o755); err != nil {
			return fmt.Errorf("EnsureShellPrefixScript: mkdir %s: %w", sub, err)
		}
	}

	logging.Log("EnsureShellPrefixScript: wrote %s", scriptPath)
	return nil
}
