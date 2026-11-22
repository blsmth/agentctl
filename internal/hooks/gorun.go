package hooks

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
)

// Running agentctl from source (`cd ~/code/personal/agentctl && go run .`)
// is denied: agentctl is on PATH, and a binary built from a worktree can
// carry edits that skip the CLI's own checks. The rule is tokenized rather
// than a regex so it fires only when `go run` is the command and the package
// it runs is the agentctl root main package, not on `go run
// ./internal/serve/servetest/devserver` in this repo or on a message body
// that mentions both words. The dispatch, cron and serve-tamper rules share
// the detection (goRunCalls) so `go run . dispatch` is still gated as a
// dispatch.

// goRunAgentctlReason is the deny reason for running agentctl from source.
const goRunAgentctlReason = "\U0001F6D1 BLOCKED: never invoke agentctl via 'go run' -- call 'agentctl' directly from PATH instead"

// agentctlModule is the module path of the agentctl root main package.
const agentctlModule = "github.com/blsmth/agentctl"

// rootVerdict says whether a `go run` runs the agentctl root package.
type rootVerdict int

const (
	notRoot rootVerdict = iota
	// rootUnknown: the package is a local path (., ./, main.go) whose
	// directory cannot be resolved (no cwd, a `cd $X`).
	rootUnknown
	isRoot
)

// goRunCall is one `go run` simple command.
type goRunCall struct {
	start, end int // tokens[start:end] are `go run [flags] <pkg> [--]`
	// prefix is the text of tokens[start] before the go word when an
	// operator is attached to it ("x&&go").
	prefix  string
	verdict rootVerdict
	// substAt is the token index where a command substitution package
	// word opens (`go run $(pwd)`), else -1.
	substAt int
}

// shellDir is the directory a simple command runs in, as far as the scanner
// can tell.
type shellDir struct {
	path  string
	known bool
}

// checkGoRunAgentctl reports goRunAgentctlReason when command runs the
// agentctl root package with go run, resolving `.` against cwd (the Bash
// tool's directory; "" when unknown) and any `cd`/`-C` before it. A local
// package whose directory cannot be resolved is denied only when the line
// mentions agentctl. It is a convenience rule, so a scanner panic allows the
// command (the gates fail closed on their own).
func checkGoRunAgentctl(command, cwd string) (reason string) {
	defer func() {
		if r := recover(); r != nil {
			logging.Warn("hook-validate-bash: go run scan panicked: %v", r)
			reason = ""
		}
	}()
	// The main line is scanned with its cd chain. A nested command line (a
	// bash -c or "$(...)" body, appended after the main line) is scanned
	// from cwd and from an unknown dir, since where it runs relative to the
	// main line's cds is not modeled.
	main, nested := invocationParts(command)
	calls := goRunScan(tokenizeQuoted(strings.TrimSpace(main)), cwd, scanDeny)
	for _, line := range nested {
		toks := tokenizeQuoted(strings.TrimSpace(line))
		calls = append(calls, goRunScan(toks, cwd, scanDeny)...)
		calls = append(calls, goRunScan(toks, "", scanDeny)...)
	}
	for _, c := range calls {
		switch c.verdict {
		case isRoot:
			return goRunAgentctlReason
		case rootUnknown:
			if strings.Contains(strings.ToLower(command), "agentctl") {
				return goRunAgentctlReason
			}
		}
	}
	return ""
}

// rewriteGoRunAgentctl replaces every `go run <pkg> [--]` that runs, or may
// run, the agentctl root package with one `agentctl` token marked
// fromSource, so the dispatch and cron gates see `go run . dispatch` as
// `agentctl dispatch`. It scans in scanGate mode: the Bash cwd is not
// threaded this far and cd is ignored (a cd in a subshell, a pipeline or a
// failed && chain does not move the outer shell), so a `.` always counts as
// agentctl (fail toward the gate); only go's own -C and absolute paths can
// show it is another package. A `go run` anywhere in a simple command
// counts, behind a wrapper (timeout, xargs, sudo) too, like the binary-path
// match in dispatchSegments. On a scanner panic every `go run` counts (fail
// closed).
func rewriteGoRunAgentctl(tokens []cmdToken) (out []cmdToken) {
	defer func() {
		if r := recover(); r != nil {
			logging.Warn("hook-validate-bash: go run scan panicked: %v", r)
			out = rewriteGoRunCrude(tokens)
		}
	}()
	calls := goRunScan(tokens, "", scanGate)
	if len(calls) == 0 {
		return tokens
	}
	last := 0
	for _, c := range calls {
		if c.verdict == notRoot || c.start < last || c.end < c.start {
			continue
		}
		out = append(out, tokens[last:c.start]...)
		if c.prefix != "" {
			out = append(out, cmdToken{text: c.prefix})
		}
		out = append(out, cmdToken{text: "agentctl", fromSource: true})
		last = c.end
	}
	if last == 0 {
		return tokens
	}
	return append(out, tokens[last:]...)
}

// rewriteGoRunCrude is the panic fallback of rewriteGoRunAgentctl: each
// `go [-C dir] run`, its flags and its first non-flag word become
// `agentctl`.
func rewriteGoRunCrude(tokens []cmdToken) []cmdToken {
	var out []cmdToken
	for i := 0; i < len(tokens); i++ {
		if j, ok := crudeRunIndex(tokens, i); ok {
			j++
			for j < len(tokens) && strings.HasPrefix(tokens[j].text, "-") {
				j++
			}
			out = append(out, cmdToken{text: "agentctl", fromSource: true})
			i = j // skips the package word too
			continue
		}
		out = append(out, tokens[i])
	}
	return out
}

// crudeRunIndex returns the index of the `run` word when tokens[i] starts
// `go [-C dir | -C=dir] run`.
func crudeRunIndex(tokens []cmdToken, i int) (int, bool) {
	if filepath.Base(tokens[i].text) != "go" {
		return 0, false
	}
	j := i + 1
	switch {
	case j < len(tokens) && (tokens[j].text == "-C" || tokens[j].text == "--C"):
		j += 2
	case j < len(tokens) && (strings.HasPrefix(tokens[j].text, "-C=") || strings.HasPrefix(tokens[j].text, "--C=")):
		j++
	}
	return j, j < len(tokens) && tokens[j].text == "run"
}

// lineRunsAgentctlFromSource reports whether command invokes agentctl
// through go run (rewriteGoRunAgentctl). Such a binary is built from
// whatever source is on disk, so no gate exemption applies to it.
func lineRunsAgentctlFromSource(command string) bool {
	for _, t := range invocationTokens(command) {
		if t.fromSource {
			return true
		}
	}
	return false
}

// goRunScan is goRunCalls; a var so tests can force a panic.
var goRunScan = goRunCalls

// scanMode picks how goRunCalls reads a line.
type scanMode int

const (
	// scanDeny (the deny rule): `go run` counts as the command word or
	// behind a known wrapper (goWrappers), and cd moves the directory when
	// it surely runs before the go run (cdCertain; a subshell's cd is undone
	// at its closing paren).
	scanDeny scanMode = iota
	// scanGate (the dispatch, cron and serve rules): any `go run` word pair
	// in a simple command counts and cd is ignored; see
	// rewriteGoRunAgentctl.
	scanGate
)

// goWrappers run their arguments as a command, so in scanDeny mode a
// `go run` after one counts.
var goWrappers = map[string]bool{
	"timeout": true, "gtimeout": true, "nice": true, "sudo": true, "doas": true,
	"xargs": true, "caffeinate": true, "stdbuf": true, "ionice": true, "chrt": true,
	"taskset": true, "watch": true, "unbuffer": true, "chronic": true, "flock": true,
	"setsid": true, "script": true,
}

// goRunCalls finds the `go run` commands among tokens (tokenizeQuoted
// output) and classifies the package each one runs. A quoted argument
// (`git commit -m "go run ."`) is one token and never a command. cwd seeds
// the directory `cd` and `-C` resolve against; "" is unknown.
func goRunCalls(tokens []cmdToken, cwd string, mode scanMode) []goRunCall {
	s := goRunScanner{tokens: tokens, mode: mode, dir: shellDir{path: cwd, known: cwd != ""}}
	var cur []cmdWord
	before := ""
	for _, it := range shellItems(tokens) {
		if it.op == "" {
			cur = append(cur, it.word)
			continue
		}
		s.command(cur, before, it.op)
		cur = nil
		switch it.op {
		case "(":
			s.stack = append(s.stack, s.dir)
		case ")":
			if n := len(s.stack); n > 0 {
				s.dir, s.stack = s.stack[n-1], s.stack[:n-1]
			}
		}
		before = it.op
	}
	s.command(cur, before, "")
	return s.calls
}

// goRunScanner is goRunCalls' state.
type goRunScanner struct {
	tokens []cmdToken
	mode   scanMode
	dir    shellDir
	stack  []shellDir // directories saved at each open paren
	calls  []goRunCall
}

// command handles one simple command between the operators before and
// after it.
func (s *goRunScanner) command(words []cmdWord, before, after string) {
	k := commandWordIndex(words)
	if k < 0 {
		return
	}
	name := words[k].tok
	if s.mode == scanDeny && !name.expands {
		switch name.text {
		case "cd", "pushd":
			// A cd that may not run (or not in this shell) moves the
			// directory only toward the agentctl root: the deny side.
			if target := cdTarget(s.dir, words[k+1:]); cdCertain(before, after) || (target.known && isAgentctlRoot(target.path)) {
				s.dir = target
			}
			return
		case "popd":
			s.dir = shellDir{}
			return
		}
	}
	from, to := k, k+1
	switch {
	case s.mode == scanGate:
		from, to = 0, len(words)
	case goWrappers[filepath.Base(name.text)]:
		from, to = k+1, len(words)
	}
	for j := from; j < to; j++ {
		c, ok := goRunAt(words, j, s.dir)
		if !ok {
			continue
		}
		c.start, c.prefix = words[j].idx, words[j].prefix
		if c.substAt >= 0 {
			c.end = substEnd(s.tokens, c.substAt)
			if c.end < len(s.tokens) && s.tokens[c.end].text == "--" {
				c.end++
			}
		}
		s.calls = append(s.calls, c)
		return
	}
}

// cdCertain reports whether a cd between the operators before and after
// surely runs, and in the shell that runs what follows: not after || or in
// a pipeline or background job, and either followed by && or not itself
// behind a && (`false && cd x; go run .` may skip the cd).
func cdCertain(before, after string) bool {
	switch before {
	case "||", "|", "|&":
		return false
	}
	switch after {
	case "&&":
		return true
	case "", ";", ";;", ")":
		return before != "&&"
	}
	return false
}

// goRunAt matches `go [-C dir] run <args>` starting at words[j] and
// classifies it.
func goRunAt(words []cmdWord, j int, dir shellDir) (goRunCall, bool) {
	if w := words[j].tok; w.expands || filepath.Base(w.text) != "go" {
		return goRunCall{}, false
	}
	m := j + 1
	for m < len(words) && strings.HasPrefix(words[m].tok.text, "-") {
		t := words[m].tok
		switch name, value, hasValue := strings.Cut(strings.TrimLeft(t.text, "-"), "="); {
		case name != "C":
			return goRunCall{}, false // go takes only -C before the subcommand
		case hasValue:
			dir = resolveWordDir(dir, cmdToken{text: value, quoted: t.quoted, expands: t.expands})
			m++
		case m+1 < len(words):
			dir = resolveWordDir(dir, words[m+1].tok)
			m += 2
		default:
			return goRunCall{}, false
		}
	}
	if m >= len(words) || words[m].tok.text != "run" {
		return goRunCall{}, false
	}
	return classifyGoRun(words[m+1:], dir)
}

// substEnd returns the token index after the command substitution that
// opens in tokens[i] ("$(pwd)", or "$(cat" ... "f)"), by paren balance.
func substEnd(tokens []cmdToken, i int) int {
	depth := 0
	for j := i; j < len(tokens); j++ {
		if !tokens[j].quoted {
			depth += strings.Count(tokens[j].text, "(") - strings.Count(tokens[j].text, ")")
		}
		if depth <= 0 {
			return j + 1
		}
	}
	return len(tokens)
}

// cmdWord is one word of a simple command and its index in the token slice.
type cmdWord struct {
	tok cmdToken
	idx int // index of the token the word came from
	// prefix is the token text before this word when the token carries
	// more than one ("x&&go" for the word go).
	prefix string
	// endIdx is the token index after the word: idx+1, or idx when the
	// token carries more words (an attached operator such as "x&&go").
	endIdx int
	// substOpen marks a word ending in $ right before a ( in the same
	// token: the start of a command substitution ("$(pwd)").
	substOpen bool
}

// shellItem is a word or a control operator (op: ; && || | & |& ;; ( )).
type shellItem struct {
	word cmdWord
	op   string
}

// shellItems splits tokens into words and control operators, including
// operators attached to words ("x;", "a&&b", "(cd") and parentheses,
// dropping redirections and their targets.
func shellItems(tokens []cmdToken) []shellItem {
	var items []shellItem
	skipTarget := false
	for i, t := range tokens {
		if skipTarget {
			skipTarget = false
			continue
		}
		if t.quoted {
			items = append(items, quotedTokenItems(t, i)...)
			continue
		}
		if isRedirection(t.text) {
			skipTarget = redirectionNeedsTarget(t.text)
			continue
		}
		items = append(items, splitWordOps(t, i)...)
	}
	return items
}

// quotedTokenItems is a quoted token as one word, except that control
// operators in its unquoted tail end it: in `bash -c 'cd /tmp'; go run .`
// the ";" is the shell's, not part of the word.
func quotedTokenItems(t cmdToken, i int) []shellItem {
	tail := t.text[min(t.unquotedTail, len(t.text)):]
	cut := strings.IndexAny(tail, ";&|()")
	if cut < 0 {
		return []shellItem{{word: cmdWord{tok: t, idx: i, endIdx: i + 1}}}
	}
	at := len(t.text) - len(tail) + cut
	w := t
	w.text = t.text[:at]
	items := []shellItem{{word: cmdWord{tok: w, idx: i, endIdx: i}}}
	rest := splitWordOps(cmdToken{text: t.text[at:], expands: t.expands}, i)
	for k := range rest {
		if rest[k].op == "" {
			rest[k].word.prefix = t.text[:at] + rest[k].word.prefix
		}
	}
	return append(items, rest...)
}

// splitWordOps splits one unquoted token at its control operator bytes.
func splitWordOps(t cmdToken, i int) []shellItem {
	var items []shellItem
	text := t.text
	start := 0
	word := func(end int) {
		if end <= start {
			return
		}
		piece := text[start:end]
		// Only a piece that itself has a $ or backtick expands: in
		// "x=$(go" the "go" is a plain command word.
		w := cmdWord{
			tok:       cmdToken{text: piece, expands: t.expands && strings.ContainsAny(piece, "$`\\")},
			idx:       i,
			prefix:    text[:start],
			endIdx:    i,
			substOpen: strings.HasSuffix(piece, "$") && end < len(text) && text[end] == '(',
		}
		if end == len(text) {
			w.endIdx = i + 1
		}
		items = append(items, shellItem{word: w})
	}
	for j := 0; j < len(text); j++ {
		c := text[j]
		switch {
		case c == '(' || c == ')':
			word(j)
			items = append(items, shellItem{op: string(c)})
			start = j + 1
		case strings.IndexByte(";&|", c) >= 0:
			word(j)
			k := j
			for k+1 < len(text) && strings.IndexByte(";&|", text[k+1]) >= 0 {
				k++
			}
			items = append(items, shellItem{op: text[j : k+1]})
			start, j = k+1, k
		}
	}
	word(len(text))
	return items
}

// commandPrefixWords may precede the command word without being it:
// keywords and wrappers that run the rest of the line as a command.
var commandPrefixWords = map[string]bool{
	"!": true, "{": true, "}": true, "if": true, "then": true, "else": true, "elif": true,
	"do": true, "while": true, "until": true, "time": true, "exec": true, "command": true,
	"nohup": true, "builtin": true,
}

// commandWordIndex returns the index of the command word in words, past
// variable assignments, keywords and `env [-i] [-u NAME] [NAME=v]...`, or
// -1 when there is none.
func commandWordIndex(words []cmdWord) int {
	inEnv := false
	for k := 0; k < len(words); k++ {
		t := words[k].tok
		switch {
		case t.quoted && !isAssignment(t.text):
			return k
		case isAssignment(t.text):
			continue
		case commandPrefixWords[t.text]:
			continue
		case t.text == "env" || strings.HasSuffix(t.text, "/env"):
			inEnv = true
			continue
		case inEnv && strings.HasPrefix(t.text, "-"):
			if t.text == "-u" || t.text == "-C" || t.text == "-S" {
				k++ // the flag's value
			}
			continue
		}
		return k
	}
	return -1
}

// cdTarget is the directory after `cd args...` run in dir.
func cdTarget(dir shellDir, args []cmdWord) shellDir {
	for len(args) > 0 && !args[0].tok.quoted && strings.HasPrefix(args[0].tok.text, "-") && args[0].tok.text != "-" {
		args = args[1:] // -P, -L, -e
	}
	if len(args) == 0 {
		home, err := os.UserHomeDir()
		return shellDir{path: home, known: err == nil && home != ""}
	}
	if args[0].tok.text == "-" {
		return shellDir{}
	}
	return resolveWordDir(dir, args[0].tok)
}

// resolveWordDir resolves a path word against dir: ~ and $HOME are
// expanded, any other expansion makes the result unknown.
func resolveWordDir(dir shellDir, t cmdToken) shellDir {
	p, ok := expandPathWord(t)
	if !ok {
		return shellDir{}
	}
	if filepath.IsAbs(p) {
		return shellDir{path: filepath.Clean(p), known: true}
	}
	if !dir.known {
		return shellDir{}
	}
	return shellDir{path: filepath.Join(dir.path, p), known: true}
}

// expandPathWord returns the path a word names, expanding a leading
// unquoted ~ and $HOME / ${HOME}. Any other expansion is not resolvable.
func expandPathWord(t cmdToken) (string, bool) {
	p := t.text
	home := func() (string, bool) {
		h, err := os.UserHomeDir()
		return h, err == nil && h != ""
	}
	for _, v := range []string{"${HOME}", "$HOME"} {
		if p == v || strings.HasPrefix(p, v+"/") {
			h, ok := home()
			if !ok {
				return "", false
			}
			p = h + p[len(v):]
			break
		}
	}
	if strings.ContainsAny(p, "$`") || strings.HasSuffix(p, `\`) {
		return "", false
	}
	if !t.quoted && (p == "~" || strings.HasPrefix(p, "~/")) {
		h, ok := home()
		if !ok {
			return "", false
		}
		p = h + p[1:]
	} else if !t.quoted && strings.HasPrefix(p, "~") {
		return "", false // ~user
	}
	return p, true
}

// goRunValueFlags are the build flags of `go run` that take a separate
// value (go help build).
var goRunValueFlags = map[string]bool{
	"C": true, "p": true, "asmflags": true, "buildmode": true, "compiler": true,
	"gccgoflags": true, "gcflags": true, "installsuffix": true, "ldflags": true,
	"mod": true, "modfile": true, "overlay": true, "pgo": true, "pkgdir": true,
	"tags": true, "toolexec": true, "exec": true, "covermode": true, "coverpkg": true,
	"o": true,
}

// classifyGoRun parses the words after `go run` and classifies the package.
// The returned call's end is the token index after the package words (and a
// following `--`); ok is false when there is no package word.
func classifyGoRun(args []cmdWord, dir shellDir) (goRunCall, bool) {
	i := 0
	for i < len(args) {
		t := args[i].tok.text
		if t == "--" {
			i++
			break
		}
		if !strings.HasPrefix(t, "-") || t == "-" {
			break
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(t, "-"), "=")
		var value cmdToken
		if hasValue {
			value = cmdToken{text: t[strings.IndexByte(t, '=')+1:], quoted: args[i].tok.quoted, expands: args[i].tok.expands}
		} else if goRunValueFlags[name] {
			if i+1 >= len(args) {
				return goRunCall{}, false
			}
			i++
			value = args[i].tok
		}
		if name == "C" {
			dir = resolveWordDir(dir, value)
		}
		i++
	}
	if i >= len(args) {
		return goRunCall{}, false
	}
	var verdict rootVerdict
	last := i
	if strings.HasSuffix(args[i].tok.text, ".go") {
		verdict = notRoot
		for last < len(args) && strings.HasSuffix(args[last].tok.text, ".go") {
			verdict = maxVerdict(verdict, classifyGoFile(args[last].tok, dir))
			last++
		}
	} else if args[i].substOpen {
		// `go run $(pwd) ...`: the package is the substitution's output,
		// which ends where its parens balance (goRunScanner sets end).
		return goRunCall{verdict: rootUnknown, substAt: args[i].idx}, true
	} else {
		verdict = classifyPackage(args[i].tok, dir)
		last = i + 1
	}
	if last < len(args) && args[last].tok.text == "--" {
		last++
	}
	return goRunCall{end: args[last-1].endIdx, verdict: verdict, substAt: -1}, true
}

func maxVerdict(a, b rootVerdict) rootVerdict {
	if b > a {
		return b
	}
	return a
}

// classifyGoFile classifies `go run <file>.go`: its package is the
// directory holding the file.
func classifyGoFile(t cmdToken, dir shellDir) rootVerdict {
	p, ok := expandPathWord(t)
	if !ok {
		return rootUnknown
	}
	d := filepath.Dir(p)
	if strings.ContainsAny(d, "*?[") {
		return rootUnknown
	}
	return classifyLocal(d, dir)
}

// classifyPackage classifies the package word of `go run <pkg>`.
func classifyPackage(t cmdToken, dir shellDir) rootVerdict {
	if !t.expands {
		if mod, _, _ := strings.Cut(t.text, "@"); mod == agentctlModule {
			return isRoot
		}
	}
	p, ok := expandPathWord(t)
	if !ok {
		return rootUnknown
	}
	if !isLocalPackagePath(p, t.quoted) {
		return notRoot // an import path: another module or a subpackage
	}
	if strings.Contains(p, "...") {
		return notRoot // a pattern; go run needs a single main package
	}
	return classifyLocal(p, dir)
}

// classifyLocal classifies the package directory p (expanded, maybe
// relative to dir). When dir is unknown, only a path that could plausibly be
// the agentctl root is rootUnknown: ".", "..", "../.." and the like, or one
// naming agentctl or a worktree. "./internal/foo" is notRoot.
func classifyLocal(p string, dir shellDir) rootVerdict {
	resolved := resolveWordDir(dir, cmdToken{text: p, quoted: true})
	if resolved.known {
		return classifyDir(resolved)
	}
	if rootLookingPath(p) {
		return rootUnknown
	}
	return notRoot
}

// rootLookingPath reports whether a relative package path may name the
// agentctl root from an unknown directory.
func rootLookingPath(p string) bool {
	lower := strings.ToLower(p)
	if strings.Contains(lower, "agentctl") || strings.Contains(lower, ".worktrees") {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(p)), "/") {
		if part != "." && part != ".." {
			return false
		}
	}
	return true
}

// isLocalPackagePath reports whether go treats a package argument as a
// directory rather than an import path.
func isLocalPackagePath(p string, quoted bool) bool {
	return p == "." || p == ".." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") ||
		filepath.IsAbs(p) || (!quoted && strings.HasPrefix(p, "~"))
}

// classifyDir is isRoot when the known dir holds the agentctl module's
// go.mod.
func classifyDir(dir shellDir) rootVerdict {
	if isAgentctlRoot(dir.path) {
		return isRoot
	}
	return notRoot
}

// isAgentctlRoot reports whether dir contains a go.mod declaring the
// agentctl module.
func isAgentctlRoot(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	return goModModule(data) == agentctlModule
}

// goModModule returns the module path declared in go.mod data.
func goModModule(data []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module")
		if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '"') {
			continue
		}
		return strings.Trim(strings.TrimSpace(rest), `"`)
	}
	return ""
}

// goRunAgentctlArgs returns the program arguments of every `go run` that
// runs, or may run, the agentctl root package (as rewriteGoRunAgentctl
// decides): the words after the package, up to the end of the command.
func goRunAgentctlArgs(command string) [][]cmdToken {
	tokens := invocationTokens(command)
	var out [][]cmdToken
	for i, t := range tokens {
		if t.fromSource {
			out = append(out, takeSegment(tokens[i+1:]))
		}
	}
	return out
}
