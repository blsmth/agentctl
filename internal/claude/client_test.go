package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain handles the "helper subprocess" pattern: when the test binary is
// invoked with GO_HELPER_PROCESS=1 it acts as a mock Claude CLI subprocess
// instead of running tests. This lets TestClient_* use exec.Command pointing
// at the test binary itself as a controllable fake.
func TestMain(m *testing.M) {
	if os.Getenv("GO_HELPER_PROCESS") == "1" {
		runHelperProcess()
		return
	}
	os.Exit(m.Run())
}

// runHelperProcess implements the mock Claude CLI subprocess.
// It writes the init message then loops reading user messages and writing
// canned responses, matching the --input-format/--output-format stream-json wire
// protocol expected by the Client.
func runHelperProcess() {
	mode := os.Getenv("GO_HELPER_MODE")

	// Always emit the init message first.
	fmt.Printf(`{"type":"system","subtype":"init","session_id":"test-session"}` + "\n")

	switch mode {
	case "die_after_init":
		// Exit immediately after init — simulates a crash before first response.
		os.Exit(1)

	case "rate_limit":
		// Respond with a rate_limit_event then a result.
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			rl := `{"type":"rate_limit_event","rate_limit_info":{"status":"ok","resetsAt":9999999999,"rateLimitType":"message"}}`
			result := `{"type":"result","subtype":"success","result":"pong"}`
			fmt.Println(rl)
			fmt.Println(result)
		}

	default:
		// Default: echo mode — for each user message, return the content as result.
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			var msg userMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				continue
			}
			result := fmt.Sprintf(`{"type":"result","subtype":"success","result":%s}`,
				jsonString("echo: "+msg.Message.Content))
			fmt.Println(result)
		}
	}
}

// jsonString returns a JSON-encoded string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// helperCmd returns an exec.Cmd that re-invokes the current test binary as a
// mock subprocess with the given mode.
func helperCmd(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestMain")
	cmd.Env = append(os.Environ(),
		"GO_HELPER_PROCESS=1",
		"GO_HELPER_MODE="+mode,
	)
	return cmd
}

// newClientWithHelper creates a Client that uses the mock subprocess.
func newClientWithHelper(t *testing.T, mode string) *Client {
	t.Helper()
	c := &Client{
		model:    defaultModel,
		newCmdFn: func(_ string) *exec.Cmd { return helperCmd(mode) },
	}
	if err := c.start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// ---------------------------------------------------------------------------
// Protocol tests
// ---------------------------------------------------------------------------

func TestProtocol_WriteUserMessage(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUserMessage(&buf, "hello world"); err != nil {
		t.Fatalf("writeUserMessage: %v", err)
	}

	// Must be valid JSON.
	var msg userMessage
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "user" {
		t.Errorf("type: got %q, want %q", msg.Type, "user")
	}
	if msg.Message.Role != "user" {
		t.Errorf("role: got %q, want %q", msg.Message.Role, "user")
	}
	if msg.Message.Content != "hello world" {
		t.Errorf("content: got %q, want %q", msg.Message.Content, "hello world")
	}

	// Must be terminated with newline (NDJSON).
	raw := buf.String()
	if !strings.HasSuffix(raw, "\n") {
		t.Errorf("expected newline terminator, got %q", raw)
	}
}

func TestProtocol_ReadUntilResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"abc"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"ok","resetsAt":1234567890,"rateLimitType":"message"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"result","subtype":"success","result":"hello"}`,
	}
	input := strings.Join(lines, "\n") + "\n"
	scanner := bufio.NewScanner(strings.NewReader(input))

	msgs, err := readUntilResult(scanner)
	if err != nil {
		t.Fatalf("readUntilResult: %v", err)
	}

	// Should return 4 messages (all lines up to and including result).
	if len(msgs) != 4 {
		t.Errorf("message count: got %d, want 4", len(msgs))
	}

	// Last message must be the result.
	last := msgs[len(msgs)-1]
	if last.Type != "result" || last.Subtype != "success" {
		t.Errorf("last message: got type=%q subtype=%q, want result/success", last.Type, last.Subtype)
	}
	if last.Result != "hello" {
		t.Errorf("result text: got %q, want %q", last.Result, "hello")
	}

	// Rate limit info should be present.
	var rl *RateLimitInfo
	for _, m := range msgs {
		if m.RateLimitInfo != nil {
			rl = m.RateLimitInfo
		}
	}
	if rl == nil {
		t.Fatal("expected rate_limit_info in messages")
	}
	if rl.Status != "ok" {
		t.Errorf("rate limit status: got %q, want %q", rl.Status, "ok")
	}
	if rl.ResetsAt != 1234567890 {
		t.Errorf("resetsAt: got %d, want 1234567890", rl.ResetsAt)
	}
}

func TestProtocol_ReadUntilResult_NoResult(t *testing.T) {
	input := `{"type":"assistant","message":{"role":"assistant","content":[]}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(input))
	_, err := readUntilResult(scanner)
	if err == nil {
		t.Fatal("expected error for stream with no result message")
	}
}

// ---------------------------------------------------------------------------
// Client tests using mock subprocess
// ---------------------------------------------------------------------------

func TestClient_Complete(t *testing.T) {
	c := newClientWithHelper(t, "echo")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := c.Complete(ctx, "ping")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result != "echo: ping" {
		t.Errorf("result: got %q, want %q", result, "echo: ping")
	}
}

func TestClient_CompleteMultiple(t *testing.T) {
	c := newClientWithHelper(t, "echo")
	ctx := context.Background()

	for i, want := range []string{"hello", "world", "foo"} {
		got, err := c.Complete(ctx, want)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if got != "echo: "+want {
			t.Errorf("call %d: got %q, want %q", i, got, "echo: "+want)
		}
	}
}

func TestClient_RestartOnDeath(t *testing.T) {
	// First subprocess: die immediately after init.
	// Client.start() should succeed (we get past init).
	// First Complete() should fail (no response), trigger restart with "echo" mode.
	// But since we always use the same helperCmd("die_after_init"), we can't
	// switch modes mid-test. Instead, use a factory that returns different cmds.
	callCount := 0
	c := &Client{
		model: defaultModel,
		newCmdFn: func(_ string) *exec.Cmd {
			callCount++
			if callCount == 1 {
				return helperCmd("die_after_init")
			}
			return helperCmd("echo")
		},
	}
	if err := c.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First call will fail (subprocess died), then transparently restart and retry.
	result, err := c.Complete(ctx, "after restart")
	if err != nil {
		t.Fatalf("Complete after restart: %v", err)
	}
	if result != "echo: after restart" {
		t.Errorf("result: got %q, want %q", result, "echo: after restart")
	}
	if callCount != 2 {
		t.Errorf("newCmdFn call count: got %d, want 2", callCount)
	}
}

func TestClient_StripsAgentctlEnv(t *testing.T) {
	// Set agentctl env vars in the current process.
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", "@42")

	env := cleanEnv()

	for _, e := range env {
		if strings.HasPrefix(e, "AGENTCTL") {
			t.Errorf("cleanEnv returned AGENTCTL var: %q", e)
		}
	}

	// HOME and PATH must be present.
	hasHome := false
	hasPath := false
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			hasHome = true
		}
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
		}
	}
	if !hasHome {
		t.Error("cleanEnv: HOME missing")
	}
	if !hasPath {
		t.Error("cleanEnv: PATH missing")
	}
}

func TestClient_CapturesRateLimitInfo(t *testing.T) {
	c := newClientWithHelper(t, "rate_limit")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := c.Complete(ctx, "ping"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	info := c.RateLimitInfo()
	if info == nil {
		t.Fatal("RateLimitInfo: expected non-nil after Complete")
	}
	if info.Status != "ok" {
		t.Errorf("Status: got %q, want %q", info.Status, "ok")
	}
	if info.ResetsAt != 9999999999 {
		t.Errorf("ResetsAt: got %d, want 9999999999", info.ResetsAt)
	}
	if info.RateLimitType != "message" {
		t.Errorf("RateLimitType: got %q, want %q", info.RateLimitType, "message")
	}
}

func TestClient_Close(t *testing.T) {
	c := newClientWithHelper(t, "echo")
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second Close must not panic.
	_ = c.Close()
}

// ---------------------------------------------------------------------------
// WithModel option
// ---------------------------------------------------------------------------
