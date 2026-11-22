package sessionaudit

import (
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// dynamic marks a part of a word whose value is only known at run time (a
// command substitution, arithmetic, a glob the shell expands, ...). A word
// holding it is never matched against a literal path.
const dynamic = "\x00"

// maxShellDepth caps how deep `bash -c "..."` and `bash <<EOF` bodies are
// parsed as commands.
const maxShellDepth = 3

// simpleCmd is one simple command of a Bash tool call, after wrappers such as
// sudo, env, timeout and xargs are peeled off.
type simpleCmd struct {
	src      string   // the command as written, from the source text
	args     []string // words; quoted parts unquoted, $VAR kept as written
	name     string   // basename of args[0]
	sudo     bool     // run through sudo
	outFiles []string // targets of > and >> redirections
	heredoc  bool     // has a heredoc on stdin
	pipedIn  bool     // reads a pipe (right side of |)
	inLoop   bool     // inside a for/while/until body
	afterCd  bool     // a cd/pushd ran earlier in the same command line
	dir      string   // where an earlier literal cd went ("" = no cd, or not known)
}

// pipe is one | of a pipeline: every command on its left and on its right.
type pipe struct {
	left, right []string // command names
	src         string
}

// loop is a for/while/until loop whose body runs sleep.
type loop struct {
	src string
}

// parsedBash is a Bash command split into what a rule can check.
type parsedBash struct {
	cmds     []simpleCmd
	pipes    []pipe
	polls    []loop
	unparsed bool // mvdan could not parse it: treated as one opaque command
}

// parseBash parses command. A command that does not parse is returned as
// unparsed with no commands, so no rule can fire on a guess.
func parseBash(command string) *parsedBash {
	p := &parsedBash{}
	if !p.add(command, 0) {
		return &parsedBash{unparsed: true}
	}
	return p
}

// add parses src and appends its commands; false on a parse error.
func (p *parsedBash) add(src string, depth int) bool {
	f, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		return false
	}
	text := func(n syntax.Node) string {
		s, e := int(n.Pos().Offset()), int(n.End().Offset())
		if s < 0 || e > len(src) || s > e {
			return ""
		}
		return src[s:e]
	}

	byCall := map[*syntax.CallExpr]int{}
	var nested []string
	seenCd, curDir := false, ""
	syntax.Walk(f, func(n syntax.Node) bool {
		st, ok := n.(*syntax.Stmt)
		if !ok {
			return true
		}
		call, ok := st.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		c := simpleCmd{src: strings.TrimRight(strings.TrimSpace(text(st)), ";& ")}
		for _, w := range call.Args {
			c.args = append(c.args, wordString(w))
		}
		var hdocBody string
		for _, r := range st.Redirs {
			switch r.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob:
				if r.Word != nil {
					if t := wordString(r.Word); t != "/dev/null" {
						c.outFiles = append(c.outFiles, t)
					}
				}
			case syntax.Hdoc, syntax.DashHdoc:
				c.heredoc = true
				if r.Hdoc != nil {
					hdocBody = wordString(r.Hdoc)
				}
			}
		}
		c.args, c.sudo = unwrap(c.args)
		if len(c.args) == 0 {
			return true
		}
		c.name = path.Base(c.args[0])
		c.afterCd, c.dir = seenCd, curDir
		if c.name == "cd" || c.name == "pushd" {
			seenCd = true
			curDir = cdTarget(c.args, curDir)
		}
		byCall[call] = len(p.cmds)
		p.cmds = append(p.cmds, c)
		if shellNames[c.name] && depth < maxShellDepth {
			if s := shellScript(c.args); s != "" {
				nested = append(nested, s)
			} else if hdocBody != "" && !hasScriptArg(c.args) {
				nested = append(nested, hdocBody)
			}
		}
		return true
	})

	// A shell running fetched code: sh -c "$(curl ...)", bash <(curl ...),
	// eval "$(curl ...)", source <(curl ...).
	for call, i := range byCall {
		if !shellNames[p.cmds[i].name] && !evalNames[p.cmds[i].name] {
			continue
		}
		var fetched []string
		for _, w := range call.Args[1:] {
			syntax.Walk(w, func(n syntax.Node) bool {
				switch n.(type) {
				case *syntax.CmdSubst, *syntax.ProcSubst:
					for _, c := range callsIn(n) {
						if j, ok := byCall[c]; ok && fetchNames[p.cmds[j].name] {
							fetched = append(fetched, p.cmds[j].name)
						}
					}
					return false
				}
				return true
			})
		}
		if len(fetched) > 0 {
			p.pipes = append(p.pipes, pipe{left: fetched, right: []string{"sh"}, src: p.cmds[i].src})
		}
	}

	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.BinaryCmd:
			if x.Op != syntax.Pipe && x.Op != syntax.PipeAll {
				return true
			}
			left, right := callsIn(x.X), callsIn(x.Y)
			pp := pipe{src: text(x)}
			for _, c := range left {
				if i, ok := byCall[c]; ok {
					pp.left = append(pp.left, p.cmds[i].name)
				}
			}
			for _, c := range right {
				if i, ok := byCall[c]; ok {
					p.cmds[i].pipedIn = true
					pp.right = append(pp.right, p.cmds[i].name)
				}
			}
			p.pipes = append(p.pipes, pp)
		case *syntax.WhileClause, *syntax.ForClause:
			sleeps := false
			for _, c := range callsIn(x) {
				if i, ok := byCall[c]; ok {
					p.cmds[i].inLoop = true
					if p.cmds[i].name == "sleep" {
						sleeps = true
					}
				}
			}
			if sleeps {
				p.polls = append(p.polls, loop{src: text(x)})
			}
		}
		return true
	})

	for _, s := range nested {
		p.add(s, depth+1) // a body that does not parse adds nothing
	}
	return true
}

// callsIn returns every call expression under n, leftmost first.
func callsIn(n syntax.Node) []*syntax.CallExpr {
	var out []*syntax.CallExpr
	syntax.Walk(n, func(m syntax.Node) bool {
		if c, ok := m.(*syntax.CallExpr); ok && len(c.Args) > 0 {
			out = append(out, c)
		}
		return true
	})
	return out
}

// wordString renders a word as the shell would see it before expansion:
// quotes removed, $VAR kept as written, anything computed marked dynamic.
func wordString(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		writePart(&b, part)
	}
	return b.String()
}

func writePart(b *strings.Builder, part syntax.WordPart) {
	switch x := part.(type) {
	case *syntax.Lit:
		b.WriteString(x.Value)
	case *syntax.SglQuoted:
		b.WriteString(x.Value)
	case *syntax.DblQuoted:
		for _, p := range x.Parts {
			writePart(b, p)
		}
	case *syntax.ParamExp:
		if x.Param != nil && x.Exp == nil && x.Repl == nil && x.Index == nil && !x.Length && !x.Excl {
			b.WriteString("$" + x.Param.Value)
			return
		}
		b.WriteString(dynamic)
	default:
		b.WriteString(dynamic)
	}
}

// shellNames are interpreters whose -c argument or heredoc is a script.
var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
}

// evalNames run their argument as shell code; fetchNames download.
var (
	evalNames  = map[string]bool{"eval": true, "source": true, ".": true}
	fetchNames = map[string]bool{"curl": true, "wget": true}
)

// shellValued are shell options that take the next argument as a value.
var shellValued = map[string]bool{
	"-o": true, "+o": true, "-O": true, "+O": true, "--rcfile": true, "--init-file": true,
}

// shellScript returns the -c script of a shell invocation, or "". Options
// before the -c cluster (-e, -o pipefail, --norc, ...) are skipped; the
// first operand (a script file) or "--" ends the search.
func shellScript(args []string) string {
	for i := 1; i < len(args)-1; i++ {
		a := args[i]
		switch {
		case shellValued[a]:
			i++
		case a == "--" || (!strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+")):
			return ""
		case strings.HasPrefix(a, "--"):
		case a[0] == '-' && strings.Contains(a[1:], "c"):
			if strings.Contains(args[i+1], dynamic) {
				return ""
			}
			return args[i+1]
		}
	}
	return ""
}

// cdTarget is where `cd <dir>` leaves the shell, given the dir an earlier
// cd left it in (prev, "" = the session cwd). A relative result is
// relative to the session cwd; dynamic marks a target that is not known.
func cdTarget(args []string, prev string) string {
	if prev == dynamic {
		return dynamic
	}
	_, ops := flagsAndOperands(args)
	if len(ops) != 1 {
		return dynamic // bare cd (home), cd -, or something odd
	}
	t := ops[0]
	switch {
	case t == "-" || strings.Contains(t, dynamic):
		return dynamic
	case strings.HasPrefix(t, "/"), t == "~", strings.HasPrefix(t, "~/"), strings.HasPrefix(t, "$"):
		return t
	case prev == "":
		return t
	}
	return path.Join(prev, t)
}

// hasScriptArg reports whether a shell invocation names a script file, in
// which case a heredoc is that script's stdin, not code.
func hasScriptArg(args []string) bool {
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "-") {
			return true
		}
	}
	return false
}

// unwrap peels command wrappers off args, returning the command they run and
// whether sudo was one of them.
func unwrap(args []string) (out []string, sudo bool) {
	for len(args) > 0 {
		name := path.Base(args[0])
		var valued map[string]bool
		switch name {
		case "sudo", "doas":
			sudo = true
			valued = map[string]bool{"-u": true, "-g": true, "-C": true, "-h": true, "-p": true, "-U": true}
		case "env":
			valued = map[string]bool{"-u": true, "-C": true, "-S": true}
		case "nohup", "time", "command", "exec", "builtin", "caffeinate":
		case "nice":
			valued = map[string]bool{"-n": true}
		case "timeout", "gtimeout":
			valued = map[string]bool{"-s": true, "-k": true, "--signal": true, "--kill-after": true}
		case "xargs":
			valued = map[string]bool{"-I": true, "-n": true, "-P": true, "-L": true, "-d": true, "-s": true, "-E": true}
		default:
			return args, sudo
		}
		i := 1
		for i < len(args) {
			a := args[i]
			switch {
			case a == "--":
				i++
				goto done
			case strings.HasPrefix(a, "-"):
				if valued[a] {
					i++
				}
				i++
			case name == "env" && strings.Contains(a, "=") && !strings.HasPrefix(a, "="):
				i++
			default:
				goto done
			}
		}
	done:
		if (name == "timeout" || name == "gtimeout") && i < len(args) {
			i++ // the duration
		}
		if i >= len(args) {
			return args, sudo // the wrapper runs nothing: it is the command (bare env)
		}
		args = args[i:]
	}
	return args, sudo
}

// flagsAndOperands splits args[1:] into flags (up to "--") and operands.
func flagsAndOperands(args []string) (flags, operands []string) {
	end := false
	for _, a := range args[1:] {
		switch {
		case end:
			operands = append(operands, a)
		case a == "--":
			end = true
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
		default:
			operands = append(operands, a)
		}
	}
	return flags, operands
}

// shortFlag reports whether a single-dash flag cluster in flags holds letter.
func shortFlag(flags []string, letter byte) bool {
	for _, f := range flags {
		if len(f) > 1 && f[0] == '-' && f[1] != '-' && strings.IndexByte(f[1:], letter) >= 0 {
			return true
		}
	}
	return false
}

func hasArg(args []string, want ...string) bool {
	for _, a := range args {
		for _, w := range want {
			if a == w {
				return true
			}
		}
	}
	return false
}
