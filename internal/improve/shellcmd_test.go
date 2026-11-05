package improve

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/shellquote"
)

// TestBuildImproveShellCmd runs the generated command under /bin/sh with a
// prompt file whose path contains spaces, quotes, and metacharacters. The
// "claude" invocation is replaced by a printf that records its argument.
func TestBuildImproveShellCmd(t *testing.T) {
	const prompt = "Improve it's `prompt`; $(touch /tmp/pwned)\nline two"

	tests := []struct {
		name       string
		skipEditor bool
		content    string
		wantOut    bool
	}{
		{"skip editor", true, prompt, true},
		{"editor keeps prompt", false, prompt, true},
		{"editor empties prompt aborts", false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "it's a $(dir); `x`")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			promptFile := filepath.Join(dir, "prompt file.txt")
			if err := os.WriteFile(promptFile, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			outFile := filepath.Join(dir, "out")
			baseFlags := "printf '%s' >" + shellquote.Quote(outFile)

			cmd := exec.Command("/bin/sh", "-c", buildImproveShellCmd(baseFlags, promptFile, tt.skipEditor))
			cmd.Env = append(os.Environ(), "EDITOR=true")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("command failed: %v\n%s", err, out)
			}

			got, err := os.ReadFile(outFile)
			switch {
			case tt.wantOut && err != nil:
				t.Fatalf("claude stand-in did not run: %v", err)
			case tt.wantOut && string(got) != tt.content:
				t.Errorf("claude received %q, want %q", got, tt.content)
			case !tt.wantOut && err == nil:
				t.Errorf("claude stand-in ran for an emptied prompt")
			}
			if _, err := os.Stat(promptFile); !os.IsNotExist(err) {
				t.Errorf("prompt file not removed (stat err = %v)", err)
			}
		})
	}
}
