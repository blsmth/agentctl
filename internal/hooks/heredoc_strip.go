package hooks

import (
	"path"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// dataReceivers is an allowlist of commands known to treat stdin purely as
// data. Anything not listed keeps its heredoc body in the matched text, so
// wrappers (timeout, nice, setsid, ...) and interpreters fail closed.
var dataReceivers = map[string]bool{
	"cat": true, "tee": true, "agentctl": true, "gh": true, "git": true,
	"jq": true, "wc": true, "grep": true, "egrep": true, "fgrep": true,
	"sort": true, "uniq": true, "head": true, "tail": true, "cut": true,
	"tr": true, "tac": true, "nl": true, "fold": true, "fmt": true,
	"column": true, "diff": true, "cmp": true, "od": true, "xxd": true,
	"base64": true, "md5sum": true, "shasum": true, "sha256sum": true,
	"pbcopy": true, "less": true, "more": true, "yq": true,
}

// stripDataHeredocs returns command with the bodies of heredocs removed when
// the receiving command does not execute its stdin. Quoted arguments are not
// touched: they are still matched literally. A statement anywhere inside a
// command or process substitution is never data, since the enclosing command
// consumes its output (`eval "$(cat <<EOF ...)"`, `bash -c "$(cat <<EOF ...)"`).
// It fails closed: on any parse error the raw command is returned unchanged.
func stripDataHeredocs(command string) string {
	if !strings.Contains(command, "<<") {
		return command
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return command
	}
	return stripParsedHeredocs(f, command)
}

// stripParsedHeredocs is stripDataHeredocs on an already parsed command.
func stripParsedHeredocs(f *syntax.File, command string) string {
	// Statements piped into another command may have their output executed
	// (e.g. `cat <<EOF | bash`), so their heredocs are never treated as data.
	piped := map[*syntax.Stmt]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		if b, ok := n.(*syntax.BinaryCmd); ok && (b.Op == syntax.Pipe || b.Op == syntax.PipeAll) {
			piped[b.X] = true
		}
		return true
	})

	// Substitutions run their statements for the enclosing command, which
	// may execute the output, so nothing nested in one is stripped.
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

	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	out := command
	for _, s := range spans {
		out = out[:s.start] + out[s.end:]
	}
	return out
}

// span is a byte range [start, end) of a command string.
type span struct{ start, end uint }

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

// substitutionBodies returns the text inside every command and process
// substitution in f (without the `$(`, backtick, `<(` or `>(` and the closing
// delimiter). Nested substitutions are returned on their own as well.
func substitutionBodies(f *syntax.File, command string) []string {
	var out []string
	add := func(start, end uint) {
		if start <= end && int(end) <= len(command) {
			out = append(out, command[start:end])
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch c := n.(type) {
		case *syntax.CmdSubst:
			start := c.Left.Offset() + 1 // backtick or $
			if !c.Backquotes {
				start++ // the ( of $(
			}
			add(start, c.Right.Offset())
		case *syntax.ProcSubst:
			add(c.OpPos.Offset()+2, c.Rparen.Offset())
		}
		return true
	})
	return out
}

// invocationText is the view of command that the dispatch and cron gates
// scan for agentctl invocations: data heredoc bodies stripped
// (stripParsedHeredocs), plus the body of every command or process
// substitution appended as lines of its own. The scanners tokenize a quoted
// word whole, so without the appended bodies `eval "$(cat <<EOF ...)"` would
// hide a dispatch inside one quoted word. It fails closed: on a parse error
// the raw command is returned.
func invocationText(command string) string {
	if !strings.Contains(command, "<<") && !strings.ContainsAny(command, "$`") &&
		!strings.Contains(command, "<(") && !strings.Contains(command, ">(") {
		return command
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return command
	}
	var b strings.Builder
	b.WriteString(stripParsedHeredocs(f, command))
	for _, body := range substitutionBodies(f, command) {
		b.WriteString("\n")
		b.WriteString(body)
	}
	return b.String()
}

// isDataReceiver reports whether st is a plain command whose name is a known
// literal on the dataReceivers allowlist.
func isDataReceiver(st *syntax.Stmt) bool {
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	name := call.Args[0].Lit()
	if name == "" {
		return false
	}
	return dataReceivers[path.Base(name)]
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
