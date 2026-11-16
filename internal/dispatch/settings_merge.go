package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/blsmth/agentctl/internal/safefs"
)

// nousDenyRules are the deny rules agentctl adds for nous launches. They are
// agentctl-owned: merging removes them when the backend is not nous.
var nousDenyRules = []string{
	"Bash(*backend-token*)",
	"Edit(**/.claude/settings*.json)",
}

// ownedEnvKeys are the settings env keys agentctl owns in a worktree file.
var ownedEnvKeys = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}

// readSettingsFile reads path without following symlinks. A missing file
// returns (nil, nil).
func readSettingsFile(root, path string) ([]byte, error) {
	f, err := safefs.OpenNoFollow(root, path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, 4<<20))
}

// mergeAgentctlSettings overlays the agentctl-owned parts of generated onto
// existing, keeping everything else (notably permission rules Claude Code
// saved when the user approved something). Owned: hooks, apiKeyHelper, env
// ANTHROPIC_BASE_URL/API_KEY/AUTH_TOKEN, the nous deny rules, and the deny and
// allow rules in generated. Owned keys absent from generated are deleted, so a
// launch with no backend drops earlier Nous routing. existing may be empty.
func mergeAgentctlSettings(existing, generated []byte) ([]byte, error) {
	gen := map[string]json.RawMessage{}
	if err := json.Unmarshal(generated, &gen); err != nil {
		return nil, fmt.Errorf("parsing generated settings: %w", err)
	}
	cur := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := json.Unmarshal(existing, &cur); err != nil {
			return nil, fmt.Errorf("existing settings are not valid JSON: %w", err)
		}
	}

	for _, k := range []string{"hooks", "apiKeyHelper"} {
		if v, ok := gen[k]; ok {
			cur[k] = v
		} else {
			delete(cur, k)
		}
	}

	env, err := mergeEnv(cur["env"], gen["env"])
	if err != nil {
		return nil, err
	}
	setOrDelete(cur, "env", env)

	perms, err := mergePermissions(cur["permissions"], gen["permissions"])
	if err != nil {
		return nil, err
	}
	setOrDelete(cur, "permissions", perms)

	return json.MarshalIndent(cur, "", "  ")
}

func setOrDelete(m map[string]json.RawMessage, key string, v map[string]json.RawMessage) {
	if len(v) == 0 {
		delete(m, key)
		return
	}
	b, _ := json.Marshal(v)
	m[key] = b
}

func rawObject(raw json.RawMessage, what string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("existing settings %q is not an object: %w", what, err)
	}
	return out, nil
}

func mergeEnv(cur, gen json.RawMessage) (map[string]json.RawMessage, error) {
	env, err := rawObject(cur, "env")
	if err != nil {
		return nil, err
	}
	for _, k := range ownedEnvKeys {
		delete(env, k)
	}
	genEnv, err := rawObject(gen, "env")
	if err != nil {
		return nil, err
	}
	for k, v := range genEnv {
		env[k] = v
	}
	return env, nil
}

func mergePermissions(cur, gen json.RawMessage) (map[string]json.RawMessage, error) {
	perms, err := rawObject(cur, "permissions")
	if err != nil {
		return nil, err
	}
	genPerms, err := rawObject(gen, "permissions")
	if err != nil {
		return nil, err
	}
	for _, list := range []string{"allow", "deny"} {
		var have, want []string
		if raw, ok := perms[list]; ok {
			if err := json.Unmarshal(raw, &have); err != nil {
				return nil, fmt.Errorf("existing permissions.%s is not a string list: %w", list, err)
			}
		}
		if raw, ok := genPerms[list]; ok {
			_ = json.Unmarshal(raw, &want)
		}
		if list == "deny" {
			have = removeStrings(have, nousDenyRules)
		}
		merged := appendMissing(have, want)
		if len(merged) == 0 {
			delete(perms, list)
			continue
		}
		b, _ := json.Marshal(merged)
		perms[list] = b
	}
	return perms, nil
}

func removeStrings(in, drop []string) []string {
	var out []string
	for _, s := range in {
		keep := true
		for _, d := range drop {
			if s == d {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, s)
		}
	}
	return out
}

func appendMissing(have, want []string) []string {
	seen := map[string]bool{}
	for _, s := range have {
		seen[s] = true
	}
	for _, s := range want {
		if !seen[s] {
			have = append(have, s)
			seen[s] = true
		}
	}
	return have
}
