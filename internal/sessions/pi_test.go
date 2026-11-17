package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// installPiSession copies the captured pi session (internal/transcript
// testdata) into a fake pi sessions root and returns the root.
func installPiSession(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../transcript/testdata/pi-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "--home-user-agentctl-pi-e2e-proj--")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "2026-11-17T20-08-01-304Z_241cdf3b-204d-43dd-aeaf-a29e54628c45.jsonl"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// An untagged pi session (not launched by agentctl) is ignored.
	if err := os.WriteFile(filepath.Join(dir, "2026-11-17T20-00-00-000Z_other.jsonl"), []byte(`{"type":"session","version":3,"id":"other","timestamp":"2026-11-17T20:00:00Z","cwd":"/x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	SetClaudeDirForTest(t.TempDir())
	SetPiSessionsDirForTest(root)
	t.Cleanup(func() { SetClaudeDirForTest(""); SetPiSessionsDirForTest("") })
	return root
}

func TestFindRecentSessionsIncludesPi(t *testing.T) {
	installPiSession(t)
	got, err := FindRecentSessions(0, 100*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions = %+v, want the one tagged pi session", got)
	}
	s := got[0]
	if s.ID != "241cdf3b-204d-43dd-aeaf-a29e54628c45" || s.Agent != "pi-tester" || s.Harness != "pi" || NormalizeProjectName(s.Project) != "proj" {
		t.Errorf("session = %+v (project %q)", s, NormalizeProjectName(s.Project))
	}
	if s.StartTime.IsZero() || !s.EndTime.After(s.StartTime) {
		t.Errorf("time range = %v .. %v", s.StartTime, s.EndTime)
	}
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalOutputTokens != 347 || sum.ModelName != "gemma4:latest" || len(sum.UserMsgs) == 0 || len(sum.ToolCalls) == 0 {
		t.Errorf("summary: output=%d model=%q users=%d tools=%d", sum.TotalOutputTokens, sum.ModelName, len(sum.UserMsgs), len(sum.ToolCalls))
	}
	if sum.ToolCalls[0].Name != "Bash" {
		t.Errorf("tool name = %q, want Bash", sum.ToolCalls[0].Name)
	}
}

func TestHarnessOf(t *testing.T) {
	installPiSession(t)
	if got := HarnessOf("241cdf3b-204d-43dd-aeaf-a29e54628c45"); got != "pi" {
		t.Errorf("HarnessOf(pi session) = %q", got)
	}
	if got := HarnessOf("unknown-session"); got != "claude" {
		t.Errorf("HarnessOf(unknown) = %q", got)
	}
}

func TestBillingFor(t *testing.T) {
	SetSessionDataDirForTest(t.TempDir())
	t.Cleanup(func() { SetSessionDataDirForTest("") })
	tests := []struct {
		name  string
		s     Session
		model string
		want  Billing
	}{
		{"claude on claude is the subscription", Session{ID: "a"}, "claude-sonnet-4-5", BillingSub},
		{"claude on pi is metered", Session{ID: "b", Harness: "pi"}, "claude-sonnet-4-5", BillingMetered},
		{"unpriced local model on pi stays unknown", Session{ID: "c", Harness: "pi"}, "gemma4:latest", BillingUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BillingFor(tt.s, tt.model, nil); got != tt.want {
				t.Errorf("BillingFor = %q, want %q", got, tt.want)
			}
		})
	}
}
