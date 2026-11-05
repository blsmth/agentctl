package improve

import (
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/state"
)

func TestHandleTransition(t *testing.T) {
	// Save and restore package-level variables.
	origDelay := launchDelay
	origFn := launchSessionFn
	t.Cleanup(func() {
		launchDelay = origDelay
		launchSessionFn = origFn
	})

	// Zero delay so async goroutines complete quickly in tests.
	launchDelay = 0

	goneTransition := state.Transition{
		Agent:    "go-engineer",
		WindowID: "@1",
		From:     state.StatusWorking,
		To:       state.StatusGone,
	}

	tests := []struct {
		name       string
		t          state.Transition
		agentCfg   config.AgentConfig
		wantCalled bool
		wantAgent  string
		wantTheme  string
		wantSkipEd bool
	}{
		{
			name:       "To != Gone — not triggered",
			t:          state.Transition{Agent: "go-engineer", To: state.StatusIdle},
			agentCfg:   config.AgentConfig{AutoImprove: true},
			wantCalled: false,
		},
		{
			name:       "improve~ prefix — recursion guard, not triggered",
			t:          state.Transition{Agent: "improve~go-engineer", To: state.StatusGone},
			agentCfg:   config.AgentConfig{AutoImprove: true},
			wantCalled: false,
		},
		{
			name:       "AutoImprove false — not triggered",
			t:          goneTransition,
			agentCfg:   config.AgentConfig{AutoImprove: false},
			wantCalled: false,
		},
		{
			name:       "Gone + AutoImprove true — triggered with correct args",
			t:          goneTransition,
			agentCfg:   config.AgentConfig{AutoImprove: true},
			wantCalled: true,
			wantAgent:  "go-engineer",
			wantTheme:  "",
			wantSkipEd: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var called bool
			var gotAgent, gotTheme string
			var gotSkipEd bool

			var wg sync.WaitGroup
			if tt.wantCalled {
				wg.Add(1)
			}

			launchSessionFn = func(agentName, theme string, skipEditor bool) error {
				mu.Lock()
				called = true
				gotAgent = agentName
				gotTheme = theme
				gotSkipEd = skipEditor
				mu.Unlock()
				if tt.wantCalled {
					wg.Done()
				}
				return nil
			}

			HandleTransition(tt.t, tt.agentCfg)

			if tt.wantCalled {
				// Wait for the goroutine to call launchSessionFn (with 1s timeout).
				done := make(chan struct{})
				go func() {
					wg.Wait()
					close(done)
				}()
				select {
				case <-done:
					// success
				case <-time.After(1 * time.Second):
					t.Fatal("timed out waiting for launchSessionFn to be called")
				}

				mu.Lock()
				defer mu.Unlock()
				if !called {
					t.Error("expected launchSessionFn to be called, but it was not")
				}
				if gotAgent != tt.wantAgent {
					t.Errorf("launchSessionFn called with agent=%q, want %q", gotAgent, tt.wantAgent)
				}
				if gotTheme != tt.wantTheme {
					t.Errorf("launchSessionFn called with theme=%q, want %q", gotTheme, tt.wantTheme)
				}
				if gotSkipEd != tt.wantSkipEd {
					t.Errorf("launchSessionFn called with skipEditor=%v, want %v", gotSkipEd, tt.wantSkipEd)
				}
			} else {
				// Give goroutine a moment to (not) fire.
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				defer mu.Unlock()
				if called {
					t.Error("expected launchSessionFn NOT to be called, but it was")
				}
			}
		})
	}
}
