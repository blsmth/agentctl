package hubtarget

import (
	"errors"
	"reflect"
	"testing"

	"github.com/blsmth/agentctl/internal/tmux"
)

type fakeLeaver struct {
	lastErr  error
	home     string
	failFor  map[string]bool
	sessions []tmux.SessionActivity
	switched []string
	msg      string
}

func (f *fakeLeaver) SwitchLast() error { return f.lastErr }
func (f *fakeLeaver) SwitchSession(n string) error {
	f.switched = append(f.switched, n)
	if f.failFor[n] {
		return errors.New("fail " + n)
	}
	return nil
}
func (f *fakeLeaver) ViewedHome() string               { return f.home }
func (f *fakeLeaver) Sessions() []tmux.SessionActivity { return f.sessions }
func (f *fakeLeaver) Message(m string)                 { f.msg = m }

func TestLeaveHub(t *testing.T) {
	sess := []tmux.SessionActivity{
		{Name: "agentctl-hub", Activity: 99},
		{Name: "agentctl-hubview-1", Activity: 98},
		{Name: "other", Activity: 97},
		{Name: "agentctl-old", Activity: 10},
		{Name: "agentctl-new", Activity: 50},
	}
	tests := []struct {
		name     string
		f        *fakeLeaver
		switched []string
		wantMsg  bool
	}{
		{"last ok", &fakeLeaver{}, nil, false},
		{"viewed home", &fakeLeaver{lastErr: errors.New("x"), home: "agentctl-a", sessions: sess}, []string{"agentctl-a"}, false},
		{"most recent non-hub", &fakeLeaver{lastErr: errors.New("x"), sessions: sess}, []string{"agentctl-new"}, false},
		{"home fails then fallback", &fakeLeaver{lastErr: errors.New("x"), home: "agentctl-a", failFor: map[string]bool{"agentctl-a": true}, sessions: sess}, []string{"agentctl-a", "agentctl-new"}, false},
		{"all fail", &fakeLeaver{lastErr: errors.New("x"), sessions: sess[:3]}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			LeaveHub(tt.f, "/dev/ttys1")
			if !reflect.DeepEqual(tt.f.switched, tt.switched) {
				t.Errorf("switched = %v, want %v", tt.f.switched, tt.switched)
			}
			if (tt.f.msg != "") != tt.wantMsg {
				t.Errorf("msg = %q, wantMsg %v", tt.f.msg, tt.wantMsg)
			}
		})
	}
}
