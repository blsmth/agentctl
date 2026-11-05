package cmd

import (
	"strings"
	"testing"
)

func TestResolveSendAddress(t *testing.T) {
	tests := []struct {
		name          string
		addr          string
		runningAgents map[string]string
		wantWindowID  string
		wantErr       bool
		wantErrSubstr string
	}{
		{
			name:         "window ID passthrough",
			addr:         "@41",
			wantWindowID: "@41",
		},
		{
			name: "window ID passthrough ignores running agents",
			addr: "@41",
			runningAgents: map[string]string{
				"@1": "go-engineer",
			},
			wantWindowID: "@41",
		},
		{
			name: "unique name match",
			addr: "go-engineer",
			runningAgents: map[string]string{
				"@10": "go-engineer",
				"@11": "python-engineer",
			},
			wantWindowID: "@10",
		},
		{
			name: "unique name match is case-insensitive",
			addr: "Go-Engineer",
			runningAgents: map[string]string{
				"@10": "go-engineer",
			},
			wantWindowID: "@10",
		},
		{
			name: "unique name match strips instance suffix",
			addr: "go-engineer",
			runningAgents: map[string]string{
				"@10": "go-engineer:2",
			},
			wantWindowID: "@10",
		},
		{
			name: "ambiguous name errors listing candidates",
			addr: "go-engineer",
			runningAgents: map[string]string{
				"@10": "go-engineer",
				"@20": "go-engineer:2",
			},
			wantErr:       true,
			wantErrSubstr: "@10",
		},
		{
			name:          "no match errors",
			addr:          "nonexistent",
			runningAgents: map[string]string{"@10": "go-engineer"},
			wantErr:       true,
			wantErrSubstr: "nonexistent",
		},
		{
			name:          "no match with empty running agents",
			addr:          "go-engineer",
			runningAgents: map[string]string{},
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSendAddress(tt.addr, tt.runningAgents)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveSendAddress(%q, %v) = %q, nil; want error", tt.addr, tt.runningAgents, got)
				}
				if tt.wantErrSubstr != "" && !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSendAddress(%q, %v) unexpected error: %v", tt.addr, tt.runningAgents, err)
			}
			if got != tt.wantWindowID {
				t.Errorf("resolveSendAddress(%q, %v) = %q, want %q", tt.addr, tt.runningAgents, got, tt.wantWindowID)
			}
		})
	}
}

func TestResolveSendAddressAmbiguousListsAllCandidates(t *testing.T) {
	running := map[string]string{
		"@10": "go-engineer",
		"@20": "go-engineer:2",
		"@30": "go-engineer:3",
	}
	_, err := resolveSendAddress("go-engineer", running)
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}
	for _, id := range []string{"@10", "@20", "@30"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q missing candidate %q", err.Error(), id)
		}
	}
}

// TestShouldNotifyOnEnqueue is a pure-function test of the notify gate: it
// asserts the working/not-working decision without touching a real
// notifier (see internal/notify.Notify), matching how the ticket wants this
// gate covered.
func TestShouldNotifyOnEnqueue(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  bool
	}{
		{name: "working: PostToolUse delivers within one tool call, no notification", state: "working", want: false},
		{name: "idle: no hook about to fire, must notify", state: "idle", want: true},
		{name: "blocked: no hook about to fire, must notify", state: "blocked", want: true},
		{name: "gone: no hook about to fire, must notify", state: "gone", want: true},
		{name: "no recorded state at all: must notify", state: "", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldNotifyOnEnqueue(tt.state); got != tt.want {
				t.Errorf("shouldNotifyOnEnqueue(%q) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}
