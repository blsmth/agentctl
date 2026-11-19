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
