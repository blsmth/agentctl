package hooks

import (
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// The write tamper templates (cronTamperTemplates) match text: a redirect,
// write verb or interpreter in the same simple command as a protected path.
// Text alone misses a write whose target is spelled relative to an earlier
// cd (`cd ~/.local/state/agentctl && rm -rf artifacts`) or through a shell
// variable (`D=~/.local/state/agentctl/cron; echo x > $D/state.json`).
// writeViews restates each simple command with those resolved, one command
// per line, and matchesWriteTemplates matches the templates against these
// lines too. The resolution is textual (no ~ expansion, no file system) and
// leans toward the deny side wherever the shell's behaviour is uncertain:
//
//   - A cd that surely runs in this shell (cdCertain) replaces the directory;
//     one that may not run adds its target to the candidate directories, and
//     every candidate gets its own line.
//   - A subshell's cd is undone at its closing paren.
//   - A variable assigned a literal value is substituted. One assigned from a
//     command substitution is substituted with the substitution's text, with
//     its separators turned into slashes, so a protected path named in it
//     still reaches the template (`D=$(echo ~/x/cron)` reads as
//     `$/echo/~/x/cron/`).
//   - A `bash -c` / `sh -c` / `eval` script is scanned as a nested line.
//
// What it cannot resolve (a variable set outside the line, a cd to an
// expansion, a cd inside backticks) stays as text, which the templates match
// as before.

// maxViewDirs caps the candidate directories tracked through uncertain cds.
const maxViewDirs = 8

// maxViewDepth caps nested `bash -c` / eval scripts.
const maxViewDepth = 3

// maxViewValue caps a variable value or directory, and maxViewBytes the
// total size of a line's views. Values grow by substitution (D=x$D$D
// doubles each time), so an uncapped line could keep the hook past its
// timeout, and a timed-out hook does not deny. Past either cap writeViews
// reports overflow and callers fail closed.
const (
	maxViewValue = 4 << 10
	maxViewBytes = 256 << 10
)

// viewShells run a -c script argument.
var viewShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

// viewInterpreters get the directory as an extra word, so the interpreter
// template sees where a script with relative paths runs.
var viewInterpreters = map[string]bool{
	"python": true, "python3": true, "node": true, "ruby": true, "deno": true,
	"bun": true, "osascript": true, "perl": true, "find": true,
}

// viewState is the directory and variable state writeViews threads through
// a line.
type viewState struct {
	dirs  []string // candidate directories; "" is the unknown starting dir
	vars  map[string]string
	stack [][]string
	// budget is shared with nested scripts.
	budget *viewBudget
}

// viewBudget tracks the bytes of view lines produced for one command line.
type viewBudget struct {
	bytes    int
	overflow bool
}

// writeViews returns sc's simple commands restated with cd and variables
// resolved (see above), only those where something was resolved.
// overflow reports that a cap was hit: the views are incomplete, and the
// caller must fail closed.
func writeViews(sc shellScript) (views []string, overflow bool) {
	st := &viewState{dirs: []string{""}, vars: map[string]string{}, budget: &viewBudget{}}
	views = st.views(sc, 0)
	if st.budget.overflow {
		return nil, true
	}
	return views, false
}

func (st *viewState) views(sc shellScript, depth int) []string {
	var out []string
	for _, it := range sc.items {
		switch it.op {
		case "":
		case "(":
			st.stack = append(st.stack, append([]string(nil), st.dirs...))
			continue
		case ")":
			if n := len(st.stack); n > 0 {
				st.dirs, st.stack = st.stack[n-1], st.stack[:n-1]
			}
			continue
		default:
			continue
		}
		for _, line := range st.command(sc, it.cmd, depth) {
			st.budget.bytes += len(line)
			out = append(out, line)
		}
		if st.budget.bytes > maxViewBytes {
			st.budget.overflow = true
		}
		if st.budget.overflow {
			return nil
		}
	}
	return out
}

// command records c's assignments and cd, and returns its view lines.
func (st *viewState) command(sc shellScript, c *shCommand, depth int) []string {
	// Words are expanded and assignments recorded left to right, so
	// `D=x E=$D/y` sees D.
	ni := c.nameIndex()
	words := make([]string, len(c.words))
	expanded := false
	for i, w := range c.words {
		var changed bool
		words[i], changed = st.expand(w.text)
		expanded = expanded || changed
		if i < ni || ni < 0 || (i > ni && declWords[words[ni]] && isAssignment(w.raw)) {
			st.assign(sc, w, words[i])
		}
	}
	redirs := make([]string, len(c.redirs))
	for i, r := range c.redirs {
		var changed bool
		redirs[i], changed = st.expand(r.target.text)
		expanded = expanded || changed
	}
	if ni < 0 || st.budget.overflow {
		return nil
	}
	name := filepath.Base(words[ni])
	args := words[ni+1:]
	switch name {
	case "cd", "pushd":
		st.cd(args, cdCertain(c.before, c.after))
		return nil
	case "popd":
		st.addDirs([]string{""})
		return nil
	}

	var out []string
	if depth < maxViewDepth {
		if script, ok := nestedScript(name, args); ok {
			nested := &viewState{dirs: append([]string(nil), st.dirs...), vars: copyVars(st.vars), budget: st.budget}
			nsc := scanShell(script)
			out = append(out, nested.views(nsc, depth+1)...)
			// The nested script's own text, cd-free, is matched as is.
			out = append(out, strings.ReplaceAll(nsc.masked(), "\n", " "))
		}
	}

	masked := !sc.unsure && !c.subst && c.isAgentctl()
	for _, dir := range st.dirs {
		if dir == "" && !expanded && depth == 0 {
			continue // nothing resolved: the plain text is matched already
		}
		parts := append([]string(nil), words[:ni+1]...)
		if dir != "" && viewInterpreters[name] {
			parts = append(parts, dir)
		}
		if !masked {
			for _, a := range args {
				parts = append(parts, joinViewPath(dir, a, true))
			}
		}
		for i, r := range c.redirs {
			parts = append(parts, r.op+" "+joinViewPath(dir, redirs[i], false))
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out
}

// declWords take NAME=value arguments that assign variables.
var declWords = map[string]bool{"export": true, "local": true, "declare": true, "typeset": true, "readonly": true}

// assign records the assignment word w, whose expanded text is expanded: a
// leading NAME=value word or an argument of one of declWords.
func (st *viewState) assign(sc shellScript, w shWord, expanded string) {
	name, value, ok := strings.Cut(expanded, "=")
	if !ok || !isAssignment(w.raw) {
		return
	}
	raw := w.raw[strings.IndexByte(w.raw, '=')+1:]
	if strings.HasSuffix(raw, "$") && w.end < len(sc.src) && sc.src[w.end] == '(' {
		// NAME=$(...): the scanner split the substitution off.
		raw = "$" + sc.src[w.end:matchParen(sc.src, w.end)]
	}
	if strings.Contains(raw, "$(") || strings.Contains(raw, "`") {
		value = substPath(raw)
	}
	if len(value) > maxViewValue {
		st.budget.overflow = true
		return
	}
	st.vars[name] = value
}

// cd moves the candidate directories to the target of `cd args`.
func (st *viewState) cd(args []string, certain bool) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		args = args[1:] // -P, -L, -e
	}
	target := "~"
	if len(args) > 0 {
		target = args[0]
	}
	var next []string
	for _, d := range st.dirs {
		if target == "-" {
			next = append(next, "")
			continue
		}
		next = append(next, joinViewPath(d, target, false))
	}
	for _, d := range next {
		if len(d) > maxViewValue {
			st.budget.overflow = true
			return
		}
	}
	if certain {
		st.dirs = dedupe(next)
		return
	}
	st.addDirs(next)
}

func (st *viewState) addDirs(dirs []string) {
	st.dirs = dedupe(append(st.dirs, dirs...))
}

// expand substitutes $NAME and ${NAME} for the variables assigned so far.
func (st *viewState) expand(s string) (string, bool) {
	if len(st.vars) == 0 || !strings.Contains(s, "$") {
		return s, false
	}
	var b strings.Builder
	changed := false
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		j, braced := i+1, false
		if j < len(s) && s[j] == '{' {
			j, braced = j+1, true
		}
		k := j
		for k < len(s) && (s[k] == '_' || s[k] >= 'a' && s[k] <= 'z' || s[k] >= 'A' && s[k] <= 'Z' || k > j && s[k] >= '0' && s[k] <= '9') {
			k++
		}
		name := s[j:k]
		if braced {
			if k >= len(s) || s[k] != '}' {
				b.WriteByte(s[i])
				continue
			}
			k++
		}
		v, ok := st.vars[name]
		if name == "" || !ok {
			b.WriteByte(s[i])
			continue
		}
		b.WriteString(v)
		changed = true
		i = k - 1
		if b.Len() > maxViewValue {
			break
		}
	}
	if b.Len() > maxViewValue {
		st.budget.overflow = true
	}
	return b.String(), changed
}

// nestedScript returns the script a `sh -c script` or `eval words...`
// command runs.
func nestedScript(name string, args []string) (string, bool) {
	if name == "eval" {
		return strings.Join(args, " "), len(args) > 0
	}
	if !viewShells[name] {
		return "", false
	}
	for i, a := range args {
		if len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "c") && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// joinViewPath resolves word against dir for a view line. Absolute, ~ and
// $-rooted words and options are kept; in an argument (isArg) the value of
// a key=value word (dd of=x) is resolved too.
func joinViewPath(dir, word string, isArg bool) string {
	if dir == "" || word == "" {
		return word
	}
	if isArg {
		if strings.HasPrefix(word, "-") {
			return word
		}
		if k, v, ok := strings.Cut(word, "="); ok && !strings.ContainsAny(k, "/$~") {
			return k + "=" + joinViewPath(dir, v, false)
		}
	}
	switch word[0] {
	case '/', '~', '$':
		return word
	}
	return cleanViewPath(dir + "/" + word)
}

// cleanViewPath cleans p unless that would climb out of its ~ or $VAR root
// ("$HOME/.." must not become "$HOME").
func cleanViewPath(p string) string {
	root, rest, ok := strings.Cut(p, "/")
	if strings.HasPrefix(root, "~") || strings.HasPrefix(root, "$") {
		if !ok {
			return p
		}
		c := path.Clean(rest)
		if c == ".." || strings.HasPrefix(c, "../") {
			return p
		}
		if c == "." {
			return root
		}
		return root + "/" + c
	}
	return path.Clean(p)
}

// substPath turns the text of a command substitution into one path-shaped
// word: quotes, parentheses, backticks and whitespace become slashes.
func substPath(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '"', '\'', '(', ')', '`', ';', '|', '&':
			return '/'
		}
		return r
	}, s)
}

// matchParen returns the index after the parenthesis that closes the one
// at s[open], or len(s).
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

func dedupe(dirs []string) []string {
	seen := map[string]bool{}
	out := dirs[:0:0]
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if len(out) > maxViewDirs {
		// Keep the starting dir and the newest targets.
		out = append(out[:1:1], out[len(out)-maxViewDirs+1:]...)
	}
	return out
}

func copyVars(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// lineAnalysis is a command line scanned once for every tamper rule.
type lineAnalysis struct {
	sc       shellScript
	views    []string
	overflow bool
}

// lastLine memoizes analyzeLine: checkGlobalDenyListIn runs every rule on
// the same line.
var lastLine struct {
	sync.Mutex
	command string
	a       *lineAnalysis
}

// analyzeLine returns command's scan and write views, reusing the previous
// result for the same line.
func analyzeLine(command string) *lineAnalysis {
	lastLine.Lock()
	defer lastLine.Unlock()
	if lastLine.a != nil && lastLine.command == command {
		return lastLine.a
	}
	sc := scanShell(command)
	views, overflow := writeViews(sc)
	a := &lineAnalysis{sc: sc, views: views, overflow: overflow}
	lastLine.command, lastLine.a = command, a
	return a
}
