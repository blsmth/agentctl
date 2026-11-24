package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/servectl"
)

// `serve stop` only switches the phone off; `serve stop --all` is the old
// full stop.
func TestServeStopAction(t *testing.T) {
	dir := t.TempDir()
	c := servectl.Controller{
		Dir:     dir,
		GOOS:    "linux",
		Running: func(string) (int, bool) { return 1, true },
	}
	msg, err := serveStopAction(false)(c)
	if err != nil || !servectl.PhoneOff(dir) || !strings.HasPrefix(msg, "phone access off") {
		t.Fatalf("stop = %q, %v (phone off %v)", msg, err, servectl.PhoneOff(dir))
	}
	msg, err = serveStopAction(true)(c)
	if err != nil || msg != "Run: systemctl --user stop "+servectl.SystemdUnit {
		t.Fatalf("stop --all = %q, %v", msg, err)
	}
}

func TestRunServeAction(t *testing.T) {
	tests := []struct {
		name      string
		client    string
		msg       string
		err       error
		wantOut   string
		wantShown string
	}{
		{name: "cli success", msg: "serve stopped", wantOut: "serve stopped\n"},
		{name: "cli error", err: errors.New("boom")},
		{name: "keybinding success", client: "/dev/ttys001", msg: "serve started at https://h/", wantOut: "serve started at https://h/\n", wantShown: "serve started at https://h/"},
		{name: "keybinding error", client: "/dev/ttys001", err: errors.New("not installed"), wantShown: "serve: not installed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origC, origD := serveController, serveDisplay
			t.Cleanup(func() { serveController, serveDisplay = origC, origD })
			serveController = func() (servectl.Controller, error) { return servectl.Controller{}, nil }
			var shownTo, shown string
			serveDisplay = func(client, msg string) { shownTo, shown = client, msg }

			var out bytes.Buffer
			err := runServeAction(&out, tt.client, func(servectl.Controller) (string, error) { return tt.msg, tt.err })
			if (err != nil) != (tt.err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if out.String() != tt.wantOut {
				t.Errorf("out = %q, want %q", out.String(), tt.wantOut)
			}
			if shown != tt.wantShown || (tt.client != "" && shownTo != tt.client) {
				t.Errorf("shown %q to %q, want %q to %q", shown, shownTo, tt.wantShown, tt.client)
			}
		})
	}
}
