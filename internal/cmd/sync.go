package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
)

// RunSync is the entry point for `agentctl sync [--dry-run] [--clean] [--unlink]`.
// Flag parsing is handled by Kong via SyncCmd in cli.go.
func RunSync(dryRun, clean, unlink bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		logging.Error("sync: could not determine home directory: %v", err)
		fmt.Fprintf(os.Stderr, "Error: could not determine home directory: %v\n", err)
		os.Exit(1)
	}

	registryDir := filepath.Join(home, ".claude", "agents")

	cwd, err := os.Getwd()
	if err != nil {
		logging.Error("sync: could not determine working directory: %v", err)
		fmt.Fprintf(os.Stderr, "Error: could not determine working directory: %v\n", err)
		os.Exit(1)
	}

	projectAgentsDir := filepath.Join(cwd, ".claude", "agents")

	if clean {
		cmdSyncClean(registryDir, projectAgentsDir, dryRun)
		return
	}

	if unlink {
		cmdSyncUnlink(registryDir, projectAgentsDir, dryRun)
		return
	}

	if err := runSync(cwd, dryRun); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runSync performs the default sync operation (link project agents into the registry)
// for the given project directory. It returns an error rather than calling os.Exit,
// so it can be called programmatically (e.g. from RunNew).
func runSync(cwd string, dryRun bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not determine home directory: %w", err)
	}

	registryDir := filepath.Join(home, ".claude", "agents")
	projectAgentsDir := filepath.Join(cwd, ".claude", "agents")

	// Default: sync .claude/agents/ → ~/.claude/agents/
	info, err := os.Stat(projectAgentsDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("No .claude/agents/ directory found. Run `agentctl init` to set up.")
	}

	// Ensure registry dir exists
	if !dryRun {
		if err := os.MkdirAll(registryDir, 0o755); err != nil {
			return fmt.Errorf("could not create registry dir: %w", err)
		}
	}

	entries, err := os.ReadDir(projectAgentsDir)
	if err != nil {
		return fmt.Errorf("reading .claude/agents/: %w", err)
	}

	linked := 0
	current := 0
	conflicts := 0

	prefix := ""
	if dryRun {
		prefix = "[dry-run] "
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		srcAbs, err := filepath.Abs(filepath.Join(projectAgentsDir, name))
		if err != nil {
			logging.Log("sync: could not get abs path for %s: %v", name, err)
			continue
		}

		target := filepath.Join(registryDir, name)
		agentName := strings.TrimSuffix(name, ".md")

		lstat, err := os.Lstat(target)
		if err != nil {
			// Nothing at target — create symlink
			if !dryRun {
				if err := os.Symlink(srcAbs, target); err != nil {
					logging.Error("sync: error linking %s: %v", agentName, err)
					fmt.Fprintf(os.Stderr, "%sError linking %s: %v\n", prefix, agentName, err)
					continue
				}
			}
			fmt.Printf("%s+ Linked %s\n", prefix, agentName)
			linked++
			continue
		}

		if lstat.Mode()&os.ModeSymlink == 0 {
			// Regular file (not a symlink) — conflict
			fmt.Printf("%s! %s conflicts with global agent (not a symlink)\n", prefix, agentName)
			conflicts++
			continue
		}

		// It's a symlink — check where it points
		existingTarget, err := os.Readlink(target)
		if err != nil {
			logging.Log("sync: could not readlink %s: %v", target, err)
			fmt.Printf("%s! %s (could not read symlink)\n", prefix, agentName)
			conflicts++
			continue
		}

		existingAbs, err := filepath.Abs(existingTarget)
		if err != nil {
			existingAbs = existingTarget
		}

		if existingAbs == srcAbs {
			fmt.Printf("%s· %s (already synced)\n", prefix, agentName)
			current++
		} else {
			fmt.Printf("%s! %s conflicts with existing symlink -> %s\n", prefix, agentName, existingAbs)
			conflicts++
		}
	}

	fmt.Printf("%sSynced: %d linked, %d already current, %d conflicts\n", prefix, linked, current, conflicts)
	return nil
}

func cmdSyncClean(registryDir, projectAgentsDir string, dryRun bool) {
	prefix := ""
	if dryRun {
		prefix = "[dry-run] "
	}

	projectAgentsDirAbs, err := filepath.Abs(projectAgentsDir)
	if err != nil {
		logging.Error("sync clean: could not resolve project agents dir: %v", err)
		fmt.Fprintf(os.Stderr, "Error: could not resolve project agents dir: %v\n", err)
		os.Exit(1)
	}

	entries, err := os.ReadDir(registryDir)
	if err != nil {
		logging.Error("sync clean: reading registry dir: %v", err)
		fmt.Fprintf(os.Stderr, "Error: reading registry dir: %v\n", err)
		os.Exit(1)
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		path := filepath.Join(registryDir, name)
		lstat, err := os.Lstat(path)
		if err != nil || lstat.Mode()&os.ModeSymlink == 0 {
			continue
		}

		linkTarget, err := os.Readlink(path)
		if err != nil {
			continue
		}

		linkTargetAbs, err := filepath.Abs(linkTarget)
		if err != nil {
			linkTargetAbs = linkTarget
		}

		// Only consider symlinks pointing into this project's .claude/agents/
		if !strings.HasPrefix(linkTargetAbs, projectAgentsDirAbs+string(filepath.Separator)) &&
			linkTargetAbs != projectAgentsDirAbs {
			continue
		}

		// Check if the target file still exists
		if _, err := os.Stat(linkTargetAbs); err == nil {
			// Target still exists — leave it alone
			continue
		}

		// Stale — target is gone
		agentName := strings.TrimSuffix(name, ".md")
		logging.Log("sync clean: removing stale symlink %s -> %s", path, linkTargetAbs)
		if !dryRun {
			if err := os.Remove(path); err != nil {
				logging.Error("sync clean: error removing %s: %v", agentName, err)
				fmt.Fprintf(os.Stderr, "%sError removing %s: %v\n", prefix, agentName, err)
				continue
			}
		}
		fmt.Printf("%s- Removed stale link %s\n", prefix, agentName)
		removed++
	}

	fmt.Printf("%sClean: %d stale symlinks removed\n", prefix, removed)
}

func cmdSyncUnlink(registryDir, projectAgentsDir string, dryRun bool) {
	prefix := ""
	if dryRun {
		prefix = "[dry-run] "
	}

	projectAgentsDirAbs, err := filepath.Abs(projectAgentsDir)
	if err != nil {
		logging.Error("sync unlink: could not resolve project agents dir: %v", err)
		fmt.Fprintf(os.Stderr, "Error: could not resolve project agents dir: %v\n", err)
		os.Exit(1)
	}

	entries, err := os.ReadDir(registryDir)
	if err != nil {
		logging.Error("sync unlink: reading registry dir: %v", err)
		fmt.Fprintf(os.Stderr, "Error: reading registry dir: %v\n", err)
		os.Exit(1)
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		path := filepath.Join(registryDir, name)
		lstat, err := os.Lstat(path)
		if err != nil || lstat.Mode()&os.ModeSymlink == 0 {
			continue
		}

		linkTarget, err := os.Readlink(path)
		if err != nil {
			continue
		}

		linkTargetAbs, err := filepath.Abs(linkTarget)
		if err != nil {
			linkTargetAbs = linkTarget
		}

		// Only remove symlinks pointing into this project's .claude/agents/
		if !strings.HasPrefix(linkTargetAbs, projectAgentsDirAbs+string(filepath.Separator)) &&
			linkTargetAbs != projectAgentsDirAbs {
			continue
		}

		agentName := strings.TrimSuffix(name, ".md")
		logging.Log("sync unlink: removing symlink %s -> %s", path, linkTargetAbs)
		if !dryRun {
			if err := os.Remove(path); err != nil {
				logging.Error("sync unlink: error removing %s: %v", agentName, err)
				fmt.Fprintf(os.Stderr, "%sError unlinking %s: %v\n", prefix, agentName, err)
				continue
			}
		}
		fmt.Printf("%s- Unlinked %s\n", prefix, agentName)
		removed++
	}

	fmt.Printf("%sUnlink: %d symlinks removed\n", prefix, removed)
}
