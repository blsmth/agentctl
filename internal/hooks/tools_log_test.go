package hooks

import (
	"testing"
	"time"
)

func TestStateChange(t *testing.T) {
	tests := []struct{ from, to, want string }{
		{"working", "working", ""},
		{"idle", "working", "idle->working"},
		{"", "working", "none->working"},
	}
	for _, tt := range tests {
		if got := stateChange(tt.from, tt.to); got != tt.want {
			t.Errorf("stateChange(%q,%q) = %q, want %q", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestLogToolHookDoesNotPanic(t *testing.T) {
	logToolHook("pre-tool", "Bash", "@1", "go-engineer", "", time.Millisecond)
	logToolHook("post-tool", "Bash", "@1", "go-engineer", "idle->working", time.Millisecond)
}

func TestLogNotifyHookDoesNotPanic(t *testing.T) {
	logNotifyHook("idle_prompt", "@1", "go-engineer", "", false, time.Millisecond)
	logNotifyHook("permission_prompt", "@1", "go-engineer", "idle->blocked", true, time.Millisecond)
	logNotifyHook("idle_prompt", "@1", "go-engineer", "kept", true, time.Millisecond)
}
