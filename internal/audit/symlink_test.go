package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppend_RefusesSymlinks(t *testing.T) {
	t.Run("audit dir is a symlink", func(t *testing.T) {
		project := setupAuditTest(t)
		target := t.TempDir()
		rt := filepath.Join(project, ".agentctl")
		if err := os.MkdirAll(rt, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(rt, "audit")); err != nil {
			t.Fatal(err)
		}
		if err := Append(Record{Session: "s1", Tool: "Bash"}); err == nil {
			t.Error("Append wrote through a symlinked audit dir")
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("symlink target written: %d entries", len(entries))
		}
	})
	t.Run("audit file is a symlink", func(t *testing.T) {
		project := setupAuditTest(t)
		auditDir := filepath.Join(project, ".agentctl", "audit")
		if err := os.MkdirAll(auditDir, 0o700); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(auditDir, "s1.jsonl")); err != nil {
			t.Fatal(err)
		}
		if err := Append(Record{Session: "s1", Tool: "Bash"}); err == nil {
			t.Error("Append wrote through a symlinked audit file")
		}
		if got, _ := os.ReadFile(victim); string(got) != "original" {
			t.Errorf("victim modified: %q", got)
		}
	})
}
