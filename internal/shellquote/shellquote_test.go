package shellquote

import (
	"os/exec"
	"slices"
	"testing"

	"mvdan.cc/sh/v3/shell"
)

// hostile is shared by the tests below: every value here would break, split,
// or execute something if interpolated into a shell command unquoted.
var hostile = []string{
	"",
	"simple",
	"with spaces",
	"  leading and trailing  ",
	"tab\there",
	"line1\nline2",
	"\n",
	"it's",
	"it's Alice's thing",
	`say "hello"`,
	"$HOME",
	"${HOME}",
	"$(touch /tmp/pwned)",
	"`touch /tmp/pwned`",
	"x; rm -rf ~",
	"a && b || c",
	"a | b > c < d",
	"#comment",
	"~/tilde",
	"*glob?[ab]",
	"back\\slash",
	"{a,b}",
	"x=y",
	"tick`back`$dollar\"quote\"'single'",
	"émoji ✓ unicode",
}

func TestQuote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple word", "hello", "'hello'"},
		{"empty", "", "''"},
		{"spaces", "a b", "'a b'"},
		{"dollar", "$HOME/foo", "'$HOME/foo'"},
		{"backtick", "`whoami`", "'`whoami`'"},
		{"double quote", `say "hi"`, `'say "hi"'`},
		{"single quote", "it's", `'it'\''s'`},
		{"multiple single quotes", "a'b'c", `'a'\''b'\''c'`},
		{"newline preserved literally", "line1\nline2", "'line1\nline2'"},
		{"newline with single quote", "it's\na test", "'it'\\''s\na test'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Quote(tt.input); got != tt.want {
				t.Errorf("Quote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestQuoteRoundTripTokenizer asserts every hostile value tokenizes back to
// exactly one field equal to the input under a POSIX shell parser.
func TestQuoteRoundTripTokenizer(t *testing.T) {
	for _, v := range hostile {
		t.Run(v, func(t *testing.T) {
			got, err := shell.Fields(Quote(v), func(string) string { return "EXPANDED" })
			if err != nil {
				t.Fatalf("shell.Fields(%q): %v", Quote(v), err)
			}
			if want := []string{v}; !slices.Equal(got, want) {
				t.Errorf("round trip = %q, want %q", got, want)
			}
		})
	}
}

func TestJoinRoundTripTokenizer(t *testing.T) {
	got, err := shell.Fields(Join(hostile...), func(string) string { return "EXPANDED" })
	if err != nil {
		t.Fatalf("shell.Fields: %v", err)
	}
	if !slices.Equal(got, hostile) {
		t.Errorf("Join round trip = %q, want %q", got, hostile)
	}
}

// TestQuoteRoundTripRealShells runs each value through real shells as a
// second opinion to the tokenizer.
func TestQuoteRoundTripRealShells(t *testing.T) {
	for _, sh := range []string{"sh", "bash", "zsh"} {
		t.Run(sh, func(t *testing.T) {
			if _, err := exec.LookPath(sh); err != nil {
				t.Skipf("%s not on PATH", sh)
			}
			for _, v := range hostile {
				out, err := exec.Command(sh, "-c", "printf '%s' "+Quote(v)).Output()
				if err != nil {
					t.Fatalf("%s: value %q: %v", sh, v, err)
				}
				if string(out) != v {
					t.Errorf("%s: round trip = %q, want %q", sh, out, v)
				}
			}
		})
	}
}
