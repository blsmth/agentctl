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
	for _, c := range goRunScan(rawInvocationTokens(command), cwd) {
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
// `agentctl dispatch`. The hook does not thread the Bash cwd this far, so a
// `.` with no absolute `cd` before it counts as agentctl (fail toward the
// gate). On a scanner panic every `go run` counts (fail closed).
func rewriteGoRunAgentctl(tokens []cmdToken) (out []cmdToken) {
	defer func() {
		if r := recover(); r != nil {
			logging.Warn("hook-validate-bash: go run scan panicked: %v", r)
			out = rewriteGoRunCrude(tokens)
		}
	}()
	calls := goRunScan(tokens, "")
	if len(calls) == 0 {
		return tokens
	}
	last := 0
	for _, c := range calls {
		if c.verdict == notRoot {
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
// `go run` and the words up to its first non-flag word become `agentctl`.
func rewriteGoRunCrude(tokens []cmdToken) []cmdToken {
	var out []cmdToken
	for i := 0; i < len(tokens); i++ {
		if i+1 < len(tokens) && tokens[i].text == "go" && tokens[i+1].text == "run" {
			j := i + 2
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

// goRunCalls finds the `go run` commands among tokens (tokenizeQuoted
// output) and classifies the package each one runs. Only a word in command
// position counts: a quoted argument (`git commit -m "go run ."`, an echo)
// is one token and never a command. cwd seeds the directory `cd` and `-C`
// resolve against; "" is unknown.
func goRunCalls(tokens []cmdToken, cwd string) []goRunCall {
	dir := shellDir{path: cwd, known: cwd != ""}
	var calls []goRunCall
	for _, cmd := range simpleCommands(tokens) {
		words := cmd.words
		k := commandWordIndex(words)
		if k < 0 {
			continue
		}
		name := words[k]
		switch {
		case name.tok.expands:
			continue
		case name.tok.text == "cd" || name.tok.text == "pushd":
			dir = cdTarget(dir, words[k+1:])
		case filepath.Base(name.tok.text) == "go" && k+1 < len(words) && words[k+1].tok.text == "run":
			if c, ok := classifyGoRun(words[k+2:], dir); ok {
				c.start, c.prefix = name.idx, name.prefix
				calls = append(calls, c)
			}
		}
	}
	return calls
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
}

// simpleCommand is the words of one simple command, redirections dropped.
type simpleCommand struct {
	words []cmdWord
}

// simpleCommands splits tokens at unquoted control operators (also attached
// ones such as "x;" or "a&&b", via splitControl) and parentheses, dropping
// redirections and their targets.
func simpleCommands(tokens []cmdToken) []simpleCommand {
	var cmds []simpleCommand
	var cur simpleCommand
	end := func() {
		if len(cur.words) > 0 {
			cmds = append(cmds, cur)
		}
		cur = simpleCommand{}
	}
	skipTarget := false
	for i, t := range tokens {
		if skipTarget {
			skipTarget = false
			continue
		}
		if t.quoted {
			cur.words = append(cur.words, cmdWord{tok: t, idx: i, endIdx: i + 1})
			continue
		}
		if isRedirection(t.text) {
			skipTarget = redirectionNeedsTarget(t.text)
			continue
		}
		pieces := splitControl(strings.NewReplacer("(", ";", ")", ";").Replace(t.text))
		for p, piece := range pieces {
			if piece == "" {
				end()
				continue
			}
			endIdx := i
			if p == len(pieces)-1 {
				endIdx = i + 1
			}
			var prefix string
			if p > 0 {
				prefix = t.text[:strings.LastIndex(t.text, piece)]
			}
			// Only a piece that itself has a $ or backtick expands: in
			// "x=$(go" the "go" is a plain command word.
			expands := t.expands && strings.ContainsAny(piece, "$`\\")
			cur.words = append(cur.words, cmdWord{tok: cmdToken{text: piece, expands: expands}, idx: i, endIdx: endIdx, prefix: prefix})
		}
	}
	end()
	return cmds
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
	} else {
		verdict = classifyPackage(args[i].tok, dir)
		last = i + 1
	}
	if last < len(args) && args[last].tok.text == "--" {
		last++
	}
	return goRunCall{end: args[last-1].endIdx, verdict: verdict}, true
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
