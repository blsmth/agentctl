package hubtarget

import (
	"reflect"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		cmd  string
		want Scope
	}{
		{"kill-menu", ScopeWindow}, {"logs", ScopeWindow}, {"settings-menu", ScopeWindow},
		{"improve-menu", ScopeWindow}, {"hud-toggle", ScopeWindow}, {"hud", ScopeWindow},
		{"context-push-menu", ScopeWindow},
		{"menu", ScopeProject}, {"launch", ScopeProject}, {"create-menu", ScopeProject},
		{"ticket-launch", ScopeProject}, {"ticket-menu", ScopeProject}, {"permissions-menu", ScopeProject},
		{"open-tickets-window", ScopeProject}, {"open-sessions-window", ScopeProject}, {"open-agents-window", ScopeProject},
		{"quit-menu", ScopeHubQuit},
		{"restart-menu", ScopeHubRefused}, {"kill-all-menu", ScopeHubRefused},
		{"hub", ScopeOther}, {"jump-next-blocked", ScopeOther}, {"theme-picker", ScopeOther}, {"status", ScopeOther},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := Classify(tt.cmd); got != tt.want {
				t.Errorf("Classify(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	viewed := Facts{KeySessionHub: true, ViewedWindow: "@7", HomeSession: "agentctl-acme", HomeRuntime: "/p/acme/.agentctl"}
	with := func(f Facts, cmd string) Facts { f.Subcmd = cmd; return f }
	noViewer := Facts{KeySessionHub: true}
	notHub := viewed
	notHub.KeySessionHub = false

	tests := []struct {
		name string
		in   Facts
		want Decision
	}{
		{"window scoped redirects", with(viewed, "kill-menu"),
			Decision{Action: ActionRedirect, Window: "@7", RuntimeDir: "/p/acme/.agentctl"}},
		{"project scoped keeps window", with(viewed, "launch"),
			Decision{Action: ActionRedirect, RuntimeDir: "/p/acme/.agentctl"}},
		{"quit closes hub", with(viewed, "quit-menu"), Decision{Action: ActionHubClose}},
		{"quit closes hub without viewer", with(noViewer, "quit-menu"), Decision{Action: ActionHubClose}},
		{"restart refused", with(viewed, "restart-menu"), Decision{Action: ActionRefuse, Message: RefusedMessage}},
		{"kill-all refused", with(viewed, "kill-all-menu"), Decision{Action: ActionRefuse, Message: RefusedMessage}},
		{"no viewer window scoped", with(noViewer, "logs"), Decision{Action: ActionRefuse, Message: NoViewerMessage}},
		{"no viewer project scoped", with(noViewer, "menu"), Decision{Action: ActionRefuse, Message: NoViewerMessage}},
		{"missing runtime dir", with(Facts{KeySessionHub: true, ViewedWindow: "@7", HomeSession: "s"}, "menu"),
			Decision{Action: ActionRefuse, Message: NoViewerMessage}},
		{"not in hub untouched", with(notHub, "kill-menu"), Decision{}},
		{"not in hub quit untouched", with(notHub, "quit-menu"), Decision{}},
		{"other command untouched in hub", with(viewed, "hub"), Decision{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.in); got != tt.want {
				t.Errorf("Resolve = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRewriteArgs(t *testing.T) {
	args := []string{"agentctl", "kill-menu", "@1"}
	got := RewriteArgs(args, "@1", Decision{Window: "@7"})
	if want := []string{"agentctl", "kill-menu", "@7"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if args[2] != "@1" {
		t.Error("input mutated")
	}
	if got := RewriteArgs(args, "@1", Decision{}); !reflect.DeepEqual(got, args) {
		t.Errorf("no window: got %v", got)
	}
}
