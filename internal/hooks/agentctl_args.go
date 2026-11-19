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
// since the shell runs that part. This is a scanner for the tamper rules, not
// a shell parser, so anything it does not model -- a comment (a word that
// starts with #), ANSI-C $'...' quoting, ${...}, an unterminated quote --
// returns command unchanged (fail closed: the rules see every word).
func maskAgentctlArgs(command string) string {
	m := argMasker{s: command, cmdStart: true}
	m.scan()
	if m.unsure || len(m.masked) == 0 {
		return command
	}
	var b strings.Builder
	last := 0
	for _, sp := range m.masked {
		b.WriteString(command[last:sp[0]])
		b.WriteByte('_')
		last = sp[1]
	}
	b.WriteString(command[last:])
	return b.String()
}

type argMasker struct {
	s string

	// Per simple command.
	cmdStart  bool     // the next word is the command name
	agentctl  bool     // the command name is agentctl
	subst     bool     // the command contains a command substitution
	redirNext bool     // the next word is a redirection target
	pending   [][2]int // argument word spans to mask if the command qualifies

	masked [][2]int
	unsure bool // the command uses shell syntax the scanner does not model
}

// endCommand closes the current simple command.
func (m *argMasker) endCommand() {
	if m.agentctl && !m.subst {
		m.masked = append(m.masked, m.pending...)
	}
	m.pending = nil
	m.cmdStart, m.agentctl, m.subst, m.redirNext = true, false, false, false
}

// word handles the word s[start:end]; text is its value with quotes removed.
func (m *argMasker) word(start, end int, text string) {
	switch {
	case m.redirNext:
		m.redirNext = false
	case m.cmdStart:
		if isAssignment(m.s[start:end]) {
			return // FOO=bar agentctl ...: still before the command name
		}
		m.cmdStart = false
		m.agentctl = filepath.Base(text) == "agentctl"
	default:
		m.pending = append(m.pending, [2]int{start, end})
	}
}

func (m *argMasker) scan() {
	s := m.s
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '\n' || c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			m.endCommand()
			i++
		case c == '<' || c == '>':
			if i+1 < len(s) && s[i+1] == '(' {
				m.subst = true // process substitution
			}
			for i < len(s) && strings.IndexByte("<>&|", s[i]) >= 0 {
				i++
			}
			m.redirNext = true
		case c == '#':
			m.unsure = true // a comment: its end is not modeled
			return
		default:
			start := i
			var text strings.Builder
			i = m.scanWord(i, &text)
			m.word(start, i, text.String())
		}
	}
	m.endCommand()
}

// scanWord reads one shell word starting at i and returns the index after
// it, writing the unquoted value to text.
func (m *argMasker) scanWord(i int, text *strings.Builder) int {
	s := m.s
	for i < len(s) {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', ';', '&', '|', '(', ')', '<', '>':
			return i
		case '\\':
			if i+1 < len(s) {
				text.WriteByte(s[i+1])
				i += 2
				continue
			}
		case '\'':
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
					m.subst = true
				}
				text.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				m.unsure = true
			}
			i++ // closing quote
			continue
		case '`':
			m.subst = true
		case '$':
			if i+1 < len(s) {
				switch s[i+1] {
				case '(':
					m.subst = true
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
