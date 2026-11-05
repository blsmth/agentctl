package hooks

import (
	"strings"
	"testing"
)

// fakeProbe builds an injectable probe function from a map of path ->
// (major, ok), so resolveBash's selection logic can be tested without
// depending on which bash binaries happen to be installed on the machine
// running the test.
func fakeProbe(versions map[string]int, missing map[string]bool) func(string) (int, bool) {
	return func(path string) (int, bool) {
		if missing[path] {
			return 0, false
		}
		major, ok := versions[path]
		return major, ok
	}
}

func TestResolveBash(t *testing.T) {
	tests := []struct {
		name       string
		candidates []string
		versions   map[string]int
		missing    map[string]bool
		want       string
	}{
		{
			name:       "picks first candidate with major >= 4",
			candidates: []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash"},
			versions:   map[string]int{"/opt/homebrew/bin/bash": 5, "/usr/local/bin/bash": 5, "/bin/bash": 3},
			want:       "/opt/homebrew/bin/bash",
		},
		{
			name:       "skips a 3.x candidate in favour of a later 5.x one",
			candidates: []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash"},
			versions:   map[string]int{"/usr/local/bin/bash": 5, "/bin/bash": 3},
			missing:    map[string]bool{"/opt/homebrew/bin/bash": true},
			want:       "/usr/local/bin/bash",
		},
		{
			name:       "falls back to /bin/bash when every candidate is < 4 or missing",
			candidates: []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash"},
			versions:   map[string]int{"/bin/bash": 3},
			missing:    map[string]bool{"/opt/homebrew/bin/bash": true, "/usr/local/bin/bash": true},
			want:       fallbackBash,
		},
		{
			name:       "falls back to /bin/bash when the probe always fails",
			candidates: []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash"},
			missing:    map[string]bool{"/opt/homebrew/bin/bash": true, "/usr/local/bin/bash": true, "/bin/bash": true},
			want:       fallbackBash,
		},
		{
			name:       "handles an empty candidate list",
			candidates: nil,
			want:       fallbackBash,
		},
		{
			name:       "deduplicates repeated candidates",
			candidates: []string{"/usr/local/bin/bash", "/usr/local/bin/bash", "/bin/bash"},
			versions:   map[string]int{"/usr/local/bin/bash": 5, "/bin/bash": 3},
			want:       "/usr/local/bin/bash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := fakeProbe(tt.versions, tt.missing)
			got := resolveBash(tt.candidates, probe)
			if got != tt.want {
				t.Errorf("resolveBash() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseBashMajorVersion(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantMajor int
		wantOK    bool
	}{
		{
			name:      "valid 5.x string",
			input:     "GNU bash, version 5.3.15(1)-release (aarch64-apple-darwin24.6.0)\nCopyright...",
			wantMajor: 5,
			wantOK:    true,
		},
		{
			name:      "valid 3.2 string",
			input:     "GNU bash, version 3.2.57(1)-release (x86_64-apple-darwin23)\nCopyright...",
			wantMajor: 3,
			wantOK:    true,
		},
		{
			name:      "garbage",
			input:     "not bash output at all, sorry",
			wantMajor: 0,
			wantOK:    false,
		},
		{
			name:      "empty string",
			input:     "",
			wantMajor: 0,
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, ok := parseBashMajorVersion(tt.input)
			if ok != tt.wantOK || major != tt.wantMajor {
				t.Errorf("parseBashMajorVersion(%q) = (%d, %v), want (%d, %v)", tt.input, major, ok, tt.wantMajor, tt.wantOK)
			}
		})
	}
}

// TestWithResolvedShebang verifies that the generated script's first line is
// the resolved shebang and that the explanatory comment block below it
// survives intact.
func TestWithResolvedShebang(t *testing.T) {
	original := "#!/bin/bash\n# comment line one\n# comment line two\necho hi\n"
	got := withResolvedShebang(original, "/opt/homebrew/bin/bash")

	lines := strings.SplitN(got, "\n", 2)
	if lines[0] != "#!/opt/homebrew/bin/bash" {
		t.Errorf("first line = %q, want %q", lines[0], "#!/opt/homebrew/bin/bash")
	}

	rest := lines[1]
	wantRest := "# comment line one\n# comment line two\necho hi\n"
	if rest != wantRest {
		t.Errorf("remainder of script changed:\ngot:  %q\nwant: %q", rest, wantRest)
	}
}

func TestWithResolvedShebang_NoTrailingNewlineInFirstLine(t *testing.T) {
	// Degenerate case: a script with no newline at all still gets a shebang.
	got := withResolvedShebang("#!/bin/bash", "/usr/local/bin/bash")
	if got != "#!/usr/local/bin/bash" {
		t.Errorf("got %q, want %q", got, "#!/usr/local/bin/bash")
	}
}

// TestResolvedBash_CachesAcrossCalls verifies that resolvedBash() only
// resolves once per process (sync.Once) -- repeated calls return the same
// path without re-probing.
func TestResolvedBash_CachesAcrossCalls(t *testing.T) {
	resetResolvedBashForTest()
	t.Cleanup(resetResolvedBashForTest)

	first := resolvedBash()
	second := resolvedBash()
	if first != second {
		t.Errorf("resolvedBash() not stable across calls: %q then %q", first, second)
	}
	if first == "" {
		t.Error("resolvedBash() returned empty path")
	}
}

// TestShellPrefixScriptContent_FirstLineIsResolvedShebang verifies that the
// real embedded script, once passed through ShellPrefixScriptContent, has
// the resolved absolute-path shebang as its first line, and that the
// explanatory comment block explaining the bash 3.2 heredoc bug survives.
func TestShellPrefixScriptContent_FirstLineIsResolvedShebang(t *testing.T) {
	content := ShellPrefixScriptContent("")

	lines := strings.SplitN(content, "\n", 2)
	if len(lines) < 2 {
		t.Fatalf("script has no body after the shebang")
	}
	if !strings.HasPrefix(lines[0], "#!") {
		t.Fatalf("first line is not a shebang: %q", lines[0])
	}
	if strings.Contains(lines[0], "env bash") {
		t.Errorf("shebang still uses env bash instead of a resolved absolute path: %q", lines[0])
	}
	if !strings.HasPrefix(lines[0][2:], "/") {
		t.Errorf("shebang path is not absolute: %q", lines[0])
	}

	if !strings.Contains(content, "bash 3.2") || !strings.Contains(content, "heredoc") {
		t.Errorf("explanatory comment about the bash 3.2 heredoc bug did not survive shebang replacement:\n%s", content)
	}
}
