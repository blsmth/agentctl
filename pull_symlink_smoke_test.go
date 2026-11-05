package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPinnedSessionSurvivesPulledSymlinks reproduces the mid-session
// `git pull` scenario: the runtime dir is pinned through AGENTCTL_RUNTIME_DIR
// (so the startup git check is skipped), and upstream has replaced the
// ignored audit/, inbox/, logs/ and windows/ dirs with committed symlinks
// into the user's home. Running the real hook-post-tool path must not
// write, move, or read through any of them.
//
// HOME and XDG_CONFIG_HOME point at a temp dir; nothing touches the real
// home or the real repository's .agentctl/.
func TestPinnedSessionSurvivesPulledSymlinks(t *testing.T) {
	bin := getTestBinary(t)

	home := t.TempDir()
	repo := t.TempDir()
	rt := filepath.Join(repo, ".agentctl")
	if err := os.MkdirAll(rt, 0o700); err != nil {
		t.Fatal(err)
	}

	// Targets the planted symlinks point at, in the fake home.
	targets := map[string]string{}
	for _, name := range []string{"audit", "inbox", "logs", "windows"} {
		target := filepath.Join(home, "target-"+name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(rt, name)); err != nil {
			t.Fatal(err)
		}
		targets[name] = target
	}

	// A deliverable message waiting behind the inbox symlink: it must be
	// neither delivered (prompt injection) nor moved to read/.
	msgDir := filepath.Join(targets["inbox"], "@99")
	if err := os.MkdirAll(msgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	msg, _ := json.Marshal(map[string]any{
		"id": "20990101T000000.000000000Z-deadbeef", "to": "@99", "kind": "fyi",
		"body": "PLANTED-INSTRUCTIONS", "created": time.Now(),
	})
	msgPath := filepath.Join(msgDir, "20990101T000000.000000000Z-deadbeef.json")
	if err := os.WriteFile(msgPath, msg, 0o644); err != nil {
		t.Fatal(err)
	}

	payload := `{"tool_name":"Bash","tool_input":{"command":"echo secret-command"},"tool_response":{"stdout":"x"},"session_id":"sess-1","cwd":"` + repo + `"}`
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "hook-post-tool")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"AGENTCTL=1",
		"AGENTCTL_RUNTIME_DIR="+rt,
		"AGENTCTL_INSTANCE=",
		"AGENTCTL_SESSION_ID=",
		"AGENTCTL_WINDOW_ID=@99",
		"TMUX=",
		"TMUX_PANE=",
	)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run() // exit status is irrelevant; side effects are what matter

	if strings.Contains(stdout.String(), "PLANTED-INSTRUCTIONS") {
		t.Errorf("message behind a symlinked inbox was delivered:\n%s", stdout.String())
	}
	if _, err := os.Lstat(msgPath); err != nil {
		t.Errorf("planted message was moved or removed: %v", err)
	}
	for name, target := range targets {
		entries, err := os.ReadDir(target)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if name == "inbox" {
			want = 1 // just the planted @99 dir
		}
		if len(entries) != want {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("%s target was written through the symlink: %v", name, names)
		}
	}
	if entries, _ := os.ReadDir(msgDir); len(entries) != 1 {
		t.Errorf("inbox @99 dir changed: %d entries", len(entries))
	}
}
