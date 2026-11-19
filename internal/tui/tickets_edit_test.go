package tui

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

var tkBaseUpdated = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

// editingModel is the detail view of #3 with an in-place edit started on
// field.
func editingModel(t *testing.T, field tkEditField) TicketsModel {
	t.Helper()
	m := tkMouseModel(t, 130, 40)
	d := *m.detailCache[3]
	d.body = "line one\nline two"
	d.updatedAt = tkBaseUpdated
	m.detail = &d
	m.viewMode = tkViewDetail
	next, _ := m.startEdit(field)
	return next.(TicketsModel)
}

// typeText sends each rune of s as a key press.
func tkType(t *testing.T, m TicketsModel, s string) TicketsModel {
	t.Helper()
	for _, r := range s {
		m = tkSendKey(t, m, charKey(r))
	}
	return m
}

// ghIssueJSON is a gh issue view reply.
func ghIssueJSON(title, body, state string, updated time.Time) []byte {
	b, _ := json.Marshal(map[string]any{
		"title": title, "body": body, "state": state, "updatedAt": updated,
		"url": "https://github.com/owner/repo/issues/3", "labels": []any{}, "assignees": []any{},
	})
	return b
}

// ghStub answers gh issue view with view and gh issue edit with editErr.
func ghStub(view []byte, viewErr, editErr error) func(string, ...string) ([]byte, error) {
	return func(name string, args ...string) ([]byte, error) {
		if name != "gh" || len(args) < 2 {
			return nil, errExecDisabled
		}
		switch args[0] + " " + args[1] {
		case "issue view":
			return view, viewErr
		case "issue edit":
			return nil, editErr
		}
		return nil, errExecDisabled
	}
}

// pump runs cmd and feeds every message back into the model, repeatedly.
func pump(t *testing.T, m TicketsModel, cmd tea.Cmd) TicketsModel {
	t.Helper()
	queue := runCmds(cmd)
	for i := 0; len(queue) > 0 && i < 20; i++ {
		msg := queue[0]
		queue = queue[1:]
		switch msg.(type) {
		case tkFlashExpireMsg, ticketsFetchedMsg:
			continue
		}
		next, c := m.Update(msg)
		m = next.(TicketsModel)
		queue = append(queue, runCmds(c)...)
	}
	return m
}

func ctrlS() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl} }

func TestEditLetterKeysAreDead(t *testing.T) {
	rec := recordExec(t, nil)
	m := editingModel(t, editTitle)
	m = tkType(t, m, "qdPtTyo?")
	if m.quitting || m.modalOpen() || m.viewMode != tkViewDetail {
		t.Fatalf("a letter key acted while editing (quit=%v modal=%v)", m.quitting, m.modalOpen())
	}
	if got := m.edit.title.Value(); !strings.HasSuffix(got, "qdPtTyo?") {
		t.Errorf("title = %q, want the letters typed into it", got)
	}
	if len(rec.all()) != 0 {
		t.Errorf("letters ran commands: %v", rec.all())
	}
}

func TestEditDirtyGuard(t *testing.T) {
	t.Run("clean esc leaves at once", func(t *testing.T) {
		m := editingModel(t, editBody)
		m = tkSendKey(t, m, tkKey("esc"))
		if m.edit.active || m.modalOpen() {
			t.Error("esc on an unchanged draft should just leave edit mode")
		}
	})
	t.Run("dirty esc asks, Keep is the default", func(t *testing.T) {
		m := editingModel(t, editBody)
		m = tkType(t, m, "x")
		m = tkSendKey(t, m, tkKey("esc"))
		c := choiceContent(t, m)
		if c.kind != "discard" || c.choices[c.focus].label != "Keep editing" {
			t.Fatalf("want the discard prompt focused on Keep editing, got %+v", c)
		}
		m = tkSendKey(t, m, tkKey("enter"))
		if !m.edit.active || !m.edit.dirty() {
			t.Fatal("Keep editing lost the draft")
		}
		m = tkSendKey(t, tkSendKey(t, m, tkKey("esc")), charKey('d'))
		if m.edit.active {
			t.Error("Discard should leave edit mode")
		}
	})
	t.Run("trailing whitespace is not a change", func(t *testing.T) {
		m := editingModel(t, editBody)
		m.edit.body.SetValue("line one   \nline two\n\n")
		if m.edit.dirty() {
			t.Error("trailing whitespace made the draft dirty")
		}
	})
	t.Run("click outside a dirty edit is ignored", func(t *testing.T) {
		m := editingModel(t, editBody)
		m = tkType(t, m, "x")
		m, _ = tkClickZone(t, m, "label:bug@detail")
		if !m.edit.active || m.viewMode != tkViewDetail || m.filterLabel != "" {
			t.Error("a stray click acted on a dirty draft")
		}
	})
	t.Run("click outside a clean edit acts", func(t *testing.T) {
		m := editingModel(t, editBody)
		m, _ = tkClickZone(t, m, "label:bug@detail")
		if m.edit.active || m.filterLabel != "bug" {
			t.Error("a click on a label should leave a clean edit and filter by it")
		}
	})
	t.Run("ctrl+c with a dirty draft asks", func(t *testing.T) {
		m := editingModel(t, editBody)
		m = tkType(t, m, "x")
		m = tkSendKey(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if m.quitting || choiceContent(t, m).kind != "quit" {
			t.Error("ctrl+c should ask before dropping a draft")
		}
	})
}

func TestEditEmptyTitleRefused(t *testing.T) {
	rec := recordExec(t, nil)
	m := editingModel(t, editTitle)
	m.edit.title.SetValue("   ")
	next, cmd := m.Update(ctrlS())
	m = next.(TicketsModel)
	runCmds(cmd)
	if !strings.Contains(m.edit.err, "empty") || m.edit.saving {
		t.Errorf("err = %q saving=%v, want the empty title refused", m.edit.err, m.edit.saving)
	}
	if len(rec.all()) != 0 {
		t.Errorf("an empty title reached gh: %v", rec.all())
	}
}

func TestEditSave(t *testing.T) {
	t.Run("success saves and refreshes", func(t *testing.T) {
		// GitHub returns the new title once the edit has gone through.
		title := "issue number 3"
		rec := recordExec(t, func(name string, args ...string) ([]byte, error) {
			if len(args) > 1 && args[0] == "issue" && args[1] == "edit" {
				for i, a := range args {
					if a == "--title" {
						title = args[i+1]
					}
				}
				return nil, nil
			}
			return ghIssueJSON(title, "line one\nline two", "OPEN", tkBaseUpdated), nil
		})
		m := editingModel(t, editTitle)
		m.edit.title.SetValue("renamed")
		next, cmd := m.Update(tkKey("enter"))
		m = pump(t, next.(TicketsModel), cmd)
		if m.edit.active {
			t.Fatalf("save should leave edit mode (err %q)", m.edit.err)
		}
		if !rec.called("gh issue edit 3 --repo owner/repo --title renamed") {
			t.Errorf("want gh issue edit --title, got %v", rec.all())
		}
		if rec.called("--body-file") {
			t.Error("an unchanged body must not be sent")
		}
		for _, iss := range m.allIssues {
			if iss.Number == 3 && iss.Title != "renamed" {
				t.Errorf("list row title = %q, want renamed", iss.Title)
			}
		}
		if !strings.Contains(m.flash, "saved #3") {
			t.Errorf("flash = %q", m.flash)
		}
	})
	t.Run("body goes through a body file", func(t *testing.T) {
		rec := recordExec(t, ghStub(ghIssueJSON("issue number 3", "line one\nline two", "OPEN", tkBaseUpdated), nil, nil))
		m := editingModel(t, editBody)
		m = tkType(t, m, "!")
		next, cmd := m.Update(ctrlS())
		m = pump(t, next.(TicketsModel), cmd)
		if m.edit.active || !rec.called("gh issue edit 3 --body-file") {
			t.Errorf("want a --body-file save, got %v (err %q)", rec.all(), m.edit.err)
		}
	})
	t.Run("failure keeps the draft", func(t *testing.T) {
		recordExec(t, ghStub(ghIssueJSON("issue number 3", "line one\nline two", "OPEN", tkBaseUpdated), nil,
			errors.New("GraphQL: Resource not accessible by integration (updateIssue)")))
		m := editingModel(t, editBody)
		m = tkType(t, m, "draft")
		draft := m.edit.body.Value()
		next, cmd := m.Update(ctrlS())
		m = pump(t, next.(TicketsModel), cmd)
		if !m.edit.active || m.edit.body.Value() != draft {
			t.Fatal("a failed save lost the draft")
		}
		if !strings.Contains(m.edit.err, "save failed: GraphQL: Resource not accessible") || !strings.Contains(m.edit.err, "ctrl+s to retry") {
			t.Errorf("err = %q", m.edit.err)
		}
	})
	t.Run("issue closed meanwhile keeps the draft", func(t *testing.T) {
		rec := recordExec(t, ghStub(ghIssueJSON("issue number 3", "line one\nline two", "CLOSED", tkBaseUpdated.Add(time.Minute)), nil, nil))
		m := editingModel(t, editBody)
		m = tkType(t, m, "x")
		next, cmd := m.Update(ctrlS())
		m = pump(t, next.(TicketsModel), cmd)
		if !m.edit.active || !strings.Contains(m.edit.err, "closed") || rec.called("issue edit") {
			t.Errorf("want the closed error and no save; err=%q calls=%v", m.edit.err, rec.all())
		}
	})
	t.Run("issue deleted meanwhile keeps the draft", func(t *testing.T) {
		recordExec(t, ghStub(nil, errors.New("GraphQL: Could not resolve to an issue or pull request with the number of 3."), nil))
		m := editingModel(t, editBody)
		m = tkType(t, m, "x")
		next, cmd := m.Update(ctrlS())
		m = pump(t, next.(TicketsModel), cmd)
		if !m.edit.active || !strings.Contains(m.edit.err, "Could not resolve") {
			t.Errorf("err = %q", m.edit.err)
		}
	})
	t.Run("no changes just leaves", func(t *testing.T) {
		rec := recordExec(t, nil)
		m := editingModel(t, editBody)
		next, cmd := m.Update(ctrlS())
		runCmds(cmd)
		if next.(TicketsModel).edit.active || len(rec.all()) != 0 {
			t.Error("saving an unchanged draft should just leave edit mode")
		}
	})
	t.Run("Save button", func(t *testing.T) {
		rec := recordExec(t, ghStub(ghIssueJSON("issue number 3", "line one\nline two", "OPEN", tkBaseUpdated), nil, nil))
		m := editingModel(t, editBody)
		m = tkType(t, m, "x")
		m, cmd := tkClickZone(t, m, "edit:save")
		m = pump(t, m, cmd)
		if m.edit.active || !rec.called("gh issue edit 3") {
			t.Error("[ Save ] should save like ctrl+s")
		}
	})
}

func TestEditStaleBlocksSave(t *testing.T) {
	remote := ghIssueJSON("issue number 3", "their new body", "OPEN", tkBaseUpdated.Add(5*time.Minute))
	start := func(t *testing.T) (TicketsModel, *execRecorder) {
		t.Helper()
		rec := recordExec(t, ghStub(remote, nil, nil))
		m := editingModel(t, editBody)
		m = tkType(t, m, "mine")
		next, cmd := m.Update(ctrlS())
		return pump(t, next.(TicketsModel), cmd), rec
	}

	t.Run("blocked with a choice, nothing written", func(t *testing.T) {
		m, rec := start(t)
		c := choiceContent(t, m)
		if c.kind != "stale" || c.choices[c.focus].label != "Keep editing" {
			t.Fatalf("want the stale prompt focused on Keep editing, got %+v", c)
		}
		if rec.called("issue edit") {
			t.Error("a stale draft was saved without asking")
		}
	})
	t.Run("keep editing keeps the draft", func(t *testing.T) {
		m, _ := start(t)
		m = tkSendKey(t, m, charKey('k'))
		if !m.edit.active || !strings.Contains(m.edit.body.Value(), "mine") || m.edit.saving {
			t.Error("Keep editing should return to the draft")
		}
	})
	t.Run("overwrite saves", func(t *testing.T) {
		m, rec := start(t)
		next, cmd := m.Update(charKey('o'))
		m = pump(t, next.(TicketsModel), cmd)
		if m.edit.active || !rec.called("gh issue edit 3 --body-file") {
			t.Errorf("Overwrite should save; calls %v", rec.all())
		}
	})
	t.Run("view theirs then back", func(t *testing.T) {
		m, _ := start(t)
		m = tkSendKey(t, m, charKey('v'))
		if _, ok := m.helpModal.Content.(tkTheirsModel); !ok || !m.helpModal.Visible {
			t.Fatal("View theirs should show the remote copy")
		}
		if !strings.Contains(m.View().Content, "their new body") {
			t.Error("their body is not shown")
		}
		m = tkSendKey(t, m, tkKey("esc"))
		if choiceContent(t, m).kind != "stale" {
			t.Error("closing their version should return to the choice")
		}
	})
	t.Run("a remote change to another field does not block", func(t *testing.T) {
		rec := recordExec(t, ghStub(ghIssueJSON("issue number 3", "line one\nline two", "OPEN", tkBaseUpdated.Add(time.Hour)), nil, nil))
		m := editingModel(t, editTitle)
		m.edit.title.SetValue("renamed")
		next, cmd := m.Update(ctrlS())
		m = pump(t, next.(TicketsModel), cmd)
		if m.choiceModal.Visible || !rec.called("issue edit 3") {
			t.Error("a label-only remote change must not block a title save")
		}
	})
}

func TestEditPasteGoesToFocusedField(t *testing.T) {
	m := editingModel(t, editTitle)
	m = tkSendMsg(t, m, tea.PasteMsg{Content: " pasted"})
	if !strings.HasSuffix(m.edit.title.Value(), " pasted") {
		t.Errorf("title = %q, want the paste", m.edit.title.Value())
	}
	m = tkSendKey(t, m, tkKey("tab"))
	m = tkSendMsg(t, m, tea.PasteMsg{Content: "PASTE"})
	if !strings.Contains(m.edit.body.Value(), "PASTE") {
		t.Errorf("body = %q, want the paste after tab", m.edit.body.Value())
	}
}

func TestEditEntryPoints(t *testing.T) {
	t.Run("pencil beside the body", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m.detail, m.viewMode = m.detailCache[3], tkViewDetail
		m, _ = tkClickZone(t, m, "edit:body@detail")
		if !m.edit.active || m.edit.field != editBody {
			t.Error("✎ should edit the body")
		}
	})
	t.Run("pencil beside the title", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m.detail, m.viewMode = m.detailCache[3], tkViewDetail
		m, _ = tkClickZone(t, m, "edit:title@detail")
		if !m.edit.active || m.edit.field != editTitle {
			t.Error("✎ should edit the title")
		}
	})
	t.Run("single click on the body does nothing", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m.detail, m.viewMode = m.detailCache[3], tkViewDetail
		m, _ = tkClickZone(t, m, "pane:body")
		if m.edit.active {
			t.Error("a single click on the body started an edit")
		}
	})
	t.Run("double click on the body edits", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m.detail, m.viewMode = m.detailCache[3], tkViewDetail
		m, _ = tkClickZone(t, m, "pane:body")
		m, _ = tkClickZone(t, m, "pane:body")
		if !m.edit.active || m.edit.field != editBody {
			t.Error("double-clicking the body should edit it")
		}
	})
	t.Run("double click on the title edits", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m.detail, m.viewMode = m.detailCache[3], tkViewDetail
		m, _ = tkClickZone(t, m, "dbl:title@detail")
		if m.edit.active {
			t.Fatal("a single click on the title started an edit")
		}
		m, _ = tkClickZone(t, m, "dbl:title@detail")
		if !m.edit.active || m.edit.field != editTitle {
			t.Error("double-clicking the title should edit it")
		}
	})
	t.Run("no repo slug disables editing", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m.repoSlug = ""
		m.detail, m.viewMode = m.detailCache[3], tkViewDetail
		m = tkSendKey(t, m, charKey('e'))
		if m.edit.active || !m.flashErr {
			t.Error("editing without a repo slug should be refused with a message")
		}
		m = tkSendKey(t, m, charKey('l'))
		if m.pickerModal.Visible {
			t.Error("the label picker should be disabled without a repo slug")
		}
	})
	t.Run("ctrl+e without EDITOR", func(t *testing.T) {
		prev := tkEditorCmd
		tkEditorCmd = func(string) *exec.Cmd { return nil }
		t.Cleanup(func() { tkEditorCmd = prev })
		m := editingModel(t, editBody)
		m = tkSendKey(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
		if !m.edit.active || !strings.Contains(m.flash, "$EDITOR") {
			t.Errorf("flash = %q, want a hint to set $EDITOR", m.flash)
		}
	})
}

func TestEditViewShowsModifiedPerField(t *testing.T) {
	m := editingModel(t, editBody)
	if strings.Contains(m.View().Content, "● modified") {
		t.Fatal("a clean draft shows ● modified")
	}
	m = tkType(t, m, "x")
	lines := strings.Split(m.View().Content, "\n")
	if strings.Contains(lines[0], "● modified") {
		t.Error("a body-only change marked the title")
	}
	if strings.Count(strings.Join(lines, "\n"), "● modified") != 1 {
		t.Error("a body change should show exactly one marker, on the body border")
	}
	if got := len(lines); got != m.termHeight {
		t.Errorf("edit frame is %d lines, want %d", got, m.termHeight)
	}

	m = editingModel(t, editTitle)
	m = tkType(t, m, "x")
	lines = strings.Split(m.View().Content, "\n")
	if !strings.Contains(lines[0], "● modified") || strings.Count(m.View().Content, "● modified") != 1 {
		t.Error("a title-only change should mark the title only")
	}
}

func TestPickers(t *testing.T) {
	t.Run("labels apply as add and remove", func(t *testing.T) {
		rec := recordExec(t, nil)
		m := tkMouseModel(t, 130, 40)
		m.labelOptions = []string{"bug", "enhancement"}
		m = tkSendKey(t, m, charKey('l'))
		p := m.pickerModal.Content.(tkPickerModel)
		if p.kind != pickLabels || p.number != 3 || !p.selected["bug"] {
			t.Fatalf("picker = %+v", p)
		}
		m = tkSendKey(t, m, tkKey("space"))   // untick bug (row 0)
		m = tkType(t, m, "enh")               // filter to enhancement
		m = tkSendKey(t, m, tkKey("space"))   // tick it
		next, cmd := m.Update(tkKey("enter")) // apply
		runCmds(cmd)
		if next.(TicketsModel).pickerModal.Visible {
			t.Error("enter should close the picker")
		}
		if !rec.called("gh issue edit 3 --repo owner/repo --add-label enhancement --remove-label bug") {
			t.Errorf("calls %v", rec.all())
		}
	})
	t.Run("click a row toggles it", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m.labelOptions = []string{"bug", "enhancement"}
		m = tkSendKey(t, m, charKey('l'))
		m, _ = tkClickZone(t, m, "mrow:1@picker")
		if p := m.pickerModal.Content.(tkPickerModel); !p.selected["enhancement"] || !m.pickerModal.Visible {
			t.Error("clicking a row should tick it and keep the picker open")
		}
	})
	t.Run("assignees", func(t *testing.T) {
		rec := recordExec(t, func(name string, args ...string) ([]byte, error) {
			if len(args) > 1 && args[0] == "repo" {
				return []byte(`{"assignableUsers":[{"login":"alice"},{"login":"bob"}]}`), nil
			}
			return nil, errExecDisabled
		})
		m := tkMouseModel(t, 130, 40)
		next, cmd := m.Update(charKey('a'))
		m = pump(t, next.(TicketsModel), cmd)
		p := m.pickerModal.Content.(tkPickerModel)
		if !containsString(p.options, "bob") {
			t.Fatalf("options = %v, want the assignable users", p.options)
		}
		m = tkType(t, m, "bob")
		m = tkSendKey(t, m, tkKey("space"))
		_, cmd = m.Update(tkKey("enter"))
		runCmds(cmd)
		if !rec.called("gh issue edit 3 --repo owner/repo --add-assignee bob") {
			t.Errorf("calls %v", rec.all())
		}
	})
	t.Run("filter picker sets the label filter", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('f'))
		m, _ = tkClickZone(t, m, "mrow:1@picker") // row 0 is "any label"
		if m.pickerModal.Visible || m.filterLabel != "bug" {
			t.Errorf("filter = %q, want bug", m.filterLabel)
		}
	})
	t.Run("esc cancels without applying", func(t *testing.T) {
		rec := recordExec(t, nil)
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('l'))
		m = tkSendKey(t, m, tkKey("space"))
		_, cmd := m.Update(tkKey("esc"))
		runCmds(cmd)
		if rec.called("issue edit") {
			t.Error("esc applied the picker")
		}
	})
}

func TestNormBody(t *testing.T) {
	tests := []struct{ a, b string }{
		{"x", "x\n"},
		{"x\ny", "x  \ny\t\n\n"},
		{"x\r\ny", "x\ny"},
	}
	for _, tt := range tests {
		if normBody(tt.a) != normBody(tt.b) {
			t.Errorf("normBody(%q) != normBody(%q)", tt.a, tt.b)
		}
	}
	if normBody("x") == normBody("y") {
		t.Error("different bodies compare equal")
	}
}

// TestCloseFinishingMidEditKeepsDraft is the regression for a close that
// lands after an edit started: the draft stays, and later opens still work.
func TestCloseFinishingMidEditKeepsDraft(t *testing.T) {
	m := editingModel(t, editBody)
	m = tkType(t, m, "x")
	m = tkSendMsg(t, m, tkIssueClosedMsg{number: 3})
	if m.viewMode != tkViewDetail || !m.edit.active || !m.edit.dirty() {
		t.Fatalf("the close dropped the draft (view=%d active=%v)", m.viewMode, m.edit.active)
	}
	if !strings.Contains(m.edit.err, "closed") {
		t.Errorf("err = %q, want the closed notice", m.edit.err)
	}

	// Leaving the detail ends the edit, so another issue can open.
	m = tkSendKey(t, m, tkKey("esc"))
	m = tkSendKey(t, m, charKey('d')) // discard
	m = tkSendKey(t, m, tkKey("esc")) // back to the list
	m = tkSendMsg(t, m, tkDetailFetchedMsg{number: 2, title: "two"})
	if m.detail == nil || m.detail.number != 2 {
		t.Error("a detail could not be opened after the edit")
	}
}

// TestOrphanedEditNeverBlocksOpens covers an edit left behind by any path
// out of the detail view.
func TestOrphanedEditNeverBlocksOpens(t *testing.T) {
	m := editingModel(t, editBody)
	m = tkType(t, m, "x")
	m.viewMode = tkViewList // as if something left the detail without ending the edit
	m = tkSendMsg(t, m, tkDetailFetchedMsg{number: 2, title: "two"})
	if m.detail == nil || m.detail.number != 2 || m.edit.active {
		t.Error("an edit outside the detail view blocked opening an issue")
	}
}
