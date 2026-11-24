package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/panestate/panesim"
)

// trustCapture loads a real folder-trust dialog capture (Claude Code
// 2.1.328, a scratch tmux server) from panestate's testdata.
func trustCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "panestate", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const trustExitedPane = "\n Claude Code didn't start because this folder isn't trusted.\nEXITED-1\n"

// reactLikeTrustDialog makes e's pane behave as the scratch run showed the
// trust dialog does: Down/Up move the ❯ between the two rows (wrapping),
// Enter on "Yes" starts Claude Code (idle pane), Enter on "No, exit" exits,
// and Escape exits too. Keys in drop leave the pane as it was.
func reactLikeTrustDialog(e *writeEnv, drop ...string) {
	e.keys.onSend = func(key string) {
		for _, d := range drop {
			if d == key {
				return
			}
		}
		e.panes.mu.Lock()
		defer e.panes.mu.Unlock()
		e.panes.text = pressTrust(e.panes.text, key)
	}
}

// pressTrust is the pane after key, with Claude Code's next screen once the
// dialog is answered: idle after trust, the exit message after exit.
func pressTrust(pane, key string) string {
	next, answered := panesim.PressTrust(pane, key)
	switch answered {
	case panestate.TrustKeyTrust:
		return idlePane
	case panestate.TrustKeyExit:
		return pane + trustExitedPane
	}
	return next
}

func trustHash(t *testing.T, pane string) string {
	t.Helper()
	d, ok := panestate.ParseTrustDialog(pane)
	if !ok {
		t.Fatal("fixture trust dialog does not parse")
	}
	return d.Hash
}

func TestPaneExposesTrustDialog(t *testing.T) {
	e := newWriteEnv(t, true)
	e.panes.set(trustCapture(t, "trust-scratch-100.txt"))
	w := e.get(winPath + "/pane")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var body struct {
		Verdict string `json:"verdict"`
		Dialog  *struct {
			Kind    string             `json:"kind"`
			Folder  string             `json:"folder"`
			Hash    string             `json:"hash"`
			Options []panestate.Option `json:"options"`
		} `json:"dialog"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Verdict != "awaiting_input" || body.Dialog == nil {
		t.Fatalf("verdict %q dialog %v", body.Verdict, body.Dialog)
	}
	d := body.Dialog
	keys := []string{d.Options[0].Key, d.Options[1].Key}
	if d.Kind != "trust" || d.Folder != "/private/tmp/trusttest.R15FAO" || len(d.Hash) != 64 || !reflect.DeepEqual(keys, []string{"exit", "trust"}) {
		t.Errorf("dialog = %+v", d)
	}
}

// The older numbered layout is still the trust dialog, not a numbered one,
// and a pane where the dialog lingers above Claude Code's exit message has
// no dialog to answer.
func TestPaneTrustDialogVariants(t *testing.T) {
	numbered := "Accessing workspace:\n\n/tmp/repo\n\nIs this a project you created or one you trust?\n\n❯ 1. Yes, I trust this folder\n  2. No, exit\n\nEnter to confirm · Esc to cancel\n"
	if b := buildPaneBody(numbered); b.Dialog == nil || b.Dialog.Kind != panestate.KindTrust || b.Dialog.Options[0].Key != "trust" {
		t.Errorf("numbered layout: %+v", b.Dialog)
	}
	if b := buildPaneBody(trustCapture(t, "trust-scratch-60-exited.txt")); b.Dialog != nil {
		t.Errorf("exited pane: dialog %+v, want none", b.Dialog)
	}
}

func TestAnswerTrustDialog(t *testing.T) {
	onExit := trustCapture(t, "trust-scratch-100.txt")
	onTrust := pressTrust(onExit, "Down")
	if d, _ := panestate.ParseTrustDialog(onTrust); d.Cursor != 1 {
		t.Fatalf("simulated Down: cursor %d", d.Cursor)
	}
	tests := []struct {
		name  string
		pane  string
		key   string
		code  int
		keys  []string
		after string
	}{
		{"trust, cursor on exit", onExit, "trust", 200, []string{"Down", "Enter"}, idlePane},
		{"trust, cursor already on trust", onTrust, "trust", 200, []string{"Enter"}, idlePane},
		{"exit, cursor on exit", onExit, "exit", 200, []string{"Enter"}, ""},
		{"exit, cursor on trust", onTrust, "exit", 200, []string{"Up", "Enter"}, ""},
		{"esc refused", onExit, "esc", 409, nil, ""},
		{"number key refused", onExit, "1", 409, nil, ""},
		{"submit refused", onExit, "submit", 409, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.panes.set(tt.pane)
			reactLikeTrustDialog(e)
			w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":%q,"dialog_hash":%q}`, tt.key, trustHash(t, tt.pane)))
			if got := sentKeys(e); w.Code != tt.code || !reflect.DeepEqual(got, tt.keys) {
				t.Fatalf("status %d sent %v, want %d %v: %s", w.Code, got, tt.code, tt.keys, w.Body)
			}
			if tt.after != "" && e.panes.text != tt.after {
				t.Errorf("pane after answer:\n%s", e.panes.text)
			}
			if tt.code == 200 && tt.key == "exit" && !strings.Contains(e.panes.text, "didn't start") {
				t.Errorf("exit did not exit:\n%s", e.panes.text)
			}
		})
	}
}

// Enter is never sent unless the cursor is seen on the chosen row: a dropped
// arrow, or a different screen after it, stops the answer before Enter.
func TestAnswerTrustStopsBeforeEnter(t *testing.T) {
	pane := trustCapture(t, "trust-scratch-100.txt")
	body := fmt.Sprintf(`{"key":"trust","dialog_hash":%q}`, trustHash(t, pane))

	e := newWriteEnv(t, true)
	e.panes.set(pane)
	reactLikeTrustDialog(e, "Down")
	w := e.post(winPath+"/answer", body)
	if got := sentKeys(e); w.Code != http.StatusConflict || errCode(t, w) != codeDialogChanged || !reflect.DeepEqual(got, []string{"Down"}) {
		t.Errorf("dropped Down: status %d sent %v: %s", w.Code, got, w.Body)
	}

	e = newWriteEnv(t, true)
	e.panes.set(pane)
	e.keys.onSend = func(string) { e.panes.set(idlePane) }
	w = e.post(winPath+"/answer", body)
	if got := sentKeys(e); w.Code != http.StatusConflict || !reflect.DeepEqual(got, []string{"Down"}) {
		t.Errorf("screen changed: status %d sent %v: %s", w.Code, got, w.Body)
	}

	// A stale hash sends nothing.
	e = newWriteEnv(t, true)
	e.panes.set(pane)
	w = e.post(winPath+"/answer", fmt.Sprintf(`{"key":"trust","dialog_hash":%q}`, strings.Repeat("0", 64)))
	if w.Code != http.StatusConflict || len(e.keys.all()) != 0 {
		t.Errorf("stale hash: status %d sent %v", w.Code, e.keys.all())
	}
}

// A second tap while the answered trust dialog is still on screen is refused.
func TestAnswerTrustRepeatRefused(t *testing.T) {
	pane := trustCapture(t, "trust-scratch-100.txt")
	e := newWriteEnv(t, true)
	e.panes.set(pane)
	reactLikeTrustDialog(e, "Enter") // the redraw has not happened yet
	body := fmt.Sprintf(`{"key":"exit","dialog_hash":%q}`, trustHash(t, pane))
	if w := e.post(winPath+"/answer", body); w.Code != 200 {
		t.Fatalf("first = %d %s", w.Code, w.Body)
	}
	w := e.post(winPath+"/answer", body)
	if w.Code != http.StatusConflict || errCode(t, w) != codeAlreadyAnswered {
		t.Fatalf("repeat = %d %s", w.Code, w.Body)
	}
	if got := sentKeys(e); !reflect.DeepEqual(got, []string{"Enter"}) {
		t.Errorf("sent %v", got)
	}
	if lines := e.auditLines(t); len(lines) < 2 {
		t.Errorf("audit = %+v", lines)
	}
}

// Trust keys on an ordinary permission dialog are not options of it.
func TestAnswerTrustKeyOnPermissionDialog(t *testing.T) {
	e := newWriteEnv(t, true)
	w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":"trust","dialog_hash":%q}`, dialogHash(t)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	e.assertNothingSent(t)
}

// A trust dialog quoted in scrollback (an agent cat-ing a capture) must not
// hide a live permission dialog below it; the older numbered trust layout
// left on screen after Claude Code exited gets no dialog at all.
func TestPaneTrustDialogInScrollback(t *testing.T) {
	quoted := strings.Split(trustCapture(t, "trust-scratch-60.txt"), "\n")
	var b strings.Builder
	for _, l := range quoted[:min(20, len(quoted))] {
		b.WriteString("     " + l + "\n")
	}
	pane := b.String() + dialogPane
	want := buildPaneBody(dialogPane).Dialog
	got := buildPaneBody(pane)
	if got.Verdict != "awaiting_input" || got.Dialog == nil || want == nil || got.Dialog.Kind != "" || got.Dialog.Options[0].Key != "1" {
		t.Fatalf("verdict %q dialog %+v, want the permission dialog", got.Verdict, got.Dialog)
	}

	exitedNumbered := "Accessing workspace:\n\n/tmp/repo\n\nIs this a project you created or one you trust?\n\n" +
		"❯ 1. Yes, I trust this folder\n  2. No, exit\n\nEnter to confirm · Esc to cancel\n" +
		"Claude Code didn't start because this folder isn't trusted.\n$ \n"
	if b := buildPaneBody(exitedNumbered); b.Dialog != nil {
		t.Errorf("exited numbered layout: dialog %+v, want none", b.Dialog)
	}
}
