package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
)

// historyTamperReason is the deny reason for Bash commands that touch the
// agent definition history stores.
const historyTamperReason = "\U0001F6D1 BLOCKED: this touches agentctl's history stores (agent-history*.git) directly. " +
	"Running git against them bypasses the approval gates on agent and config changes. " +
	"Use `agentctl agents history|diff|revert` instead."

// historyNamePattern matches a path word naming one of the stores whatever the
// state dir: agent-history.git, agent-history-config.git, agent-history/...
// Anchored to a whole path component so ordinary text (a branch name, a doc
// path, a commit message) does not match; the resolved-path check covers the
// real state dir.
var historyNamePattern = regexp.MustCompile(
	`(?:^|[/=])agent-history(?:-[\w.-]+)?\.git(?:/|$)|(?:^|[/=])agent-history/[^/]+\.git(?:/|$)|agentctl/agent-history(?:/|$)`)

// historyRiskyCmds are commands that can reach into the state dir root and
// land inside a store (find -exec, tar -C, rsync, rm -rf, ...).
var historyRiskyCmds = map[string]bool{
	"find": true, "tar": true, "rsync": true, "xargs": true,
	"cp": true, "mv": true, "rm": true, "ln": true,
}

const globMeta = "*?[{"

// historyRoots returns the state dir and its symlink-resolved form.
func historyRoots() []string {
	sd := historyStateDir()
	if sd == "" {
		return nil
	}
	sd = filepath.Clean(sd)
	roots := []string{sd}
	if r := resolveExistingPrefix(sd); r != sd {
		roots = append(roots, r)
	}
	return roots
}

// isStateRoot reports whether abs p is the state dir itself.
func isStateRoot(p string) bool {
	for _, c := range []string{p, resolveExistingPrefix(p)} {
		for _, r := range historyRoots() {
			if c == r {
				return true
			}
		}
	}
	return false
}

// globReachesStore reports whether the glob pattern (absolute) may match a
// path inside a store: any real match does; for a brace or a Glob error it
// is enough that the part before the first metacharacter is the state dir
// or inside a store.
func globReachesStore(pat string) bool {
	if !strings.Contains(pat, "{") {
		if ms, err := filepath.Glob(pat); err == nil {
			for _, m := range ms {
				if inHistoryStore(m) {
					return true
				}
			}
			return false
		}
	}
	dir := filepath.Dir(pat[:strings.IndexAny(pat, globMeta)] + "x")
	return isStateRoot(dir) || inHistoryStore(dir)
}

// historyStateDir is the resolved state dir holding the stores. A var so
// tests can pin it. Resolved on each call, never at init.
var historyStateDir = func() string {
	dir, err := config.StateDir()
	if err != nil {
		return ""
	}
	return dir
}

// inHistoryStore reports whether p (absolute) is one of the stores or inside
// one: its first component under the state dir (or the symlink-resolved
// state dir) starts with "agent-history". Symlinks in p are resolved.
func inHistoryStore(p string) bool {
	for _, c := range []string{p, resolveExistingPrefix(p)} {
		for _, r := range historyRoots() {
			rel, err := filepath.Rel(r, c)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			first, _, _ := strings.Cut(rel, string(filepath.Separator))
			if strings.HasPrefix(first, "agent-history") {
				return true
			}
		}
	}
	return false
}

// expandWordPath expands a leading ~, $HOME or ${HOME}.
func expandWordPath(w string) string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return w
	}
	for _, p := range []string{"~", "${HOME}", "$HOME"} {
		if w == p || strings.HasPrefix(w, p+"/") {
			return home + strings.TrimPrefix(w, p)
		}
	}
	return w
}

// checkHistoryTamper denies a Bash command that references the history
// stores: git --git-dir/-C/GIT_DIR, cd into one, rm/mv/cp, a symlink to one,
// or a relative path after a cd. Reads are denied too (simpler). The
// arguments of agentctl commands are masked first, so `agentctl agents
// history|diff|revert` keep working. cwd is the Bash tool's directory.
func checkHistoryTamper(command, cwd string) string {
	if historyStateDir() == "" {
		return ""
	}
	masked := maskAgentctlArgs(command)
	lines := []string{masked}
	if views, _ := writeViews(scanShell(masked)); len(views) > 0 {
		lines = append(lines, views...)
	}
	for _, line := range lines {
		if historyReferenced(line, cwd) {
			return historyTamperReason
		}
	}
	return ""
}

func historyReferenced(line, cwd string) bool {
	sc := scanShell(line)
	var words []string
	var cds []string
	risky := strings.Contains(line, "`")
	for _, it := range sc.items {
		c := it.cmd
		if c == nil {
			continue
		}
		risky = risky || c.subst
		for _, w := range c.words {
			words = append(words, w.text)
			risky = risky || w.text == "for"
		}
		for _, r := range c.redirs {
			words = append(words, r.target.text)
		}
		if i := c.nameIndex(); i >= 0 {
			risky = risky || historyRiskyCmds[filepath.Base(c.words[i].text)]
			switch c.words[i].text {
			case "cd", "pushd":
				if a := c.args(); len(a) > 0 {
					cds = append(cds, a[0].text)
				}
			}
		}
	}
	if sc.unsure {
		words = append(words, strings.Fields(line)...)
	}
	// Directories a relative word may be relative to: the cwd, then each cd
	// target (resolved against the ones before it).
	bases := []string{}
	if cwd != "" {
		bases = append(bases, filepath.Clean(cwd))
	}
	for _, d := range cds {
		d = expandWordPath(d)
		switch {
		case filepath.IsAbs(d):
			bases = append(bases, filepath.Clean(d))
		case cwd != "":
			for _, b := range append([]string(nil), bases...) {
				bases = append(bases, filepath.Join(b, d))
			}
		}
	}
	for _, w := range words {
		risky = risky || strings.ContainsAny(w, globMeta)
	}
	if risky {
		for _, b := range bases {
			if isStateRoot(b) {
				return true
			}
		}
	}
	for _, w := range words {
		cands := []string{w}
		if _, v, ok := strings.Cut(w, "="); ok {
			cands = append(cands, v) // GIT_DIR=x, --git-dir=x, --work-tree=x
		}
		for _, c := range cands {
			if c == "" {
				continue
			}
			if historyNamePattern.MatchString(c) {
				return true
			}
			c = expandWordPath(c)
			var full []string
			if filepath.IsAbs(c) {
				full = []string{filepath.Clean(c)}
			} else {
				for _, b := range bases {
					full = append(full, filepath.Join(b, c))
				}
			}
			for _, f := range full {
				if inHistoryStore(f) {
					return true
				}
				if strings.ContainsAny(f, globMeta) && globReachesStore(f) {
					return true
				}
				if risky && isStateRoot(f) {
					return true
				}
			}
		}
	}
	return false
}

// historyEditTarget returns the path a file-editing tool call would write
// when it is inside a history store ("" otherwise): the Write/Edit
// counterpart of checkHistoryTamper.
func historyEditTarget(toolName string, rawInput json.RawMessage) string {
	p := editToolPath(toolName, rawInput)
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	if inHistoryStore(p) {
		return p
	}
	return ""
}

// historyEditDenyReason is shown to the model when it tries to edit a history
// store with a file-editing tool.
func historyEditDenyReason(path string) string {
	return fmt.Sprintf("\U0001F6D1 BLOCKED: %s is inside agentctl's agent history store. "+
		"Use `agentctl agents history|diff|revert` instead.", path)
}
