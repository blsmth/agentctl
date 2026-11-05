// Package shellquote is the single place agentctl turns arbitrary strings
// into POSIX shell words. Every command string agentctl builds for a shell
// to parse later (tmux launch scripts, display-popup commands, notify_cmd
// templates, $CLAUDE_ENV_FILE exports) must route interpolated values
// through Quote.
//
// It is deliberately a dependency-free leaf package. The two well-known
// libraries were considered: al.essio.dev/pkg/shellescape implements the
// exact same single-quote idiom as Quote below (so it would add a module for
// three lines), and mvdan.cc/sh/v3/syntax.Quote refuses to quote newlines in
// LangPOSIX mode and emits bash-only $'...' strings in every other mode --
// but agentctl routinely quotes multi-line prompts into scripts run by
// /bin/sh. mvdan.cc/sh/v3/shell is used in tests to verify that quoted output
// tokenizes back to the intended argv.
package shellquote

import "strings"

// Quote returns s as a single POSIX shell word that expands to exactly s.
//
// The result is always wrapped in single quotes -- even for strings that
// would be safe bare -- and every embedded single quote is written as a
// closing quote, a backslash-escaped quote, and a reopening quote. Single quotes
// disable every form of expansion (parameter, command, arithmetic, globbing,
// tilde, word splitting), and newlines are preserved literally, so the
// result is safe in sh, bash, zsh, and tmux's own command parser alike.
//
// s must not contain a NUL byte; no shell can represent one in an argument.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes every element of args with Quote and joins them with single
// spaces, producing a command line a shell will split back into exactly
// args.
func Join(args ...string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = Quote(a)
	}
	return strings.Join(quoted, " ")
}
