package hooks

import (
	"path/filepath"
	"strings"
)

// A search tool's pattern argument is data: grep and rg match it against
// file contents and never open, run or write it. The tamper rules mask it
// like an agentctl argument (shellScript.masked, writeViews), so `grep -n
// "state/agentctl/serve" docs/configuration.md` is a search of a repo doc,
// not a read of the serve state dir. Path operands stay visible: `grep x
// ~/.local/state/agentctl/serve/devices.json` is still denied, and so is a
// pattern file (-f).
//
// Masking leans toward keeping words visible. Only a pattern the shell
// passes through as one unexpanded word is masked (fullyQuotedLiteral), and
// only when every argument of the command is understood: an expansion that
// could turn into -e or -f, or a long option not in the tables below
// (getopt also accepts abbreviations such as --fi for --file), would move
// the pattern, and then nothing is masked.

// searchTool describes how a search command takes its pattern.
type searchTool struct {
	// shortArgs are the single-letter options that take a value.
	shortArgs string
	// longArgs are the long options that take a value; longFlags those that
	// take none. Any other long option masks nothing.
	longArgs, longFlags map[string]bool
}

// searchTools are the commands whose pattern argument is data. rg --pre
// runs a program, but on the files, never on the pattern.
var searchTools = map[string]searchTool{
	"grep":  grepTool,
	"egrep": grepTool,
	"fgrep": grepTool,
	"rg": {
		shortArgs: "efgtTABCmMjrEd",
		longArgs: setOf("regexp", "file", "glob", "iglob", "type", "type-not", "type-add", "type-clear",
			"after-context", "before-context", "context", "max-count", "max-columns", "threads",
			"replace", "encoding", "max-depth", "max-filesize", "sort", "sortr", "pre", "pre-glob",
			"colors", "color", "context-separator", "field-context-separator", "field-match-separator",
			"path-separator", "ignore-file", "engine"),
		longFlags: setOf("hidden", "no-ignore", "no-ignore-vcs", "files", "files-with-matches",
			"files-without-match", "count", "count-matches", "fixed-strings", "ignore-case", "smart-case",
			"case-sensitive", "line-number", "no-line-number", "no-filename", "with-filename", "heading",
			"no-heading", "json", "vimgrep", "multiline", "multiline-dotall", "pcre2", "follow",
			"word-regexp", "line-regexp", "invert-match", "only-matching", "quiet", "column", "no-messages",
			"null", "text", "binary", "search-zip", "sort-files", "stats", "trim", "unrestricted",
			"pretty", "passthru", "byte-offset", "max-columns-preview", "crlf", "no-config", "help",
			"version"),
	},
}

// grepTool covers GNU and BSD grep. --context is in neither table: BSD
// grep's argument to it is optional, so a following word may be the
// pattern.
var grepTool = searchTool{
	shortArgs: "efABCmdD",
	longArgs: setOf("regexp", "file", "max-count", "label", "after-context", "before-context",
		"include", "exclude", "exclude-from", "exclude-dir", "binary-files", "devices", "directories",
		"group-separator"),
	longFlags: setOf("line-number", "recursive", "dereference-recursive", "ignore-case", "no-ignore-case",
		"files-with-matches", "files-without-match", "count", "only-matching", "quiet", "silent",
		"no-messages", "invert-match", "word-regexp", "line-regexp", "with-filename", "no-filename",
		"extended-regexp", "fixed-strings", "basic-regexp", "perl-regexp", "null", "null-data", "text",
		"color", "colour", "byte-offset", "initial-tab", "line-buffered", "no-group-separator", "help",
		"version"),
}

// searchOptionEnvVars put options in front of a search tool's argv: BSD
// grep (macOS) honours GREP_OPTIONS, and an rg config file can carry -e or
// --regexp. With either, `GREP_OPTIONS='-e x' grep '<path>'` reads <path>.
var searchOptionEnvVars = []string{"GREP_OPTIONS", "RIPGREP_CONFIG_PATH"}

// searchOptionEnv reports whether command names one of searchOptionEnvVars
// anywhere (a prefix assignment, export, declare, env ...), in which case
// no search pattern on the line is masked.
func searchOptionEnv(command string) bool {
	for _, v := range searchOptionEnvVars {
		if strings.Contains(command, v) {
			return true
		}
	}
	return false
}

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// searchPatternArgs returns the indices into c.words of c's pattern words
// that are safe to mask (see above): -e/--regexp values, and the first
// operand when no -e or -f gives the patterns. It returns nil when c is not
// a search tool, has a substitution, has a prefix assignment (which may set
// an option variable, see searchOptionEnvVars), or has an argument it
// cannot place.
func (c *shCommand) searchPatternArgs() []int {
	ni := c.nameIndex()
	if ni != 0 || c.subst || !plainLiteral(c.words[ni].raw) {
		return nil
	}
	tool, ok := searchTools[filepath.Base(c.words[ni].text)]
	if !ok {
		return nil
	}
	var patterns, operands []int
	fromOpts, endOpts := false, false
	args := c.words[ni+1:]
	for j := 0; j < len(args); j++ {
		w := args[j]
		if !fixedWord(w.raw) {
			return nil
		}
		t := w.text
		// valueIdx is the index of a separate value word, -1 for none.
		valueIdx, isPattern := -1, false
		switch {
		case endOpts || !strings.HasPrefix(t, "-") || t == "-":
			operands = append(operands, ni+1+j)
			continue
		case t == "--":
			endOpts = true
			continue
		case strings.HasPrefix(t, "--"):
			name, _, hasValue := strings.Cut(t[2:], "=")
			switch {
			case tool.longArgs[name]:
			case tool.longFlags[name]:
				continue
			default:
				return nil // unknown, or an abbreviation getopt would expand
			}
			if name == "regexp" || name == "file" {
				fromOpts = true
			}
			if !hasValue {
				valueIdx, isPattern = j+1, name == "regexp"
			}
		default:
			// A short option cluster: the first letter that takes a value
			// takes the rest of the word, or else the next word.
			for k := 1; k < len(t); k++ {
				if !strings.ContainsRune(tool.shortArgs, rune(t[k])) {
					continue
				}
				if t[k] == 'e' || t[k] == 'f' {
					fromOpts = true
				}
				if k == len(t)-1 {
					valueIdx, isPattern = j+1, t[k] == 'e'
				}
				break
			}
		}
		if valueIdx < 0 {
			continue
		}
		if valueIdx >= len(args) || !fixedWord(args[valueIdx].raw) {
			return nil
		}
		if isPattern && fullyQuotedLiteral(args[valueIdx].raw) {
			patterns = append(patterns, ni+1+valueIdx)
		}
		j = valueIdx
	}
	if !fromOpts && len(operands) > 0 && fullyQuotedLiteral(c.words[operands[0]].raw) {
		patterns = append(patterns, operands[0])
	}
	return patterns
}

// fixedWord reports whether the shell cannot turn raw into a different
// number of words or into an option: no $ or backtick (a substitution or
// unquoted variable may split into `-f x`), and no leading glob or brace.
// $ inside single quotes is literal, so a fully quoted literal counts.
func fixedWord(raw string) bool {
	if fullyQuotedLiteral(raw) {
		return true
	}
	return raw != "" && !strings.ContainsAny(raw, "$`") && !strings.ContainsAny(raw[:1], "*?[{")
}

// plainLiteral reports whether raw is a word the shell passes through as
// written: no quotes, escapes, expansions or glob characters.
func plainLiteral(raw string) bool {
	return raw != "" && !strings.ContainsAny(raw, "'\"\\$`*?[{~")
}

// fullyQuotedLiteral reports whether raw consists only of single- and
// double-quoted parts, with no $ or backtick inside the double quotes
// (backslash escapes are fine): the shell passes its text through as one
// word, unexpanded.
func fullyQuotedLiteral(raw string) bool {
	if raw == "" {
		return false
	}
	for i := 0; i < len(raw); {
		switch raw[i] {
		case '\'':
			end := strings.IndexByte(raw[i+1:], '\'')
			if end < 0 {
				return false
			}
			i += end + 2
		case '"':
			i++
			for ; i < len(raw) && raw[i] != '"'; i++ {
				switch raw[i] {
				case '\\':
					i++
				case '$', '`':
					return false
				}
			}
			if i >= len(raw) {
				return false
			}
			i++
		default:
			return false
		}
	}
	return true
}
