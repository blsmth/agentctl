package tmux

import (
	"errors"
	"testing"
)

// TestSessionNameForPane exercises the query seam (queryPaneSessionName)
// rather than shelling out to a real tmux server.
func TestSessionNameForPane(t *testing.T) {
	origFn := queryPaneSessionName
	t.Cleanup(func() { queryPaneSessionName = origFn })

	t.Run("empty pane short-circuits without querying", func(t *testing.T) {
		called := false
		queryPaneSessionName = func(pane string) (string, error) {
			called = true
			return "should-not-be-used", nil
		}
		if got := SessionNameForPane(""); got != "" {
			t.Errorf("SessionNameForPane(\"\") = %q, want \"\"", got)
		}
		if called {
			t.Error("queryPaneSessionName should not be called for an empty pane")
		}
	})

	t.Run("query succeeds", func(t *testing.T) {
		queryPaneSessionName = func(pane string) (string, error) {
			if pane != "%12" {
				t.Errorf("query called with pane %q, want %%12", pane)
			}
			return "agentctl-acme", nil
		}
		if got := SessionNameForPane("%12"); got != "agentctl-acme" {
			t.Errorf("SessionNameForPane(%%12) = %q, want agentctl-acme", got)
		}
	})

	t.Run("query fails returns empty", func(t *testing.T) {
		queryPaneSessionName = func(pane string) (string, error) {
			return "", errors.New("no server running on socket")
		}
		if got := SessionNameForPane("%12"); got != "" {
			t.Errorf("SessionNameForPane(%%12) = %q, want \"\" on query failure", got)
		}
	})
}
