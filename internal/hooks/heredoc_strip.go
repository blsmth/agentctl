package hooks

import (
	"path"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// dataReceivers is an allowlist of commands known to treat stdin purely as
// data. Anything not listed keeps its heredoc body in the matched text, so
// wrappers (timeout, nice, setsid, ...) and interpreters fail closed. git and
// gh can run stdin through aliases, so they count only with a builtin
// subcommand from dataSubcommands.
var dataReceivers = map[string]bool{
	"cat": true, "tee": true, "agentctl": true, "gh": true, "git": true,
	"jq": true, "wc": true, "grep": true, "egrep": true, "fgrep": true,
	"sort": true, "uniq": true, "head": true, "tail": true, "cut": true,
	"tr": true, "tac": true, "nl": true, "fold": true, "fmt": true,
	"column": true, "diff": true, "cmp": true, "od": true, "xxd": true,
	"base64": true, "md5sum": true, "shasum": true, "sha256sum": true,
	"pbcopy": true, "less": true, "more": true, "yq": true,
}

// dataSubcommands lists, for receivers that can run aliases, the builtin
// subcommands (which an alias cannot shadow) that read stdin as data. The
// subcommand must be the first argument: a global option before it (git -c
// alias.x=..., git -C dir) keeps the body.
var dataSubcommands = map[string]map[string]bool{
	"git": {"commit": true, "tag": true, "notes": true, "hash-object": true, "apply": true, "am": true},
	"gh":  {"pr": true, "issue": true, "release": true, "api": true, "gist": true},
}

// shellNames are interpreters whose -c argument is a command line.
var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true, "fish": true,
}

// span is a byte range [start, end) of a command string.
type span struct{ start, end uint }

func (s span) contains(o span) bool { return s.start <= o.start && o.end <= s.end }

// stripDataHeredocs returns command with the bodies of heredocs removed when
// the receiving command does not execute its stdin. Quoted arguments are not
// touched: they are still matched literally. It fails closed: on any parse
// error the raw command is returned unchanged. See dataHeredocSpans for what
// counts as data.
func stripDataHeredocs(command string) string {
	if !strings.Contains(command, "<<") {
		return command
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return command
	}
	return cutSpans(command, span{0, uint(len(command))}, dataHeredocSpans(f, command))
}

// dataHeredocSpans returns the byte ranges of the heredoc bodies in f that are
// only data. A body is kept (not returned) when:
//   - its statement is piped into another command (`cat <<EOF | bash`);
//   - the receiver is not on the allowlist, has a variable assignment, or is
//     git/gh without a data subcommand (isDataReceiver);
//   - the statement has or sits inside a command or process substitution: the
//     enclosing command consumes its output (`eval "$(cat <<EOF ...)"`);
//   - the command has a process substitution or coproc anywhere, or the
//     statement redirects to another file descriptor or a non-literal target:
//     an earlier statement may have wired that fd to an interpreter
//     (`exec 3> >(bash); cat >&3 <<EOF`);
//   - the statement writes to a file named anywhere else in the command, which
//     may run it (`cat > s.sh <<EOF ... EOF; bash s.sh`).
func dataHeredocSpans(f *syntax.File, command string) []span {
	if hasProcSubstOrCoproc(f) {
		return nil
	}
	// Statements piped into another command may have their output executed
	// (e.g. `cat <<EOF | bash`), so their heredocs are never treated as data.
	piped := map[*syntax.Stmt]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		if b, ok := n.(*syntax.BinaryCmd); ok && (b.Op == syntax.Pipe || b.Op == syntax.PipeAll) {
			piped[b.X] = true
		}
		return true
	})

	substs := substitutionSpans(f)
	inSubst := func(st *syntax.Stmt) bool {
		off := st.Pos().Offset()
		for _, s := range substs {
			if off >= s.start && off < s.end {
				return true
			}
		}
		return false
	}

	var spans []span
	syntax.Walk(f, func(n syntax.Node) bool {
		st, ok := n.(*syntax.Stmt)
		if !ok || piped[st] || !isDataReceiver(st) || hasSubstitution(st) || inSubst(st) {
			return true
		}
		targets, ok := writeTargets(st)
		if !ok || targetNamedElsewhere(f, st, targets) {
			return true
		}
		for _, r := range st.Redirs {
			if (r.Op != syntax.Hdoc && r.Op != syntax.DashHdoc) || r.Hdoc == nil {
				continue
			}
			s, e := r.Hdoc.Pos().Offset(), r.Hdoc.End().Offset()
			if s < e && int(e) <= len(command) {
				spans = append(spans, span{s, e})
			}
		}
		return true
	})
	return spans
}

// cutSpans returns command[region] with every span inside region removed.
// Overlapping or nested spans are merged (the outermost wins).
func cutSpans(command string, region span, spans []span) string {
	var in []span
	for _, s := range spans {
		if region.contains(s) && s != region {
			in = append(in, s)
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].start < in[j].start })
	var b strings.Builder
	pos := region.start
	for _, s := range in {
		if s.start < pos {
			if s.end > pos {
				pos = s.end
			}
			continue
		}
		b.WriteString(command[pos:s.start])
		pos = s.end
	}
	b.WriteString(command[pos:region.end])
	return b.String()
}

// isDataReceiver reports whether st is a plain command, with no variable
// assignments, whose name is a known literal on the dataReceivers allowlist
// (and, for git and gh, whose first argument is a data subcommand).
func isDataReceiver(st *syntax.Stmt) bool {
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) > 0 {
		return false
	}
	name := call.Args[0].Lit()
	if name == "" {
		return false
	}
	name = path.Base(name)
	if !dataReceivers[name] {
		return false
	}
	if subs, gated := dataSubcommands[name]; gated {
		return len(call.Args) > 1 && subs[call.Args[1].Lit()]
	}
	return true
}

// writeTargets returns the files st writes to: the literal targets of its
// output redirections and, for tee, its literal non-flag arguments. ok is
// false when st redirects to another file descriptor (other than 1 and 2, or
// closing one) or to a target that is not a literal, since either may lead to
// an interpreter.
func writeTargets(st *syntax.Stmt) (targets []string, ok bool) {
	for _, r := range st.Redirs {
		switch r.Op {
		case syntax.Hdoc, syntax.DashHdoc:
			continue
		case syntax.DplOut, syntax.DplIn:
			switch r.Word.Lit() {
			case "1", "2", "-":
				continue
			}
			return nil, false
		}
		lit := r.Word.Lit()
		if lit == "" {
			return nil, false
		}
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.AppClob, syntax.RdrAll,
			syntax.RdrAllClob, syntax.AppAll, syntax.AppAllClob, syntax.RdrInOut:
			targets = append(targets, lit)
		}
	}
	if call, isCall := st.Cmd.(*syntax.CallExpr); isCall && path.Base(call.Args[0].Lit()) == "tee" {
		for _, w := range call.Args[1:] {
			lit := w.Lit()
			if lit == "" {
				return nil, false
			}
			if !strings.HasPrefix(lit, "-") {
				targets = append(targets, lit)
			}
		}
	}
	return targets, true
}

// targetNamedElsewhere reports whether any target (or its base name) is a
// field of a word anywhere in f outside st's own words and heredoc bodies.
func targetNamedElsewhere(f *syntax.File, st *syntax.Stmt, targets []string) bool {
	if len(targets) == 0 {
		return false
	}
	own := span{st.Pos().Offset(), st.End().Offset()}
	bodies := map[*syntax.Word]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		if r, ok := n.(*syntax.Redirect); ok && r.Hdoc != nil {
			bodies[r.Hdoc] = true
		}
		return true
	})
	found := false
	syntax.Walk(f, func(n syntax.Node) bool {
		w, ok := n.(*syntax.Word)
		if !ok || found {
			return !found
		}
		if bodies[w] || own.contains(span{w.Pos().Offset(), w.End().Offset()}) {
			return false
		}
		for _, field := range strings.FieldsFunc(wordText(w), isFieldSep) {
			for _, t := range targets {
				if field == t || path.Base(field) == path.Base(t) {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

func isFieldSep(r rune) bool {
	return strings.ContainsRune(" \t\n;&|()<>='\"`$", r)
}

// hasSubstitution reports whether n (a statement: args, redirects and heredoc
// body) contains command or process substitution, which the shell would
// execute or wire to another command (e.g. `cat <<EOF > >(bash)`).
func hasSubstitution(n syntax.Node) bool {
	found := false
	syntax.Walk(n, func(c syntax.Node) bool {
		switch c.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			found = true
		}
		return !found
	})
	return found
}

// hasProcSubstOrCoproc reports whether f has a process substitution or a
// coproc, either of which can leave a file descriptor wired to a command.
func hasProcSubstOrCoproc(f *syntax.File) bool {
	found := false
	syntax.Walk(f, func(c syntax.Node) bool {
		switch c.(type) {
		case *syntax.ProcSubst, *syntax.CoprocClause:
			found = true
		}
		return !found
	})
	return found
}

// substitutionSpans returns the byte range of every command and process
// substitution in f, outermost and nested alike.
func substitutionSpans(f *syntax.File) []span {
	var out []span
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			out = append(out, span{n.Pos().Offset(), n.End().Offset()})
		}
		return true
	})
	return out
}

// wordText returns a word roughly as the command receives it: single-quoted
// parts unwrapped, double-quoted parts without the quotes, expansions as
// written. A command or process substitution becomes the placeholder "$_":
// it still marks the word as expanding, and its body is scanned on its own
// (nestedCommandLines) or in the surrounding text, so it is not counted
// twice.
func wordText(w *syntax.Word) string {
	var b strings.Builder
	for _, p := range w.Parts {
		switch q := p.(type) {
		case *syntax.SglQuoted:
			b.WriteString(q.Value)
		case *syntax.DblQuoted:
			for _, dp := range q.Parts {
				writePart(&b, dp)
			}
		default:
			writePart(&b, p)
		}
	}
	return b.String()
}

func writePart(b *strings.Builder, p syntax.WordPart) {
	switch q := p.(type) {
	case *syntax.Lit:
		b.WriteString(q.Value)
	case *syntax.CmdSubst, *syntax.ProcSubst:
		b.WriteString("$_")
	default:
		b.WriteString(nodeText(p))
	}
}

// nodeText prints n back as shell source.
func nodeText(n syntax.Node) string {
	var b strings.Builder
	_ = syntax.NewPrinter().Print(&b, n)
	return b.String()
}

// nestedCommandLines returns the command lines in f that the scanners would
// otherwise miss because they sit inside one quoted word:
//   - the body of every command substitution that is a direct part of a
//     double-quoted word (`eval "$(cat <<EOF ...)"`); unquoted ones are
//     already seen word by word in the surrounding text;
//   - the -c argument of a shell (`bash -c 'agentctl dispatch ...'`), also
//     behind wrappers such as env or xargs;
//   - the arguments of eval;
//   - a here-string fed to a shell (`bash <<< '...'`).
func nestedCommandLines(f *syntax.File, command string) []string {
	var out []string
	syntax.Walk(f, func(n syntax.Node) bool {
		switch c := n.(type) {
		case *syntax.DblQuoted:
			for _, p := range c.Parts {
				if cs, ok := p.(*syntax.CmdSubst); ok {
					if body, ok := cmdSubstBody(cs, command); ok {
						out = append(out, body)
					}
				}
			}
		case *syntax.CallExpr:
			out = append(out, callCommandLines(c)...)
		case *syntax.Stmt:
			if call, ok := c.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 && shellNames[path.Base(call.Args[0].Lit())] {
				for _, r := range c.Redirs {
					if r.Op == syntax.WordHdoc && r.Word != nil {
						out = append(out, wordText(r.Word))
					}
				}
			}
		}
		return true
	})
	return out
}

// cmdSubstBody returns the source between a command substitution's
// delimiters.
func cmdSubstBody(cs *syntax.CmdSubst, command string) (string, bool) {
	start := cs.Left.Offset() + 1 // backtick or $
	if !cs.Backquotes {
		start++ // the ( of $(
	}
	end := cs.Right.Offset()
	if start > end || int(end) > len(command) {
		return "", false
	}
	return command[start:end], true
}

// callCommandLines returns the -c arguments of any shell word in call and,
// for eval, its arguments joined.
func callCommandLines(call *syntax.CallExpr) []string {
	var out []string
	if len(call.Args) > 1 && call.Args[0].Lit() == "eval" {
		parts := make([]string, 0, len(call.Args)-1)
		for _, w := range call.Args[1:] {
			parts = append(parts, wordText(w))
		}
		out = append(out, strings.Join(parts, " "))
	}
	for i, w := range call.Args {
		if !shellNames[path.Base(w.Lit())] {
			continue
		}
		for j := i + 1; j+1 < len(call.Args); j++ {
			flag := call.Args[j].Lit()
			if !strings.HasPrefix(flag, "-") || strings.HasPrefix(flag, "--") {
				break
			}
			if strings.Contains(flag, "c") {
				out = append(out, wordText(call.Args[j+1]))
				break
			}
		}
	}
	return out
}

// invocationText is the view of command that the dispatch and cron gates
// scan for agentctl invocations: data heredoc bodies stripped
// (dataHeredocSpans), plus nestedCommandLines appended as lines of their own,
// since the scanners tokenize a quoted word whole. It fails closed: on a
// parse error the raw command is returned.
func invocationText(command string) string {
	if !strings.ContainsAny(command, "<$`'\"") && !strings.Contains(command, "eval") {
		return command
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return command
	}
	var b strings.Builder
	b.WriteString(cutSpans(command, span{0, uint(len(command))}, dataHeredocSpans(f, command)))
	for _, line := range nestedCommandLines(f, command) {
		b.WriteString("\n")
		b.WriteString(line)
	}
	return b.String()
}
