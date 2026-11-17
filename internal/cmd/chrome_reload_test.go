package cmd

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/selfexec"
)

type fakePane struct {
	reexec bool
}

func (f fakePane) Init() tea.Cmd                       { return nil }
func (f fakePane) Update(tea.Msg) (tea.Model, tea.Cmd) { return f, nil }
func (f fakePane) View() tea.View                      { return tea.NewView("") }
func (f fakePane) Reexec() bool                        { return f.reexec }

func stubExec(t *testing.T, captureErr, execErr error) *[]string {
	t.Helper()
	var execs []string
	oldE, oldC := paneExec, paneCapture
	t.Cleanup(func() { paneExec, paneCapture = oldE, oldC })
	paneCapture = func() (selfexec.Identity, error) {
		return selfexec.Identity{Path: "/opt/agentctl"}, captureErr
	}
	paneExec = func(path string) error { execs = append(execs, path); return execErr }
	return &execs
}

func TestRunWithReload(t *testing.T) {
	tests := []struct {
		name       string
		reexec     bool
		captureErr error
		execErr    error
		wantExecs  int
		wantRuns   int
	}{
		{"unchanged: no exec", false, nil, nil, 0, 1},
		{"changed: quit then exec", true, nil, nil, 1, 1},
		{"capture error: keep running old image", true, errors.New("x"), nil, 0, 2},
		{"exec failure: keep running old image", true, nil, errors.New("boom"), 1, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			execs := stubExec(t, tt.captureErr, tt.execErr)
			runs := 0
			run := func(m tea.Model) (tea.Model, error) {
				runs++
				if runs > 1 {
					return fakePane{}, nil // reload disabled on the rerun
				}
				return fakePane{reexec: tt.reexec}, nil
			}
			runWithReload("test", fakePane{}, run, func(tea.Model) reloadable { return fakePane{} })
			if len(*execs) != tt.wantExecs {
				t.Errorf("execs = %v, want %d", *execs, tt.wantExecs)
			}
			if runs != tt.wantRuns {
				t.Errorf("runs = %d, want %d", runs, tt.wantRuns)
			}
		})
	}
}
