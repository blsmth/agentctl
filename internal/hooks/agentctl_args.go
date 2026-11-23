package hooks

import (
	"path/filepath"
	"strings"
)

// maskAgentctlArgs returns command with the argument words of every agentctl
// invocation replaced by "_", so the file tamper rules match real shell write
// targets only and not, say, the text of `agentctl context set k '... tee
// schedules.json ...'`. agentctl never hands its arguments to a shell, so
// those words cannot write anything.
//
// Redirections and their targets are kept (`agentctl status > file` still
// writes file). An agentctl command that contains a command substitution
// ($(...), backticks, <(...) or >(...) outside single quotes) is left whole,
// since the shell runs that part. A comment (a word that starts with #,
// outside quotes) runs to the end of its line and is kept as is. This is a
// scanner for the tamper rules, not a shell parser, so anything it does not
// model -- ANSI-C $'...' quoting, ${...}, an unterminated quote -- returns
// command unchanged (fail closed: the rules see every word).
func maskAgentctlArgs(command string) string {
	return scanShell(command).masked()
}

// shellScript is a command line split into simple commands and the control
// operators between them (scanShell).
type shellScript struct {
	src   string
	items []shItem
	// unsure: the line uses shell syntax the scanner does not model.
	unsure bool
}

// shItem is a simple command or a control operator (; && || | |& & ( ),
// with a newline reported as ";").
type shItem struct {
	op  string
	cmd *shCommand
}

// shWord is one shell word: src[start:end], text with quotes removed.
type shWord struct {
	start, end int
	raw        string // src[start:end]
	text       string
}

// shRedir is a redirection operator and its target word.
type shRedir struct {
	op     string
	target shWord
}

// shCommand is one simple command.
type shCommand struct {
	words  []shWord // assignments, command name and arguments, in order
	redirs []shRedir
	// before and after are the control operators around the command (""
	// at either end of the line).
	before, after string
	// subst: the command contains a command or process substitution.
	subst bool
}

// nameIndex returns the index of the command name in c.words, past
// variable assignments, or -1 when the command is only assignments.
func (c *shCommand) nameIndex() int {
	for i, w := range c.words {
		if !isAssignment(w.raw) {
			return i
		}
	}
	return -1
}

// isAgentctl reports whether the command name is agentctl (by base name).
func (c *shCommand) isAgentctl() bool {
	i := c.nameIndex()
	return i >= 0 && filepath.Base(c.words[i].text) == "agentctl"
}

// args returns the words after the command name.
func (c *shCommand) args() []shWord {
	i := c.nameIndex()
	if i < 0 {
		return nil
	}
	return c.words[i+1:]
}

// masked is maskAgentctlArgs over the scanned line.
func (sc shellScript) masked() string {
	if sc.unsure {
		return sc.src
	}
	var b strings.Builder
	last := 0
	for _, it := range sc.items {
		c := it.cmd
		if c == nil || c.subst || !c.isAgentctl() {
			continue
		}
		for _, w := range c.args() {
			b.WriteString(sc.src[last:w.start])
			b.WriteByte('_')
			last = w.end
		}
	}
	if last == 0 {
		return sc.src
	}
	b.WriteString(sc.src[last:])
	return b.String()
}

// agentctlCommands returns every simple command whose name is agentctl,
// with or without a command substitution, including those inside an
// unquoted $(...).
func (sc shellScript) agentctlCommands() []*shCommand {
	var out []*shCommand
	for _, it := range sc.items {
		if it.cmd != nil && it.cmd.isAgentctl() {
			out = append(out, it.cmd)
		}
	}
	return out
}

// scanShell splits command into simple commands and control operators.
func scanShell(command string) shellScript {
	m := shellScanner{s: command}
	m.scan()
	return shellScript{src: command, items: m.items, unsure: m.unsure}
}

// shellScanner is scanShell's state.
type shellScanner struct {
	s string

	cur       shCommand
	redirOp   string // the pending redirection operator
	redirNext bool   // the next word is a redirection target
	lastOp    string

	items  []shItem
	unsure bool
}

// continuationOps continue a command list onto the next line.
var continuationOps = map[string]bool{"|": true, "|&": true, "&&": true, "||": true}

// endCommand closes the current simple command at the operator op ("" at
// the end of the line).
func (m *shellScanner) endCommand(op string) {
	empty := len(m.cur.words) == 0 && len(m.cur.redirs) == 0
	if empty && op == ";" && continuationOps[m.lastOp] {
		return // `a |` newline `b`: still one pipeline
	}
	if !empty {
		c := m.cur
		c.before, c.after = m.lastOp, op
		m.items = append(m.items, shItem{cmd: &c})
	}
	if op != "" {
		m.items = append(m.items, shItem{op: op})
	}
	m.lastOp = op
	m.cur = shCommand{}
	m.redirNext = false
}

// word handles the word s[start:end]; text is its value with quotes removed.
func (m *shellScanner) word(start, end int, text string) {
	w := shWord{start: start, end: end, raw: m.s[start:end], text: text}
	if m.redirNext {
		m.redirNext = false
		m.cur.redirs = append(m.cur.redirs, shRedir{op: m.redirOp, target: w})
		return
	}
	m.cur.words = append(m.cur.words, w)
}

func (m *shellScanner) scan() {
	s := m.s
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '\\' && i+1 < len(s) && s[i+1] == '\n':
			i += 2 // a line continuation between words
		case c == '\n':
			m.endCommand(";")
			i++
		case c == '(' || c == ')':
			m.endCommand(string(c))
			i++
		case c == '&' && i+1 < len(s) && s[i+1] == '>':
			i = m.redirection(i) // &> file
		case c == ';' || c == '&' || c == '|':
			j := i
			for j < len(s) && strings.IndexByte(";&|", s[j]) >= 0 && !(s[j] == '&' && j+1 < len(s) && s[j+1] == '>' && j > i) {
				j++
			}
			m.endCommand(s[i:j])
			i = j
		case c == '<' || c == '>':
			i = m.redirection(i)
		case c == '#':
			// A comment: # at the start of a word, outside quotes (inside
			// a word or quotes scanWord reads it as text). It runs to the
			// end of the line and runs nothing.
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				i = len(s)
			} else {
				i += nl
			}
		default:
			start := i
			var text strings.Builder
			i = m.scanWord(i, &text)
			m.word(start, i, text.String())
		}
	}
	m.endCommand("")
}

// redirection reads the redirection operator at s[i] and returns the index
// after it.
func (m *shellScanner) redirection(i int) int {
	s := m.s
	if (s[i] == '<' || s[i] == '>') && i+1 < len(s) && s[i+1] == '(' {
		m.cur.subst = true // process substitution
	}
	start := i
	for i < len(s) && strings.IndexByte("<>&|", s[i]) >= 0 {
		i++
	}
	m.redirOp, m.redirNext = s[start:i], true
	return i
}

// scanWord reads one shell word starting at i and returns the index after
// it, writing the unquoted value to text.
func (m *shellScanner) scanWord(i int, text *strings.Builder) int {
	s := m.s
	for i < len(s) {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', ';', '&', '|', '(', ')', '<', '>':
			return i
		case '\\':
			if i+1 < len(s) {
				if s[i+1] != '\n' { // backslash-newline is a line continuation
					text.WriteByte(s[i+1])
				}
				i += 2
				continue
			}
		case '\'':
			// Inside single quotes nothing is special: no substitution,
			// no comment, newlines included.
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				m.unsure = true
				return len(s)
			}
			text.WriteString(s[i+1 : i+1+end])
			i += end + 2
			continue
		case '"':
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					text.WriteByte(s[i+1])
					i += 2
					continue
				}
				if s[i] == '`' || (s[i] == '$' && i+1 < len(s) && s[i+1] == '(') {
					m.cur.subst = true
				}
				text.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				// Unterminated: stop at the end, never past it (the
				// caller slices s[start:i]).
				m.unsure = true
				return len(s)
			}
			i++ // closing quote
			continue
		case '`':
			m.cur.subst = true
		case '$':
			if i+1 < len(s) {
				switch s[i+1] {
				case '(':
					m.cur.subst = true
				case '\'', '{':
					m.unsure = true // ANSI-C quoting, parameter expansion
				}
			}
		}
		text.WriteByte(c)
		i++
	}
	return i
}

// isAssignment reports whether a raw word is a shell variable assignment
// (NAME=value) rather than a command name.
func isAssignment(raw string) bool {
	eq := strings.IndexByte(raw, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range raw[:eq] {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
