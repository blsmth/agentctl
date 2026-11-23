package hooks

import (
	"encoding/json"
	"testing"
)

func pinArtifactsDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "/Users/u")
	orig := artifactsStateDir
	t.Cleanup(func() { artifactsStateDir = orig })
	artifactsStateDir = func() string { return "/Users/u/.local/state/agentctl/artifacts" }
}

func TestCheckArtifactsTamper(t *testing.T) {
	pinArtifactsDir(t)
	tests := []struct {
		name     string
		command  string
		wantDeny bool
	}{
		{"redirect meta", `echo '{}' > ~/.local/state/agentctl/artifacts/abcdefghij/meta.json`, true},
		{"redirect via HOME", `echo x > "$HOME/.local/state/agentctl/artifacts/x/rev/1/index.html"`, true},
		{"absolute path", `cp /tmp/a.html /Users/u/.local/state/agentctl/artifacts/abcdefghij/rev/1/a.html`, true},
		{"rm -rf store", `rm -rf ~/.local/state/agentctl/artifacts`, true},
		{"xdg", `rm -rf $XDG_STATE_HOME/agentctl/artifacts/abc`, true},
		{"tee", `echo hi | tee /Users/u/.local/state/agentctl/artifacts/x/meta.json`, true},
		{"sed in place", `sed -i 's/a/b/' ~/.local/state/agentctl/artifacts/x/meta.json`, true},
		{"python", `python3 -c "open('/Users/u/.local/state/agentctl/artifacts/x/meta.json','w')"`, true},
		{"chained", `go build ./... && touch ~/.local/state/agentctl/artifacts/.lock`, true},

		{"cat", `cat ~/.local/state/agentctl/artifacts/x/meta.json`, false},
		{"ls", `ls ~/.local/state/agentctl/artifacts`, false},
		{"jq read", `jq .title ~/.local/state/agentctl/artifacts/x/meta.json`, false},
		{"read, save elsewhere", `cat ~/.local/state/agentctl/artifacts/x/meta.json > /tmp/m.json`, false},
		{"agentctl add", `agentctl artifact add report.html --title 'x'`, false},
		{"agentctl add mentions path", `agentctl artifact add r.html --description 'see > ~/.local/state/agentctl/artifacts/x'`, false},
		{"agentctl rm", `agentctl artifact rm abcdefghij --yes`, false},
		{"agentctl prune", `agentctl artifact prune --older-than 7d`, false},
		{"go test", `go test ./internal/artifacts/...`, false},
		{"git add package", `git add internal/artifacts/store.go internal/cmd/artifact.go`, false},
		{"write own file", `echo hi > /tmp/artifacts.html`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkArtifactsTamper(tt.command)
			if (got != "") != tt.wantDeny {
				t.Errorf("checkArtifactsTamper(%q) = %q, wantDeny=%v", tt.command, got, tt.wantDeny)
			}
			// The same rule is part of the global deny list.
			if all := checkGlobalDenyList(tt.command); tt.wantDeny && all == "" {
				t.Errorf("checkGlobalDenyList(%q) did not deny", tt.command)
			}
		})
	}
}

func TestArtifactsEditTarget(t *testing.T) {
	pinArtifactsDir(t)
	in := func(p string) json.RawMessage { return json.RawMessage(`{"file_path":"` + p + `"}`) }
	tests := []struct {
		name  string
		tool  string
		input json.RawMessage
		want  string
	}{
		{"write meta", "Write", in("/Users/u/.local/state/agentctl/artifacts/abcdefghij/meta.json"), "/Users/u/.local/state/agentctl/artifacts/abcdefghij/meta.json"},
		{"edit content", "Edit", in("/Users/u/.local/state/agentctl/artifacts/x/rev/1/index.html"), "/Users/u/.local/state/agentctl/artifacts/x/rev/1/index.html"},
		{"multiedit", "MultiEdit", in("/Users/u/.local/state/agentctl/artifacts/x/meta.json"), "/Users/u/.local/state/agentctl/artifacts/x/meta.json"},
		{"tilde", "Write", in("~/.local/state/agentctl/artifacts/x/meta.json"), "/Users/u/.local/state/agentctl/artifacts/x/meta.json"},
		{"dotdot cleaned", "Write", in("/tmp/../Users/u/.local/state/agentctl/artifacts/x/meta.json"), "/Users/u/.local/state/agentctl/artifacts/x/meta.json"},
		{"read allowed", "Read", in("/Users/u/.local/state/agentctl/artifacts/x/meta.json"), ""},
		{"other dir", "Write", in("/Users/u/.local/state/agentctl/artifacts-old/x"), ""},
		{"sibling", "Write", in("/Users/u/.local/state/agentctl/cron/x"), ""},
		{"project file", "Write", in("/Users/u/code/p/internal/artifacts/store.go"), ""},
		{"bad json", "Write", json.RawMessage(`{`), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := artifactsEditTarget(tt.tool, tt.input); got != tt.want {
				t.Errorf("artifactsEditTarget = %q, want %q", got, tt.want)
			}
		})
	}
}
