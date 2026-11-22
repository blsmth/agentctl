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

func TestNotifyLine(t *testing.T) {
	tests := []struct {
		name, change string
		alerted      bool
		want         string
	}{
		{"no change", "", false, "hook/notify: type=idle_prompt window=@1 agent=a alert=false Δ1ms"},
		{"changed", "idle->blocked", true, "hook/notify: type=idle_prompt window=@1 agent=a state=idle->blocked alert=true Δ1ms"},
		{"kept", "kept", true, "hook/notify: type=idle_prompt window=@1 agent=a state=kept alert=true Δ1ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := notifyLine("idle_prompt", "@1", "a", tt.change, tt.alerted, time.Millisecond); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
