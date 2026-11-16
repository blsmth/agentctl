package hooks

import (
	"path"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// stdinExecutingCommands are commands that execute (or forward for execution)
// what they read on stdin or that wrap other commands, so a heredoc body fed
// to them is code rather than data and must still be matched.
var stdinExecutingCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"fish": true, "csh": true, "tcsh": true, "ash": true,
	"eval": true, "source": true, ".": true, "exec": true,
	"ssh": true, "python": true, "python3": true, "perl": true,
	"ruby": true, "node": true, "deno": true, "bun": true, "php": true,
	"lua": true, "xargs": true, "env": true, "sudo": true, "doas": true,
	"su": true, "nohup": true, "time": true, "command": true,
	"builtin": true, "watch": true, "tmux": true, "docker": true,
	"kubectl": true, "osascript": true, "sqlite3": true, "psql": true,
	"mysql": true, "gdb": true, "expect": true,
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
		if !ok || piped[st] || !isDataReceiver(st) {
			return true
		}
		for _, r := range st.Redirs {
			if (r.Op != syntax.Hdoc && r.Op != syntax.DashHdoc) || r.Hdoc == nil {
				continue
			}
			if hasSubstitution(r.Hdoc) {
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
// literal that does not execute its stdin.
func isDataReceiver(st *syntax.Stmt) bool {
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	name := call.Args[0].Lit()
	if name == "" {
		return false
	}
	return !stdinExecutingCommands[path.Base(name)]
}

// hasSubstitution reports whether the heredoc body contains command or
// process substitution, which the outer shell would execute.
func hasSubstitution(w *syntax.Word) bool {
	found := false
	syntax.Walk(w, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			found = true
		}
		return !found
	})
	return found
}
