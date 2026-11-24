package auditnarrate

import (
	"os"
	"regexp"
	"strings"
)

// Evidence snippets are command lines and tool inputs, so they can carry
// secrets typed into a command. They are redacted before anything leaves the
// machine.
const redacted = "[REDACTED]"

var redactors = []struct {
	re   *regexp.Regexp
	repl string
}{
	// URL userinfo: https://user:pass@host.
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@:]+:[^\s/@]+@`), "${1}" + redacted + "@"},
	// Authorization headers and bearer tokens.
	{regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(bearer\s+|basic\s+|token\s+)?[^\s"']+`), "${1}" + redacted},
	{regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}" + redacted},
	// Well-known key shapes.
	{regexp.MustCompile(`\bsk[-_][A-Za-z0-9_-]{8,}`), redacted},
	{regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{8,}`), redacted},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{8,}`), redacted},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{8,}`), redacted},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{12,}`), redacted},
	// KEY=value (also --flag=value, "key": "value") where the key looks secret.
	// An unquoted value may contain backslash-escaped characters (a\ b).
	{regexp.MustCompile(`(?i)(\b[\w.-]*(?:token|secret|pass|key|auth|credential)[\w.-]*["']?\s*[=:]\s*)("[^"]*"|'[^']*'|(?:\\.|[^\s"';&|\\])+)`), "${1}" + redacted},
	// Secret flags with a separate value: --password hunter2.
	{regexp.MustCompile(`(?i)(--?[\w-]*(?:token|secret|pass|api-?key)[\w-]*\s+)("[^"]*"|'[^']*'|[^\s"';&|-](?:\\.|[^\s"';&|\\])*)`), "${1}" + redacted},
	// Database and ssh clients: -phunter2, -p hunter2 (only on these commands,
	// since -p is a harmless flag elsewhere, e.g. mkdir -p).
	{regexp.MustCompile(`(\b(?:mysql\w*|mariadb\w*|sshpass)\b[^|;&\n]*?\s-p\s*)("[^"]*"|'[^']*'|[^\s"';&|-][^\s"';&|]*)`), "${1}" + redacted},
	// Basic-auth credentials: curl -u bob:pw, --user bob:pw, --proxy-user.
	{regexp.MustCompile(`((?:\s-u\s*|--user[=\s]\s*|--proxy-user[=\s]\s*)["']?[^\s:"']+:)[^\s"']+`), "${1}" + redacted},
}

// homeDir is a seam for tests.
var homeDir = os.UserHomeDir

// Redact masks secrets in s and shortens the home directory to ~.
func Redact(s string) string {
	for _, r := range redactors {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	if h, err := homeDir(); err == nil && len(h) > 1 {
		h = strings.TrimRight(h, "/")
		s = replacePathPrefix(s, h)
	}
	return s
}

// replacePathPrefix turns h into ~ where it starts a path (not inside a
// longer directory name like /Users/bobby when h is /Users/bob).
func replacePathPrefix(s, h string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(h) + `(/|$|[^\w.-])`)
	return re.ReplaceAllString(s, "~${1}")
}
