package paths

import "testing"

func TestBindRestoresAndBlocksInit(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")

	restore := Bind("/a/.agentctl")
	if got := RuntimeDir(); got != "/a/.agentctl" {
		t.Fatalf("RuntimeDir = %q, want /a/.agentctl", got)
	}
	Init(t.TempDir()) // once already consumed: must not override the binding
	if got := RuntimeDir(); got != "/a/.agentctl" {
		t.Fatalf("Init overrode binding: RuntimeDir = %q", got)
	}

	inner := Bind("/b/.agentctl")
	if got := RuntimeDir(); got != "/b/.agentctl" {
		t.Fatalf("nested RuntimeDir = %q", got)
	}
	inner()
	if got := RuntimeDir(); got != "/a/.agentctl" {
		t.Fatalf("after inner restore RuntimeDir = %q", got)
	}
	restore()
	if got := RuntimeDir(); got != "" {
		t.Fatalf("after restore RuntimeDir = %q, want empty", got)
	}
}
