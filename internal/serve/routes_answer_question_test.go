package serve

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/panestate"
)

// readServeCapture loads a real AskUserQuestion capture shared with the
// servetest fixtures.
func readServeCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("servetest", "panes", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parseCapture(t *testing.T, pane string) panestate.Dialog {
	t.Helper()
	d, ok := panestate.ParseDialog(pane)
	if !ok {
		t.Fatal("capture does not parse")
	}
	return d
}

// toggleBox does what Claude Code does when a digit is pressed on a
// multi-select tab: it flips that option's checkbox.
func toggleBox(pane, key string) string {
	lines := strings.Split(pane, "\n")
	for i, l := range lines {
		plain := strings.TrimLeft(panestate.StripANSI(l), " ❯")
		if !strings.HasPrefix(plain, key+". [") {
			continue
		}
		if strings.Contains(l, "[ ]") {
			lines[i] = strings.Replace(l, "[ ]", "[✔]", 1)
		} else {
			lines[i] = strings.Replace(l, "[✔]", "[ ]", 1)
		}
	}
	return strings.Join(lines, "\n")
}

// multiEnv is a write env showing the multi-select capture, with a pane
// that reacts to keys like Claude Code: digits toggle (when react says so),
// Tab moves to the review tab.
func multiEnv(t *testing.T, pane string, react bool) *writeEnv {
	t.Helper()
	e := newWriteEnv(t, true)
	review := readServeCapture(t, "askuser-review.txt")
	e.panes.set(pane)
	e.keys.onSend = func(key string) {
		e.panes.mu.Lock()
		defer e.panes.mu.Unlock()
		switch {
		case key == "Tab":
			e.panes.text = review
		case react:
			e.panes.text = toggleBox(e.panes.text, key)
		}
	}
	return e
}

func sentKeys(e *writeEnv) []string {
	var out []string
	for _, k := range e.keys.all() {
		out = append(out, k.key)
	}
	return out
}

func TestAnswerMultiSelect(t *testing.T) {
	q2 := readServeCapture(t, "askuser-q2.txt")
	preTicked := toggleBox(q2, "2")
	tests := []struct {
		name   string
		pane   string
		react  bool
		body   func(hash string) string
		status int
		code   string
		sent   []string
	}{
		{
			name: "ticks each key then tabs", pane: q2, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"keys":["1","3"],"dialog_hash":%q}`, h) },
			status: 200, sent: []string{"1", "3", "Tab"},
		},
		{
			name: "unticks what was not chosen", pane: preTicked, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"keys":["1"],"dialog_hash":%q}`, h) },
			status: 200, sent: []string{"1", "2", "Tab"},
		},
		{
			name: "already as wanted: only tab", pane: preTicked, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"keys":["2"],"dialog_hash":%q}`, h) },
			status: 200, sent: []string{"Tab"},
		},
		{
			// The re-parse after the first key shows no change: stop there.
			name: "toggle not taken stops", pane: q2, react: false,
			body:   func(h string) string { return fmt.Sprintf(`{"keys":["1","3"],"dialog_hash":%q}`, h) },
			status: 409, code: codeDialogChanged, sent: []string{"1"},
		},
		{
			name: "single key refused", pane: q2, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, h) },
			status: 409, code: codeDialogChanged,
		},
		{
			name: "free-text option refused", pane: q2, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"keys":["4"],"dialog_hash":%q}`, h) },
			status: 409, code: codeDialogChanged,
		},
		{
			name: "esc dismisses", pane: q2, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"key":"esc","dialog_hash":%q}`, h) },
			status: 200, sent: []string{"Escape"},
		},
		{
			name: "stale hash", pane: preTicked, react: true,
			body:   func(string) string { return fmt.Sprintf(`{"keys":["1"],"dialog_hash":%q}`, parseCapture(t, q2).Hash) },
			status: 409, code: codeDialogChanged,
		},
		{
			name: "key and keys", pane: q2, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"key":"1","keys":["1"],"dialog_hash":%q}`, h) },
			status: 400, code: codeBadRequest,
		},
		{
			name: "duplicate keys", pane: q2, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"keys":["1","1"],"dialog_hash":%q}`, h) },
			status: 400, code: codeBadRequest,
		},
		{
			name: "keys on a permission dialog", pane: dialogPane, react: true,
			body:   func(h string) string { return fmt.Sprintf(`{"keys":["1"],"dialog_hash":%q}`, h) },
			status: 409, code: codeDialogChanged,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := multiEnv(t, tt.pane, tt.react)
			w := e.post(winPath+"/answer", tt.body(parseCapture(t, tt.pane).Hash))
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if tt.code != "" && errCode(t, w) != tt.code {
				t.Errorf("code %s, want %s", errCode(t, w), tt.code)
			}
			if got := sentKeys(e); !slices.Equal(got, tt.sent) {
				t.Errorf("sent %v, want %v", got, tt.sent)
			}
		})
	}
}

// A single-select AskUserQuestion tab is answered with one digit, which
// Claude Code takes as pick-and-advance. Its free-text entries are not
// offered.
func TestAnswerSingleSelectQuestion(t *testing.T) {
	q1 := readServeCapture(t, "askuser-q1.txt")
	d := parseCapture(t, q1)
	if d.Kind != panestate.KindQuestion {
		t.Fatalf("kind = %q", d.Kind)
	}
	for _, tc := range []struct {
		key    string
		status int
	}{{"2", 200}, {"4", 409}, {"5", 409}} {
		e := newWriteEnv(t, true)
		e.panes.set(q1)
		w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":%q,"dialog_hash":%q}`, tc.key, d.Hash))
		if w.Code != tc.status {
			t.Errorf("key %s: status %d: %s", tc.key, w.Code, w.Body)
		}
		if tc.status == http.StatusOK && !slices.Equal(sentKeys(e), []string{tc.key}) {
			t.Errorf("key %s: sent %v", tc.key, sentKeys(e))
		}
	}
}

// The pane endpoint reports AskUserQuestion screens as awaiting input with a
// dialog, including the footer-less review tab.
func TestPaneAskUserQuestion(t *testing.T) {
	for _, name := range []string{"askuser-q1.txt", "askuser-q2.txt", "askuser-review.txt"} {
		body := buildPaneBody(readServeCapture(t, name))
		if body.Verdict != "awaiting_input" || body.Dialog == nil {
			t.Errorf("%s: verdict %s dialog %v", name, body.Verdict, body.Dialog)
		}
	}
}
