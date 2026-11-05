package dispatch

import (
	"strings"
	"testing"
	"time"
)

func TestRunHookCommand(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		timeout     time.Duration
		wantErr     bool
		wantOut     string
		maxDuration time.Duration
	}{
		{name: "success captures output and env", cmd: "echo $HOOK_X", timeout: 5 * time.Second, wantOut: "v1", maxDuration: 5 * time.Second},
		{name: "failure reported", cmd: "exit 3", timeout: 5 * time.Second, wantErr: true, maxDuration: 5 * time.Second},
		// A forked grandchild holds the pipes open; the group kill must reap it.
		{name: "timeout kills process group", cmd: "sleep 30 & sleep 30", timeout: 200 * time.Millisecond, wantErr: true, maxDuration: 5 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			out, err := runHookCommand(tc.cmd, []string{"HOOK_X=v1"}, tc.timeout)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !strings.Contains(string(out), tc.wantOut) {
				t.Errorf("out = %q, want substring %q", out, tc.wantOut)
			}
			if d := time.Since(start); d > tc.maxDuration {
				t.Errorf("took %v, want < %v", d, tc.maxDuration)
			}
		})
	}
}
