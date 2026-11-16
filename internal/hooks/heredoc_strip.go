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
// touched: they are still matched literally. It fails closed: on any parse
// error the raw command is returned unchanged.
func stripDataHeredocs(command string) string {
	if !strings.Contains(command, "<<") {
		return command
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return command
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

	type span struct{ start, end uint }
	var spans []span
	syntax.Walk(f, func(n syntax.Node) bool {
		st, ok := n.(*syntax.Stmt)
		if !ok || piped[st] || !isDataReceiver(st) || hasSubstitution(st) {
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
