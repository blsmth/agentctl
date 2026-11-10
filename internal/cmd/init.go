package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/safefs"
)

// RunInit is the entry point for `agentctl init`. It sets up the .claude/agents/
// directory, installs starter templates, and configures Claude Code hooks.
func RunInit() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not determine working directory: %v\n", err)
		os.Exit(1)
	}

	dir := filepath.Join(cwd, ".claude", "agents")
	logging.Log("RunInit: target dir=%s", dir)

	info, err := os.Stat(dir)
	if err == nil && info.IsDir() {
		fmt.Println("Already initialized — .claude/agents/ exists")
	} else {
		// .claude/ may be repository content; never create through a symlink.
		if err := safefs.MkdirAll(cwd, dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not create .claude/agents/: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Created .claude/agents/")
		fmt.Println("Use `agentctl new <name>` to create agents, then `agentctl sync` to register them.")

		// Write the shell-prefix bootstrap script and create plugin directories.
		if err := hooks.EnsureShellPrefixScript(); err != nil {
			logging.Log("RunInit: ensureShellPrefixScript: %v", err)
		}
	}

	fmt.Print("Install starter agent templates? [Y/n] ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)

	if line == "" || line == "y" || line == "Y" {
		installed, skipped, err := agent.InstallTemplates(dir, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(installed) == 0 && len(skipped) == 0 {
			fmt.Println("No templates found.")
			return
		}

		for _, name := range installed {
			fmt.Printf("  installed  %s\n", name)
		}
		for _, name := range skipped {
			fmt.Printf("  skipped    %s  (already exists; use --force to overwrite)\n", name)
		}

		if len(installed) > 0 {
			fmt.Printf("\n%d template(s) installed to %s\n", len(installed), dir)
		}
		if len(skipped) > 0 {
			fmt.Printf("%d template(s) skipped\n", len(skipped))
		}
	} else {
		fmt.Println("Skipped. Run `agentctl install-templates` later to add them.")
	}

	fmt.Print("\nConfigure Claude Code hooks? [Y/n] ")
	line, _ = reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" || line == "y" || line == "Y" {
		path, err := InstallHooks(false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error configuring hooks: %v\n", err)
		} else {
			fmt.Printf("Hooks configured in %s\n", path)
		}
	} else {
		fmt.Println("Skipped. Run `agentctl hooks-install` later to add them.")
	}

	configureDefaultAgent(reader, cwd)
}

// configureDefaultAgent prompts the user to select a default agent and saves it
// to the global agentctl config. A repo may override it with default_agent in
// its .agentctl/config.json, which prefix+A honors only after the launch-time
// safety checks (see usableDefaultAgent). It is called at the end of RunInit
// as a final setup step.
func configureDefaultAgent(reader *bufio.Reader, cwd string) {
	cfg := config.Load()

	if cfg.GlobalDefaultAgent != "" {
		fmt.Printf("\nDefault agent: %s (already configured)\n", cfg.GlobalDefaultAgent)
		return
	}

	agents, err := agent.DiscoverAll(cfg, []string{cwd})
	if err != nil {
		logging.Log("configureDefaultAgent: discover failed: %v", err)
		return
	}

	// Filter out template-scoped agents — only real installed agents are selectable.
	var selectable []agent.Agent
	for _, a := range agents {
		if a.Scope != agent.ScopeTemplate {
			selectable = append(selectable, a)
		}
	}

	if len(selectable) == 0 {
		fmt.Println("\nNo agents found. Set default_agent later in config or run `agentctl launch <name>`.")
		return
	}

	if len(selectable) == 1 {
		cfg.DefaultAgent = selectable[0].Name
		if err := config.SetDefaultAgent(cfg.DefaultAgent); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save config: %v\n", err)
			return
		}
		fmt.Printf("\nDefault agent set to %q\n", cfg.DefaultAgent)
		return
	}

	// Multiple agents — prompt the user to choose.
	fmt.Println("\nSelect default agent (for quick-launch via prefix+A):")
	for i, a := range selectable {
		fmt.Printf("  %d. %s\n", i+1, a.Name)
	}
	fmt.Print("Choice [1]: ")

	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)

	if line == "n" || line == "skip" {
		fmt.Println("Skipped. Set default_agent manually in config later.")
		return
	}

	choice := 1
	if line != "" {
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(selectable) {
			fmt.Fprintf(os.Stderr, "Invalid choice %q — skipping default agent setup.\n", line)
			return
		}
		choice = n
	}

	cfg.DefaultAgent = selectable[choice-1].Name
	if err := config.SetDefaultAgent(cfg.DefaultAgent); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save config: %v\n", err)
		return
	}
	fmt.Printf("Default agent set to %q\n", cfg.DefaultAgent)
}

// persistedPrefixFn resolves the hook command prefix to persist; a variable so
// tests (whose binary lives in a go-build dir) can substitute it.
var persistedPrefixFn = hooks.PersistedPrefix

// InstallHooks installs agentctl hook handlers into the appropriate settings file.
// If global is true, writes to ~/.claude/settings.json (global Claude settings).
// If global is false, writes to .claude/settings.local.json in the current working
// directory, creating .claude/ if needed (project-local settings).
// Returns the path that was written.
func InstallHooks(global bool) (string, error) {
	var settingsPath string
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		settingsPath = filepath.Join(home, ".claude", "settings.json")
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
		// .claude/ is repository-controlled territory: refuse a symlinked
		// dir or settings file instead of merging hooks into whatever it
		// points at (claude.SaveSettings deliberately follows symlinks, for
		// dotfile-managed ~/.claude/settings.json).
		claudeDir := filepath.Join(cwd, ".claude")
		if err := safefs.MkdirAll(cwd, claudeDir, 0o755); err != nil {
			return "", fmt.Errorf("create .claude/: %w", err)
		}
		settingsPath = filepath.Join(claudeDir, "settings.local.json")
		if safefs.IsSymlink(settingsPath) {
			return "", fmt.Errorf("%s is a symlink; refusing to write through it", settingsPath)
		}
	}

	prefix, err := persistedPrefixFn()
	if err != nil {
		return "", fmt.Errorf("refusing to write hooks: %w", err)
	}
	if err := writeHooksToPath(settingsPath, prefix); err != nil {
		return "", err
	}
	return settingsPath, nil
}

// writeHooksToPath reads the existing JSON file at path (if any), repairs
// every agentctl-owned hook event to its canonical value, preserves any hook
// event agentctl does not own (a user's own custom hook), and writes the
// result back. Idempotent.
//
// "Owned" means present in hooks.HooksConfig() — the single source of truth
// for agentctl's hook map. An owned event is always overwritten with its
// canonical value, even if it already exists with different content, because
// a drifted owned event (e.g. Stop rebound to the shell builtin "true") is
// exactly the failure mode this function exists to repair: without this,
// re-running `agentctl init`/`hooks-install` could never fix an event a user
// or an older agentctl version had clobbered. Any event NOT in the canonical
// map is left completely untouched.
//
// The file is rewritten through claude.UpdateSettingsKey: only the "hooks"
// key changes, every other top-level key (and every unowned hook event) is
// kept with its value and key order intact (whitespace may be normalized),
// the original is backed up once to settings.json.bak,
// the write is atomic, and the file mode is preserved.
func writeHooksToPath(settingsPath, prefix string) error {
	var repaired []string
	err := claude.UpdateSettingsKey(settingsPath, "hooks", func(old json.RawMessage, present bool) (json.RawMessage, error) {
		var merged []byte
		var err error
		merged, repaired, err = mergeHooks(old, present, hooks.BuildHooksConfig(prefix))
		return merged, err
	})
	if err != nil {
		return err
	}
	if len(repaired) > 0 {
		logging.Log("writeHooksToPath: repaired drifted hook event(s) %v in %s", repaired, settingsPath)
		fmt.Printf("Repaired drifted hook registration(s) in %s: %s\n", settingsPath, strings.Join(repaired, ", "))
	}
	return nil
}

// mergeHooks overlays canonical onto the existing raw "hooks" object. Unowned
// events keep their raw bytes; owned events are replaced by their canonical
// value. It returns the merged object and the sorted list of owned events
// whose prior value differed from canonical. A "hooks" value that is not a
// JSON object is replaced outright.
func mergeHooks(old json.RawMessage, present bool, canonical map[string]interface{}) ([]byte, []string, error) {
	existing := map[string]json.RawMessage{}
	if present {
		if err := json.Unmarshal(old, &existing); err != nil || existing == nil {
			existing = map[string]json.RawMessage{}
		}
	}

	merged := make(map[string]json.RawMessage, len(existing)+len(canonical))
	for event, val := range existing {
		if _, owned := canonical[event]; !owned {
			merged[event] = val
		}
	}

	var repaired []string
	for event, canonicalVal := range canonical {
		canonicalJSON, err := json.Marshal(canonicalVal)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding hook %s: %w", event, err)
		}
		if prior, ok := existing[event]; ok && !sameJSON(prior, canonicalJSON) {
			repaired = append(repaired, event)
		}
		merged[event] = canonicalJSON
	}
	sort.Strings(repaired)

	out, err := json.Marshal(merged)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding hooks: %w", err)
	}
	return out, repaired, nil
}

// sameJSON reports whether a and b encode the same value, ignoring
// whitespace and object key order.
func sameJSON(a, b []byte) bool {
	var va, vb interface{}
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	na, _ := json.Marshal(va)
	nb, _ := json.Marshal(vb)
	return string(na) == string(nb)
}

// RunNew creates a new agent in the current project's .claude/agents/ directory.
// Flag parsing is handled by Kong via NewCmd in cli.go.
func RunNew(name, extends string) {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not determine working directory: %v\n", err)
		os.Exit(1)
	}

	agentsDir := filepath.Join(cwd, ".claude", "agents")
	logging.Log("RunNew: name=%s extends=%q agentsDir=%s", name, extends, agentsDir)

	// Check that .claude/agents/ exists
	info, err := os.Stat(agentsDir)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "No .claude/agents/ directory. Run `agentctl init` first.")
		os.Exit(1)
	}

	targetPath := filepath.Join(agentsDir, name+".md")

	// Check for existing file (Lstat: a dangling symlink counts as existing).
	if _, err := os.Lstat(targetPath); err == nil {
		fmt.Fprintf(os.Stderr, "Agent %s already exists at %s\n", name, targetPath)
		os.Exit(1)
	}

	var content string
	if extends != "" {
		content = buildExtendsAgentFile(name, extends)
	} else {
		content = buildLocalAgentFile(name)
	}

	if err := safefs.WriteFile(cwd, targetPath, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not write agent file: %v\n", err)
		os.Exit(1)
	}

	if extends != "" {
		fmt.Printf("Created .claude/agents/%s.md (extends %s)\n", name, extends)
	} else {
		fmt.Printf("Created .claude/agents/%s.md\n", name)
	}

	// Auto-sync so the symlink is created immediately
	logging.Log("RunNew: running sync")
	if err := runSync(cwd, false); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: sync failed: %v\n", err)
	}

	fmt.Printf("Agent registered. Launch with `agentctl launch %s`\n", name)
}

// buildLocalAgentFile scaffolds a full agent definition for local use.
func buildLocalAgentFile(name string) string {
	var sb strings.Builder

	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", name))
	sb.WriteString("description: \n")
	sb.WriteString("tools: Read, Write, Edit, Bash, Grep, Glob\n")
	sb.WriteString("---\n\n")

	domain := inferLocalDomain(name)
	sb.WriteString(fmt.Sprintf("You are a senior %s engineer.\n\n", domain))

	sb.WriteString("## Role\n\n")
	sb.WriteString("<!-- Describe what this agent does and its primary responsibilities -->\n\n")

	sb.WriteString("## Context\n\n")
	sb.WriteString("<!-- Add project-specific context, conventions, and background -->\n\n")

	sb.WriteString("## Code Style\n\n")
	sb.WriteString("<!-- Add naming conventions, patterns to use/avoid -->\n")

	return sb.String()
}

// buildExtendsAgentFile scaffolds a thin override file that extends a base agent.
func buildExtendsAgentFile(name, base string) string {
	var sb strings.Builder

	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", name))
	sb.WriteString(fmt.Sprintf("extends: %s\n", base))
	sb.WriteString(fmt.Sprintf("description: %s with project-specific context\n", name))
	sb.WriteString("---\n\n")

	sb.WriteString("## Project-Specific Context\n\n")
	sb.WriteString("<!-- Add project/client specific instructions here -->\n\n")

	sb.WriteString("## Overrides\n\n")
	sb.WriteString("<!-- Any instructions that supplement the base agent -->\n")

	return sb.String()
}

// inferLocalDomain extracts a readable domain name from an agent name.
// "go-engineer" → "Go", "my-agent" → "my-agent"
func inferLocalDomain(name string) string {
	suffixes := []string{"-engineer", "-agent", "-worker"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) {
			domain := strings.TrimSuffix(name, suffix)
			parts := strings.Split(domain, "-")
			for i, p := range parts {
				if len(p) > 0 {
					parts[i] = strings.ToUpper(p[:1]) + p[1:]
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return name
}
