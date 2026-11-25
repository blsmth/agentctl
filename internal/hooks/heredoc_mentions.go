package hooks

import (
	"path"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// A data heredoc written to a file keeps its body in the matched text when
// the file is named again elsewhere in the command, since that command may
// run it (`cat > s.sh <<EOF ... EOF; bash s.sh`). Some commands only read
// the file or edit it in place, and naming the file there runs nothing:
// `cat >> x_test.go <<EOF ... EOF; sed -i '' 's/a/b/' x_test.go; gofmt -l
// x_test.go`. inertMention reports those, so targetNamedElsewhere can skip
// them. Anything not understood here still counts (fail closed).

// inertScopes returns the statements of f that inertMention may consider:
// top-level statements and the operands of top-level && / || chains. A
// statement nested in a block, subshell, if, loop, case or function body
// inherits that compound command's pipe or redirect (`{ cat s.sh; } |
// bash`), so it never counts as inert. It returns nil, so nothing is
// inert, when the line can change what a plain command name or its stdout
// means: a function definition (`cat() { bash; }`), an exec (`exec >
// t.sh` redirects every later command), alias, shopt, enable, hash, or a
// PATH assignment.
func inertScopes(f *syntax.File, command string) map[*syntax.Stmt]bool {
	if strings.Contains(command, "PATH=") {
		return nil
	}
	rebinds := false
	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.FuncDecl:
			rebinds = true
		case *syntax.CallExpr:
			if len(x.Args) > 0 {
				switch x.Args[0].Lit() {
				case "exec", "alias", "shopt", "enable", "hash", "builtin", "command":
					rebinds = true
				}
			}
		}
		return !rebinds
	})
	if rebinds {
		return nil
	}
	top := map[*syntax.Stmt]bool{}
	var add func(st *syntax.Stmt)
	add = func(st *syntax.Stmt) {
		top[st] = true
		if b, ok := st.Cmd.(*syntax.BinaryCmd); ok && (b.Op == syntax.AndStmt || b.Op == syntax.OrStmt) {
			add(b.X)
			add(b.Y)
		}
	}
	for _, st := range f.Stmts {
		add(st)
	}
	return top
}

// inertFileTools read the files they are given, or rewrite them in place,
// and never execute them, copy them elsewhere or run a helper program on
// them. sort (--compress-program, -o), uniq and xxd (an output operand), rg
// (--pre) and less (LESSOPEN) are left out for that reason.
var inertFileTools = map[string]bool{
	"cat": true, "head": true, "tail": true, "wc": true, "grep": true, "egrep": true, "fgrep": true,
	"diff": true, "cmp": true, "nl": true, "od": true, "cut": true, "md5sum": true, "shasum": true,
	"sha256sum": true, "gofmt": true, "ls": true, "stat": true,
}

// inertMention reports whether st only reads or rewrites in place the
// files it names: a plain command (no assignments, not piped, no
// substitution, no output redirect other than /dev/null or a duplicated
// stdout/stderr) that is an inertFileTools command, a sed whose script
// only substitutes, prints, deletes or quits (inertSedArgs), or a python
// -c whose code has no exec-like call (inertPythonCode).
func inertMention(st *syntax.Stmt, piped, inSubst func(*syntax.Stmt) bool) bool {
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) > 0 || st.Background || st.Coprocess ||
		piped(st) || inSubst(st) || hasSubstitution(st) {
		return false
	}
	for _, r := range st.Redirs {
		switch r.Op {
		case syntax.DplOut:
			if l := r.Word.Lit(); l == "1" || l == "2" {
				continue
			}
			return false
		case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob, syntax.AppClob:
			if r.Word.Lit() == "/dev/null" {
				continue
			}
			return false
		case syntax.RdrIn:
			continue
		default:
			return false
		}
	}
	name := call.Args[0].Lit()
	if name == "" {
		return false
	}
	words := make([]string, 0, len(call.Args)-1)
	for _, w := range call.Args[1:] {
		words = append(words, wordText(w))
	}
	switch path.Base(name) {
	case "sed":
		return inertSedArgs(words)
	case "python", "python3":
		return len(words) >= 2 && words[0] == "-c" && inertPythonCode(words[1])
	}
	return inertFileTools[path.Base(name)]
}

// execLikePython matches Python code that could run another file or
// program, or copy one: exec, eval, imports, os/subprocess/shutil and any
// dunder access.
var execLikePython = regexp.MustCompile(
	`\b(?:exec|eval|compile|import|execfile|runpy|system|popen|spawn\w*|subprocess|shutil|os|sys|pty|ctypes|getattr|globals|locals|vars|breakpoint|input)\b|__`)

// inertPythonCode reports whether a python -c program only reads and
// rewrites files with open(): no execLikePython token.
func inertPythonCode(code string) bool {
	return !execLikePython.MatchString(code)
}

// inertSedArgs reports whether sed with these arguments runs only inert
// scripts (inertSedScript). Options other than -n, -E, -r, -s, -u, -z, -i
// and -e are not understood (-f reads a script file). BSD sed takes the
// word after -i as the backup suffix and GNU sed does not, so a non-empty
// word after a bare -i must itself be an inert script as well.
func inertSedArgs(args []string) bool {
	var scripts []string
	sawE := false
	var operands []string
	for j := 0; j < len(args); j++ {
		a := args[j]
		switch {
		case a == "-e" || a == "--expression":
			if j+1 >= len(args) {
				return false
			}
			j++
			scripts, sawE = append(scripts, args[j]), true
		case strings.HasPrefix(a, "--expression="):
			scripts, sawE = append(scripts, strings.TrimPrefix(a, "--expression=")), true
		case a == "-i":
			if j+1 < len(args) && !strings.HasPrefix(args[j+1], "-") {
				j++
				if args[j] != "" && !strings.HasPrefix(args[j], ".") {
					scripts = append(scripts, args[j]) // GNU: the script
				}
			}
		case strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "--in-place"):
			// -i.bak, --in-place[=suffix]
		case a == "-n" || a == "-E" || a == "-r" || a == "-s" || a == "-u" || a == "-z" ||
			a == "--quiet" || a == "--silent" || a == "--regexp-extended" || a == "--posix":
		case strings.HasPrefix(a, "-") && a != "-":
			return false
		default:
			operands = append(operands, a)
		}
	}
	if !sawE {
		if len(operands) == 0 {
			return false
		}
		scripts = append(scripts, operands[0])
	}
	for _, s := range scripts {
		if !inertSedScript(s) {
			return false
		}
	}
	return true
}

// inertSedScript reports whether a sed script consists only of s///
// substitutions (flags g, p, i, I and a number), p, d, q, Q and =, each
// with optional line-number, $ or /regex/ addresses and !. Everything else,
// notably GNU's e command and s///e flag and the w, W and r file commands,
// is not inert.
func inertSedScript(s string) bool {
	i := 0
	for {
		for i < len(s) && strings.ContainsRune(" \t\n;", rune(s[i])) {
			i++
		}
		if i >= len(s) {
			return true
		}
		var ok bool
		if i, ok = sedAddresses(s, i); !ok || i >= len(s) {
			return false
		}
		switch s[i] {
		case 'p', 'd', 'q', 'Q', '=':
			i++
		case 's':
			if i, ok = sedSubst(s, i+1); !ok {
				return false
			}
		default:
			return false
		}
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i < len(s) && s[i] != ';' && s[i] != '\n' {
			return false
		}
	}
}

// sedAddresses skips up to two addresses (N, $ or /re/, comma separated)
// and a !, returning the index of the command.
func sedAddresses(s string, i int) (int, bool) {
	next, ok := sedAddress(s, i)
	if !ok {
		return 0, false
	}
	if next > i && next < len(s) && s[next] == ',' {
		second, ok := sedAddress(s, next+1)
		if !ok || second == next+1 {
			return 0, false // a comma with no second address
		}
		next = second
	}
	i = next
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '!') {
		i++
	}
	return i, true
}

// sedAddress skips one address (N, $ or /re/) at i, if there is one.
func sedAddress(s string, i int) (int, bool) {
	switch {
	case i >= len(s):
	case s[i] >= '0' && s[i] <= '9':
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	case s[i] == '$':
		i++
	case s[i] == '/':
		end, ok := sedDelimited(s, i+1, '/')
		if !ok {
			return 0, false
		}
		i = end + 1
	}
	return i, true
}

// sedSubst parses the rest of an s command after the s: delimiter,
// pattern, replacement and inert flags. It returns the index after it.
func sedSubst(s string, i int) (int, bool) {
	if i >= len(s) || s[i] == '\\' || s[i] == '\n' {
		return 0, false
	}
	d := s[i]
	end, ok := sedDelimited(s, i+1, d)
	if !ok {
		return 0, false
	}
	if end, ok = sedDelimited(s, end+1, d); !ok {
		return 0, false
	}
	i = end + 1
	for i < len(s) && strings.ContainsRune("gpiI0123456789", rune(s[i])) {
		i++
	}
	return i, true
}

// sedDelimited returns the index of the first unescaped d at or after i.
func sedDelimited(s string, i int, d byte) (int, bool) {
	for ; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case d:
			return i, true
		}
	}
	return 0, false
}
