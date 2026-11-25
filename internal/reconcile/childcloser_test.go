package reconcile

import (
	"errors"
	"reflect"
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func rec(by, bySession, sid string) windowstate.WindowState {
	return windowstate.WindowState{SpawnedBy: by, SpawnedBySession: bySession, SessionID: sid}
}

func TestChildCloserClose(t *testing.T) {
	wins := []LiveWindow{
		{Session: "s", WindowID: "@2", Record: rec("@1", "p", "c")},
		{Session: "s", WindowID: "@3", Record: rec("@2", "c", "g")},
	}
	tests := []struct {
		name    string
		enabled bool
		sid     string
		listErr error
		failID  string
		want    []string
	}{
		{"deepest first", true, "p", nil, "", []string{"@3", "@2"}},
		{"disabled", false, "p", nil, "", nil},
		{"empty session id", true, "", nil, "", nil},
		{"list failure", true, "p", errors.New("down"), "", nil},
		{"kill failure skipped", true, "p", nil, "@3", []string{"@2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			c := ChildCloser{
				Enabled: func() bool { return tt.enabled },
				List:    func() ([]LiveWindow, error) { return wins, tt.listErr },
				Kill: func(w LiveWindow) error {
					if w.WindowID == tt.failID {
						return errors.New("kill failed")
					}
					got = append(got, w.WindowID)
					return nil
				},
			}
			c.Close("@1", tt.sid)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("killed %v, want %v", got, tt.want)
			}
		})
	}
}
