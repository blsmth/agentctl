package statusline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/hooks"
)

// statusLineKey is the top-level Claude Code settings key this package owns.
const statusLineKey = "statusLine"

// command is the bare-PATH form of the command Claude Code invokes. The
// installed command swaps "agentctl" for the persisted prefix (see
// installCommand); it carries no other arguments -- the whole payload arrives
// on stdin.
const command = "agentctl statusline"

// persistedPrefixFn resolves the command prefix that is safe to persist into
// settings.json. It is the same logic hooks-install uses, including the
// refusal of temp/go-build binaries. Overridable in tests.
var persistedPrefixFn = hooks.PersistedPrefix

// installCommand returns the statusLine command to write: the persisted
// prefix (bare "agentctl" when on PATH, else the quoted absolute path of the
// running binary) followed by "statusline".
func installCommand() (string, error) {
	prefix, err := persistedPrefixFn()
	if err != nil {
		return "", fmt.Errorf("cannot install statusLine: %w", err)
	}
	return prefix + " statusline", nil
}

// isOurCommand reports whether cmd is an agentctl statusline invocation
// written by any version of Install, so re-installing upgrades it in place
// rather than demanding --force.
func isOurCommand(cmd string) bool {
	if cmd == command {
		return true
	}
	return strings.HasSuffix(cmd, "agentctl statusline") || strings.HasSuffix(cmd, "agentctl' statusline")
}

// desiredEntry is the canonical statusLine block agentctl installs. There is
// no "timeout" key -- Claude Code rejects the block if one is present. refreshInterval is set to its minimum (1 second) so the
// clock and rate-limit gauge keep advancing while a session sits idle --
// measured at ~25ms per invocation, well within Claude Code's debounce
// budget. hideVimModeIndicator is the only other valid key, which nothing
// here needs.
func desiredEntry(cmd string) map[string]interface{} {
	return map[string]interface{}{
		"type":            "command",
		"command":         cmd,
		"padding":         0,
		"refreshInterval": 1,
	}
}

// Install writes agentctl's statusLine entry to ~/.claude/settings.json, or
// (with uninstall=true) removes it.
//
// There is exactly one statusLine per Claude Code settings resolution, so
// installing clobbers any existing one -- if a *different* statusLine entry
// is already present, Install refuses and reports what is there unless force
// is set. This function deliberately does not attempt to build a
// tee/wrapper to coexist with an existing statusLine script; that is a real
// design question left out of scope for this phase.
func Install(force, uninstall bool) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}

	settings, err := claude.LoadSettings(path)
	if err != nil {
		return fmt.Errorf("loading %s: %w", path, err)
	}

	existing, hasExisting := existingEntry(settings)

	if uninstall {
		if !hasExisting {
			fmt.Println("No statusLine entry to remove.")
			return nil
		}
		settings.DeleteRawKey(statusLineKey)
		if err := claude.SaveSettings(path, settings); err != nil {
			return fmt.Errorf("saving %s: %w", path, err)
		}
		fmt.Printf("Removed statusLine entry from %s\n", path)
		return nil
	}

	cmd, err := installCommand()
	if err != nil {
		return err
	}
	desired := normalize(desiredEntry(cmd))

	if hasExisting && !reflect.DeepEqual(existing, desired) && !force {
		// An entry whose command is ours (e.g. installed by a prior agentctl
		// version that lacked refreshInterval) is an upgrade, not a
		// conflict -- allow it through without requiring --force. Only a
		// genuinely foreign command (someone else's statusLine script) still
		// needs --force.
		if existingCommand, _ := existing["command"].(string); !isOurCommand(existingCommand) {
			existingJSON, _ := json.MarshalIndent(existing, "", "  ")
			return fmt.Errorf(
				"a different statusLine is already configured in %s:\n%s\nRe-run with --force to overwrite (agentctl allows only one statusLine)",
				path, existingJSON,
			)
		}
	}

	if err := settings.SetRawKey(statusLineKey, desiredEntry(cmd)); err != nil {
		return fmt.Errorf("setting statusLine: %w", err)
	}
	if err := claude.SaveSettings(path, settings); err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}
	fmt.Printf("Installed statusLine command in %s\n", path)
	return nil
}

// settingsPath returns ~/.claude/settings.json.
func settingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// existingEntry returns the current statusLine entry (decoded to a generic
// map so it can be diffed against desiredEntry regardless of key order) and
// whether one is present at all.
func existingEntry(s *claude.Settings) (map[string]interface{}, bool) {
	raw, ok := s.RawKey(statusLineKey)
	if !ok {
		return nil, false
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		// Present but not an object we can parse -- treat as "different"
		// rather than erroring, so Install still reports it via existingEntry's
		// caller comparing against desired.
		return map[string]interface{}{"_unparseable": string(raw)}, true
	}
	return m, true
}

// normalize round-trips a map through JSON so number types line up with what
// json.Unmarshal would have produced for existingEntry (e.g. int 0 becomes
// float64 0), making reflect.DeepEqual meaningful between the two.
func normalize(m map[string]interface{}) map[string]interface{} {
	data, err := json.Marshal(m)
	if err != nil {
		return m
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return m
	}
	return out
}
