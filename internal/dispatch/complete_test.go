package dispatch

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/state"
)

func TestHandleComplete_FiltersBadTransitions(t *testing.T) {
	// Configure a dispatch_complete hook so that any case which DOES spawn a
	// background goroutine gives us a deterministic completion signal (via
	// runHook) to synchronize on, instead of a fragile sleep. This avoids a
	// data race on config's package-level test config path across subtests.
	config.SetConfigPathForTest(filepath.Join(t.TempDir(), "config.json"))
	globalCfg := &config.Config{
		Agents: map[string]config.AgentConfig{},
		Hooks:  config.HooksConfig{"dispatch_complete": {"true"}},
	}
	if err := config.Save(globalCfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	cfg := config.DefaultAgentConfig()

	tests := []struct {
		name            string
		t               state.Transition
		spawnsGoroutine bool
	}{
		{
			name: "not done",
			t:    state.Transition{Done: false, To: state.StatusIdle, Mode: "dispatch", TicketNumber: 42},
		},
		{
			name: "not idle",
			t:    state.Transition{Done: true, To: state.StatusWorking, Mode: "dispatch", TicketNumber: 42},
		},
		{
			name: "not dispatch mode",
			t:    state.Transition{Done: true, To: state.StatusIdle, Mode: "discuss", TicketNumber: 42},
		},
		{
			name:            "no ticket number and no branch",
			t:               state.Transition{Done: true, To: state.StatusIdle, Mode: "dispatch", TicketNumber: 0},
			spawnsGoroutine: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			openCalled := false
			hookCalled := make(chan struct{}, 1)
			h := &completeHandler{
				lookupPR: func(repo, branch string) int { called = true; return 0 },
				runHook:  func(cmd string, env []string) { hookCalled <- struct{}{} },
				openPR:   func(pr int, workDir string) { openCalled = true },
			}

			h.handle(tc.t, cfg)

			if tc.spawnsGoroutine {
				// Wait for the goroutine to reach completion (runHook fires,
				// happens strictly after any config.Load()/lookupPR calls),
				// so the next subtest doesn't race with this one's config read.
				select {
				case <-hookCalled:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for goroutine to complete")
				}
			} else {
				// These cases hit an early-return guard and never spawn a
				// goroutine at all — a short sleep is sufficient to confirm
				// nothing fires.
				time.Sleep(10 * time.Millisecond)
			}

			if called {
				t.Errorf("lookupPR was called unexpectedly for transition %q", tc.name)
			}
			if openCalled {
				t.Errorf("openPR was called unexpectedly for transition %q", tc.name)
			}
		})
	}
}

func TestHandleComplete_SwapLabels(t *testing.T) {
	tests := []struct {
		name          string
		autoPoll      bool
		prNumber      int
		wantSwapCalls int
		wantIssueArg  int
	}{
		{
			name:          "Autopilot on + PR found swaps labels",
			autoPoll:      true,
			prNumber:      99,
			wantSwapCalls: 1,
			wantIssueArg:  42,
		},
		{
			name:          "Autopilot off does not swap labels",
			autoPoll:      false,
			prNumber:      99,
			wantSwapCalls: 0,
		},
		{
			name:          "Autopilot on + no PR does not swap labels",
			autoPoll:      true,
			prNumber:      0,
			wantSwapCalls: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config.SetConfigPathForTest(filepath.Join(t.TempDir(), "config.json"))

			globalCfg := &config.Config{
				Agents:   map[string]config.AgentConfig{},
				AutoPoll: tc.autoPoll,
			}
			if err := config.Save(globalCfg); err != nil {
				t.Fatalf("save config: %v", err)
			}

			swapCalled := make(chan int, 1)
			h := &completeHandler{
				lookupPR: func(repo, branch string) int { return tc.prNumber },
				runHook:  func(cmd string, env []string) {},
				swapLabels: func(repo string, issueNumber int) {
					swapCalled <- issueNumber
				},
			}

			tr := state.Transition{
				Agent:        "go-engineer",
				Done:         true,
				To:           state.StatusIdle,
				Mode:         "dispatch",
				TicketNumber: 42,
				Branch:       "agent/go-engineer/issue-42",
			}
			h.handle(tr, config.DefaultAgentConfig())

			if tc.wantSwapCalls == 0 {
				select {
				case issue := <-swapCalled:
					t.Errorf("swapLabels called unexpectedly with issue #%d", issue)
				case <-time.After(200 * time.Millisecond):
					// Good — not called.
				}
			} else {
				select {
				case issue := <-swapCalled:
					if issue != tc.wantIssueArg {
						t.Errorf("swapLabels called with issue #%d, want #%d", issue, tc.wantIssueArg)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for swapLabels to be called")
				}
			}
		})
	}
}

func TestHandleComplete_NonDispatchModeSkipsEverything(t *testing.T) {
	modes := []string{"discuss", "bare", ""}

	for _, mode := range modes {
		t.Run(fmt.Sprintf("mode=%q", mode), func(t *testing.T) {
			lookupCalled := false
			swapCalled := false
			openCalled := false
			h := &completeHandler{
				lookupPR:   func(repo, branch string) int { lookupCalled = true; return 1 },
				runHook:    func(cmd string, env []string) {},
				swapLabels: func(repo string, issueNumber int) { swapCalled = true },
				openPR:     func(pr int, workDir string) { openCalled = true },
			}

			h.handle(state.Transition{
				Done:         true,
				To:           state.StatusIdle,
				Mode:         mode,
				TicketNumber: 42,
				Branch:       "some-branch",
			}, config.DefaultAgentConfig())
			time.Sleep(20 * time.Millisecond)

			if lookupCalled {
				t.Errorf("lookupPR should not be called for mode %q", mode)
			}
			if swapCalled {
				t.Errorf("swapLabels should not be called for mode %q", mode)
			}
			if openCalled {
				t.Errorf("openPR should not be called for mode %q", mode)
			}
		})
	}
}

func TestHandleComplete_Adhoc(t *testing.T) {
	tests := []struct {
		name         string
		branch       string
		wantLookup   bool
		wantSwap     bool
		wantHookCall bool
	}{
		{
			name:         "adhoc with branch: lookupPR and hooks fire, swapLabels does not",
			branch:       "agent/go-engineer/adhoc-1234",
			wantLookup:   true,
			wantSwap:     false,
			wantHookCall: true,
		},
		{
			name:         "adhoc with empty branch: no PR lookup, hooks still fire",
			branch:       "",
			wantLookup:   false,
			wantSwap:     false,
			wantHookCall: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config.SetConfigPathForTest(filepath.Join(t.TempDir(), "config.json"))
			globalCfg := &config.Config{
				Agents:   map[string]config.AgentConfig{},
				AutoPoll: true,
			}
			if err := config.Save(globalCfg); err != nil {
				t.Fatalf("save config: %v", err)
			}

			lookupCalled := make(chan struct{}, 1)
			swapCalled := make(chan int, 1)
			hookCalled := make(chan []string, 1)

			h := &completeHandler{
				lookupPR: func(repo, branch string) int {
					lookupCalled <- struct{}{}
					return 99
				},
				runHook: func(cmd string, env []string) {
					hookCalled <- env
				},
				swapLabels: func(repo string, issueNumber int) {
					swapCalled <- issueNumber
				},
			}

			agentCfg := config.DefaultAgentConfig()
			agentCfg.Hooks = config.HooksConfig{"dispatch_complete": {"echo done"}}

			tr := state.Transition{
				Agent:        "go-engineer",
				Done:         true,
				To:           state.StatusIdle,
				Mode:         "adhoc",
				TicketNumber: 0,
				Branch:       tc.branch,
			}
			h.handle(tr, agentCfg)

			if tc.wantLookup {
				select {
				case <-lookupCalled:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for lookupPR to be called")
				}
			} else {
				select {
				case <-lookupCalled:
					t.Error("lookupPR called unexpectedly")
				case <-time.After(200 * time.Millisecond):
				}
			}

			if tc.wantSwap {
				select {
				case <-swapCalled:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for swapLabels to be called")
				}
			} else {
				select {
				case issue := <-swapCalled:
					t.Errorf("swapLabels called unexpectedly with issue #%d", issue)
				case <-time.After(200 * time.Millisecond):
				}
			}

			if tc.wantHookCall {
				select {
				case env := <-hookCalled:
					found := false
					for _, e := range env {
						if e == "AGENTCTL_ISSUE=0" {
							found = true
						}
					}
					if !found {
						t.Errorf("expected AGENTCTL_ISSUE=0 in hook env, got %v", env)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for runHook to be called")
				}
			} else {
				select {
				case <-hookCalled:
					t.Error("runHook called unexpectedly")
				case <-time.After(200 * time.Millisecond):
				}
			}
		})
	}
}

func TestHandleComplete_OpenPRInBrowser(t *testing.T) {
	tests := []struct {
		name            string
		prNumber        int
		openPRInBrowser *bool
		wantOpenCalls   int
	}{
		{
			name:          "PR found opens browser",
			prNumber:      99,
			wantOpenCalls: 1,
		},
		{
			name:          "no PR found does not open browser",
			prNumber:      0,
			wantOpenCalls: 0,
		},
		{
			name:            "explicitly disabled does not open browser",
			prNumber:        99,
			openPRInBrowser: boolPtr(false),
			wantOpenCalls:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ResetOpenedPRsForTest()
			config.SetConfigPathForTest(filepath.Join(t.TempDir(), "config.json"))
			globalCfg := &config.Config{
				Agents:          map[string]config.AgentConfig{},
				OpenPRInBrowser: tc.openPRInBrowser,
			}
			if err := config.Save(globalCfg); err != nil {
				t.Fatalf("save config: %v", err)
			}

			openCalled := make(chan struct {
				pr      int
				workDir string
			}, 1)
			hookCalled := make(chan struct{}, 1)
			h := &completeHandler{
				lookupPR: func(repo, branch string) int { return tc.prNumber },
				runHook:  func(cmd string, env []string) { hookCalled <- struct{}{} },
				openPR: func(pr int, workDir string) {
					openCalled <- struct {
						pr      int
						workDir string
					}{pr, workDir}
				},
			}

			agentCfg := config.DefaultAgentConfig()
			agentCfg.Hooks = config.HooksConfig{"dispatch_complete": {"echo done"}}

			tr := state.Transition{
				Agent:        "go-engineer",
				Done:         true,
				To:           state.StatusIdle,
				Mode:         "dispatch",
				TicketNumber: 42,
				Branch:       "agent/go-engineer/issue-42",
				WorkDir:      "/tmp/some-workdir",
			}
			h.handle(tr, agentCfg)

			// Synchronize on the hook, which always fires after the opener check.
			select {
			case <-hookCalled:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for goroutine to complete")
			}

			if tc.wantOpenCalls == 0 {
				select {
				case got := <-openCalled:
					t.Errorf("openPR called unexpectedly with pr=%d workDir=%q", got.pr, got.workDir)
				default:
				}
			} else {
				select {
				case got := <-openCalled:
					if got.pr != tc.prNumber {
						t.Errorf("openPR called with pr=%d, want %d", got.pr, tc.prNumber)
					}
					if got.workDir != tr.WorkDir {
						t.Errorf("openPR called with workDir=%q, want %q", got.workDir, tr.WorkDir)
					}
				default:
					t.Error("openPR was not called")
				}
			}
		})
	}
}

func TestHandleComplete_OpenPRInBrowser_Dedupe(t *testing.T) {
	ResetOpenedPRsForTest()
	config.SetConfigPathForTest(filepath.Join(t.TempDir(), "config.json"))
	globalCfg := &config.Config{Agents: map[string]config.AgentConfig{}}
	if err := config.Save(globalCfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	openCalled := make(chan int, 4)
	hookCalled := make(chan struct{}, 4)
	h := &completeHandler{
		lookupPR: func(repo, branch string) int { return 99 },
		runHook:  func(cmd string, env []string) { hookCalled <- struct{}{} },
		openPR:   func(pr int, workDir string) { openCalled <- pr },
	}

	agentCfg := config.DefaultAgentConfig()
	agentCfg.Hooks = config.HooksConfig{"dispatch_complete": {"echo done"}}

	tr := state.Transition{
		Agent:        "go-engineer",
		Done:         true,
		To:           state.StatusIdle,
		Mode:         "dispatch",
		TicketNumber: 42,
		Branch:       "agent/go-engineer/issue-42",
		WorkDir:      "/tmp/some-workdir",
	}

	// Fire the same transition twice, as if the agent flapped
	// done->working->done within one session.
	h.handle(tr, agentCfg)
	h.handle(tr, agentCfg)

	for i := 0; i < 2; i++ {
		select {
		case <-hookCalled:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for goroutine %d to complete", i)
		}
	}

	gotOpens := 0
	for {
		select {
		case <-openCalled:
			gotOpens++
		default:
			if gotOpens != 1 {
				t.Errorf("openPR called %d times, want exactly 1 (dedupe failed)", gotOpens)
			}
			return
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func TestHandleComplete_NoHookWhenNotConfigured(t *testing.T) {
	config.SetConfigPathForTest(filepath.Join(t.TempDir(), "config.json"))
	// No on_complete in config.
	globalCfg := &config.Config{Agents: map[string]config.AgentConfig{}}
	if err := config.Save(globalCfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	hookCalled := make(chan struct{}, 1)
	h := &completeHandler{
		lookupPR: func(repo, branch string) int { return 0 },
		runHook:  func(cmd string, env []string) { hookCalled <- struct{}{} },
	}

	h.handle(state.Transition{
		Agent:        "go-engineer",
		Done:         true,
		To:           state.StatusIdle,
		Mode:         "dispatch",
		TicketNumber: 1,
		Branch:       "agent/go-engineer/issue-1",
	}, config.DefaultAgentConfig())

	select {
	case <-hookCalled:
		t.Error("runHook was called but no on_complete configured")
	case <-time.After(200 * time.Millisecond):
		// Good — hook not called.
	}
}

// TestHandleCompleteRepoResolutionFailureSkipsGH verifies that when the repo
// cannot be resolved, no PR lookup or label swap runs (so gh is never called
// against a guessed repo), while lifecycle hooks still fire.
func TestHandleCompleteRepoResolutionFailureSkipsGH(t *testing.T) {
	cfg := config.AgentConfig{}
	hookCalled := make(chan struct{}, 1)
	var lookups, swaps int
	h := &completeHandler{
		resolveRepo: func(dir string) (string, error) { return "", errors.New("no remote") },
		lookupPR:    func(repo, branch string) int { lookups++; return 5 },
		runHook:     func(cmd string, env []string) { hookCalled <- struct{}{} },
		swapLabels:  func(repo string, n int) { swaps++ },
	}
	config.SetConfigPathForTest(filepath.Join(t.TempDir(), "config.json"))
	h.handle(state.Transition{
		Done: true, To: state.StatusIdle, Mode: "dispatch",
		TicketNumber: 7, Branch: "b", WorkDir: "/w",
	}, cfg)
	// Give the goroutine time to finish; no hooks are configured so we poll.
	time.Sleep(200 * time.Millisecond)
	if lookups != 0 || swaps != 0 {
		t.Errorf("lookups=%d swaps=%d, want 0 and 0", lookups, swaps)
	}
}

// TestHandleCompletePassesResolvedRepo verifies the resolved slug is threaded
// to the lookup and label-swap calls.
func TestHandleCompletePassesResolvedRepo(t *testing.T) {
	got := make(chan string, 1)
	h := &completeHandler{
		resolveRepo: func(dir string) (string, error) { return "acme/widgets", nil },
		lookupPR:    func(repo, branch string) int { got <- repo; return 0 },
		runHook:     func(cmd string, env []string) {},
	}
	config.SetConfigPathForTest(filepath.Join(t.TempDir(), "config.json"))
	h.handle(state.Transition{
		Done: true, To: state.StatusIdle, Mode: "dispatch",
		TicketNumber: 7, Branch: "b", WorkDir: "/w",
	}, config.AgentConfig{})
	select {
	case r := <-got:
		if r != "acme/widgets" {
			t.Errorf("repo = %q", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lookupPR not called")
	}
}
