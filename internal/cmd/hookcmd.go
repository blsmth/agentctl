package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/selfpath"
)

// normalizeHookCommand reduces a registered hook command to a canonical form
// so that equivalent ways of invoking agentctl compare equal: bare `agentctl`
// and any absolute (or ~-relative, optionally quoted) path that resolves to an
// executable named agentctl all normalize to "agentctl <args>". A path whose
// target is missing, not executable, or not an agentctl binary is rewritten to
// "MISSING:<path> <args>" so it never equals a healthy command (that is real
// drift). Commands with any other shape are returned unchanged.
func normalizeHookCommand(cmd, home string) string {
	tok, rest, ok := splitFirstToken(cmd)
	if !ok {
		return cmd
	}
	if tok == "agentctl" {
		return cmd
	}
	// The running binary is by definition an agentctl, whatever it is named
	// (e.g. a test binary); this keeps HooksConfig() self-equivalent.
	if tok == selfpath.Path() {
		return "agentctl" + rest
	}
	path := tok
	if strings.HasPrefix(path, "~/") && home != "" {
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return cmd
	}
	if filepath.Base(tok) != "agentctl" {
		return cmd
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
		return "MISSING:" + tok + rest
	}
	return "agentctl" + rest
}

// splitFirstToken splits a shell command into its first word (handling a
// single- or double-quoted word) and the remainder, including its leading space.
func splitFirstToken(cmd string) (tok, rest string, ok bool) {
	cmd = strings.TrimLeft(cmd, " ")
	if cmd == "" {
		return "", "", false
	}
	if q := cmd[0]; q == '\'' || q == '"' {
		end := strings.IndexByte(cmd[1:], q)
		if end < 0 {
			return "", "", false
		}
		return cmd[1 : 1+end], cmd[2+end:], true
	}
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		return cmd[:i], cmd[i:], true
	}
	return cmd, "", true
}

// normalizeHookValue returns a deep copy of a hook event value with every
// "command" string passed through normalizeHookCommand.
func normalizeHookValue(v interface{}, home string) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			if s, isStr := val.(string); isStr && k == "command" {
				out[k] = normalizeHookCommand(s, home)
				continue
			}
			out[k] = normalizeHookValue(val, home)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, val := range x {
			out[i] = normalizeHookValue(val, home)
		}
		return out
	default:
		return v
	}
}
