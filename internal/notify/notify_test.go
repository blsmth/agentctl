package notify

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// setNotifyCmd points config at a temp config file with the given
// notify_cmd, for the duration of the test.
func setNotifyCmd(t *testing.T, cmd string) {
	t.Helper()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfg := &config.Config{
		NotifyCmd: config.StringPtr(cmd),
		Agents:    map[string]config.AgentConfig{},
	}
	config.SetGlobalConfigPathForTest(cfgPath)
	config.SetConfigPathForTest(cfgPath)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save: %v", err)
	}
	t.Cleanup(func() { config.SetGlobalConfigPathForTest(""); config.SetConfigPathForTest("") })
}

// TestNotifyWithOptions_SubtitleSubstitution verifies that {{subtitle}} is
// substituted and shell-quoted like the other placeholders.
func TestNotifyWithOptions_SubtitleSubstitution(t *testing.T) {
	cfgDir := t.TempDir()
	outFile := filepath.Join(cfgDir, "out.txt")
	setNotifyCmd(t, "printf '%s\\n%s\\n%s' {{title}} {{subtitle}} {{message}} > "+outFile)

	NotifyWithOptions(Options{
		Title:    "go-engineer · myproj",
		Subtitle: "Needs permission — Bash",
		Message:  "git push --force origin main",
	})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read notify output: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "Needs permission — Bash") {
		t.Errorf("output %q does not contain subtitle", got)
	}
	if !strings.Contains(got, "go-engineer · myproj") {
		t.Errorf("output %q does not contain title", got)
	}
	if !strings.Contains(got, "git push --force origin main") {
		t.Errorf("output %q does not contain message", got)
	}
}

// TestNotifyWithOptions_EmptySubtitleIsSafe verifies that an empty subtitle
// substitutes to the empty string and does not produce a broken command
// (e.g. --subtitle with a dangling unquoted argument).
func TestNotifyWithOptions_EmptySubtitleIsSafe(t *testing.T) {
	cfgDir := t.TempDir()
	outFile := filepath.Join(cfgDir, "out.txt")
	// Mirrors DefaultCmd()'s shape: --subtitle {{subtitle}} with nothing supplied.
	setNotifyCmd(t, "printf 'ran --subtitle %s ok' {{subtitle}} > "+outFile)

	NotifyWithOptions(Options{Title: "t", Message: "m"})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read notify output: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "ran --subtitle  ok") && !strings.Contains(got, "ran --subtitle ok") {
		t.Errorf("expected safe empty-subtitle substitution, got %q", got)
	}
}

// TestDefaultCmd_IncludesSubtitleOnDarwin is a lightweight regression guard:
// DefaultCmd's darwin branch must reference {{subtitle}} so alerter receives
// a --subtitle flag (an empty subtitle is safe -- see NotifyWithOptions).
func TestDefaultCmd_IncludesSubtitleOnDarwin(t *testing.T) {
	if !strings.Contains(DefaultCmd(), "{{subtitle}}") && DefaultCmd() != "" {
		t.Errorf("DefaultCmd() = %q, want it to reference {{subtitle}} (or be empty on non-darwin)", DefaultCmd())
	}
}

// TestNotify_DelegatesToNotifyWithOptions verifies the back-compat Notify
// function still fires the command with title/message, and an empty
// subtitle.
func TestNotify_DelegatesToNotifyWithOptions(t *testing.T) {
	cfgDir := t.TempDir()
	outFile := filepath.Join(cfgDir, "out.txt")
	setNotifyCmd(t, "printf '%s|%s' {{title}} {{message}} > "+outFile)

	Notify("my-title", "my-message", nil)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read notify output: %v", err)
	}
	got := string(data)
	if got != "my-title|my-message" {
		t.Errorf("output = %q, want %q", got, "my-title|my-message")
	}
}

// TestNotifyWithOptions_EmptyCmdDisables verifies an explicitly empty
// notify_cmd disables notifications instead of falling back to the platform
// default, while an unset one does not run a user command.
func TestNotifyWithOptions_EmptyCmdDisables(t *testing.T) {
	setNotifyCmd(t, "")
	// With the platform default this would exec alerter/notify-send; a PATH
	// containing only a marker-writing fake proves nothing ran.
	bin := t.TempDir()
	marker := filepath.Join(bin, "ran")
	for _, name := range []string{"alerter", "notify-send"} {
		script := "#!/bin/sh\ntouch " + marker + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	Notify("t", "m", nil)

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("notification ran despite notify_cmd being explicitly empty")
	}
}

func TestDefaultCmd_LinuxNotifySend(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only default")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if got := DefaultCmd(); got != "" {
		t.Errorf("DefaultCmd() without notify-send = %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(bin, "notify-send"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DefaultCmd(); got != "notify-send {{title}} {{message}}" {
		t.Errorf("DefaultCmd() = %q", got)
	}
}

func TestDefaultCmd_DarwinAlerter(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only default")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if got := DefaultCmd(); got != "" {
		t.Errorf("DefaultCmd() without alerter = %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(bin, "alerter"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DefaultCmd(); !strings.HasPrefix(got, "alerter ") {
		t.Errorf("DefaultCmd() = %q, want alerter command", got)
	}
}

// TestExpandNotifyCmd_SinglePass is the regression test for sequential
// placeholder substitution: a value containing a later placeholder must stay
// literal (and quoted) rather than being substituted again.
func TestExpandNotifyCmd_SinglePass(t *testing.T) {
	tests := []struct {
		name                                 string
		title, subtitle, message, agent, typ string
		wantMessage                          string
	}{
		{name: "message with agent placeholder", message: "hi {{agent}}", agent: "$(touch /tmp/p)", wantMessage: "hi {{agent}}"},
		{name: "title with message placeholder", title: "{{message}}", message: "'; touch /tmp/p; '", wantMessage: "'; touch /tmp/p; '"},
		{name: "subtitle with type placeholder", subtitle: "{{type}}", typ: "x'y", message: "m", wantMessage: "m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := expandNotifyCmd("printf '%s|%s|%s|%s|%s' {{title}} {{subtitle}} {{message}} {{agent}} {{type}}",
				tt.title, tt.subtitle, tt.message, tt.agent, tt.typ)
			out, err := exec.Command("sh", "-c", cmd).Output()
			if err != nil {
				t.Fatalf("running %q: %v", cmd, err)
			}
			want := strings.Join([]string{tt.title, tt.subtitle, tt.wantMessage, tt.agent, tt.typ}, "|")
			if string(out) != want {
				t.Errorf("output %q, want %q (cmd %q)", out, want, cmd)
			}
		})
	}
}
