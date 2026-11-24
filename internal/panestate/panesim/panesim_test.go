package panesim

import (
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/panestate"
)

func TestPressDrivesSubmitRow(t *testing.T) {
	b, err := os.ReadFile("../testdata/askuser-q2-live.txt")
	if err != nil {
		t.Fatal(err)
	}
	pane := string(b)
	pane, _ = Press(pane, "2")
	d, _ := panestate.ParseDialog(pane)
	if !panestate.Checked(d.Options[1].Label) || d.Cursor != 0 {
		t.Fatalf("after 2: %+v cursor %d", d.Options, d.Cursor)
	}
	for range d.Submit.Moves {
		pane, _ = Press(pane, "Down")
	}
	d, _ = panestate.ParseDialog(pane)
	if d.Submit == nil || d.Submit.Moves != 0 {
		t.Fatalf("after moves: submit %+v cursor %d", d.Submit, d.Cursor)
	}
	if _, submitted := Press(pane, "Enter"); !submitted {
		t.Error("Enter on the Submit row did not submit")
	}
	pane, _ = Press(pane, "Up")
	if _, submitted := Press(pane, "Enter"); submitted {
		t.Error("Enter off the Submit row submitted")
	}
}

func TestPressTrust(t *testing.T) {
	pane := "Accessing workspace:\n\n/tmp/x\n\nQuick safety check: Is this a project you created or one you trust?\n\n" +
		" ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel\n"
	cursor := func(p string) int {
		d, ok := panestate.ParseTrustDialog(p)
		if !ok {
			t.Fatalf("not a trust dialog:\n%s", p)
		}
		return d.Cursor
	}
	down, _ := PressTrust(pane, "Down")
	up, _ := PressTrust(pane, "Up")
	if cursor(pane) != 0 || cursor(down) != 1 || cursor(up) != 1 {
		t.Errorf("cursor %d, after Down %d, after Up %d", cursor(pane), cursor(down), cursor(up))
	}
	if _, a := PressTrust(pane, "Enter"); a != panestate.TrustKeyExit {
		t.Errorf("Enter on No = %q", a)
	}
	if _, a := PressTrust(down, "Enter"); a != panestate.TrustKeyTrust {
		t.Errorf("Enter on Yes = %q", a)
	}
	if p, a := PressTrust(pane, "x"); a != "" || p != pane {
		t.Errorf("other key changed the pane")
	}
}
