package hooks

import (
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestNoticeWho(t *testing.T) {
	ws := windowstate.WindowState{
		SessionID: "a3f9c0de-1111",
		MainPane:  "%34",
		AgentName: "go-engineer",
	}
	ws.ShortTitle.Title = "esc fix in hub"
	if got, want := noticeWho("@12", ws, "go-engineer"), `go-engineer "esc fix in hub" (a3f9)`; got != want {
		t.Errorf("who = %q, want %q", got, want)
	}
	if got, want := noticeLocation("@12", ws), "@12.%34"; got != want {
		t.Errorf("location = %q, want %q", got, want)
	}
	ws.MainPane = ""
	if got := noticeLocation("@12", ws); got != "@12" {
		t.Errorf("location = %q", got)
	}
	if got := alertHandle(windowstate.WindowState{}); got != "" {
		t.Errorf("handle without a session = %q", got)
	}
	if got := alertHandle(ws); got != "a3f9" {
		t.Errorf("handle = %q", got)
	}
}

func TestAlertTitleNeverShowsWindowID(t *testing.T) {
	if got, _ := alertTitle(windowstate.WindowState{}, "", "@7"); got != "session" {
		t.Errorf("title = %q", got)
	}
}
