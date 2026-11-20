package cmd

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHubResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	type call struct{ agent, sid, dir string }
	stub := func(t *testing.T, err error) *[]call {
		t.Helper()
		var calls []call
		old := hubResumeLaunch
		hubResumeLaunch = func(agent, sid, dir string) (string, error) {
			calls = append(calls, call{agent, sid, dir})
			return "@77", err
		}
		t.Cleanup(func() { hubResumeLaunch = old })
		return &calls
	}

	t.Run("resumes in the work dir and prints the window", func(t *testing.T) {
		calls := stub(t, nil)
		proj, work := t.TempDir(), t.TempDir()
		f := &fakeHubNewSession{}
		var out bytes.Buffer
		err := runHubResume(&out, HubResumeOptions{ProjectDir: proj, WorkDir: work, Agent: "go-engineer", SessionID: "u1"}, f.deps(filepath.Join(proj, ".agentctl")))
		if err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || (*calls)[0] != (call{"go-engineer", "u1", work}) {
			t.Errorf("calls = %+v", *calls)
		}
		if f.started != 1 || f.launched != 0 {
			t.Errorf("started=%d launched=%d, want the session started and no plain launch", f.started, f.launched)
		}
		if strings.TrimSpace(out.String()) != "@77" {
			t.Errorf("out = %q", out.String())
		}
	})

	t.Run("refusals launch nothing", func(t *testing.T) {
		proj := t.TempDir()
		tests := []struct {
			name string
			o    HubResumeOptions
			want string
		}{
			{"no session id", HubResumeOptions{ProjectDir: proj, Agent: "a"}, "no session id"},
			{"no agent", HubResumeOptions{ProjectDir: proj, SessionID: "u"}, "no agent"},
			{"work dir gone", HubResumeOptions{ProjectDir: proj, WorkDir: filepath.Join(proj, "gone"), Agent: "a", SessionID: "u"}, "is gone"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				calls := stub(t, nil)
				f := &fakeHubNewSession{}
				err := runHubResume(&bytes.Buffer{}, tt.o, f.deps(filepath.Join(proj, ".agentctl")))
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Errorf("err = %v, want %q", err, tt.want)
				}
				if len(*calls) != 0 || f.started != 0 {
					t.Errorf("launched anyway: calls=%v started=%d", *calls, f.started)
				}
			})
		}
	})

	t.Run("launch error is returned", func(t *testing.T) {
		stub(t, errors.New("boom"))
		proj := t.TempDir()
		f := &fakeHubNewSession{exists: true, path: proj}
		err := runHubResume(&bytes.Buffer{}, HubResumeOptions{ProjectDir: proj, Agent: "a", SessionID: "u"}, f.deps(filepath.Join(proj, ".agentctl")))
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("err = %v", err)
		}
	})
}
