package cmd

import (
	"strings"
	"testing"

	ctxstore "github.com/blsmth/agentctl/internal/context"
)

// TestResolvePromptKey_Found verifies the happy path: a populated context
// entry resolves to its content, unwrapped, ready to become promptText.
func TestResolvePromptKey_Found(t *testing.T) {
	ctxstore.SetPathForTest(t.TempDir())

	if err := ctxstore.Set("ticket-42-spec", ctxstore.Entry{Content: "do the thing"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := resolvePromptKey("ticket-42-spec")
	if err != nil {
		t.Fatalf("resolvePromptKey: unexpected error: %v", err)
	}
	if got != "do the thing" {
		t.Errorf("resolvePromptKey = %q, want %q", got, "do the thing")
	}
}

// TestResolvePromptKey_NotFound verifies a missing key surfaces a
// "not found" error naming the exact key requested.
func TestResolvePromptKey_NotFound(t *testing.T) {
	ctxstore.SetPathForTest(t.TempDir())

	_, err := resolvePromptKey("does-not-exist")
	if err == nil {
		t.Fatal("resolvePromptKey: expected error, got nil")
	}
	if !strings.Contains(err.Error(), `"does-not-exist"`) || !strings.Contains(err.Error(), "not found") {
		t.Errorf("resolvePromptKey error = %q, want it to mention the missing key and \"not found\"", err.Error())
	}
}

// TestResolvePromptKey_WhitespaceOnly verifies a context entry that exists
// but has only whitespace content is treated as empty, not silently
// dispatched as a blank prompt.
func TestResolvePromptKey_WhitespaceOnly(t *testing.T) {
	ctxstore.SetPathForTest(t.TempDir())

	if err := ctxstore.Set("blank-spec", ctxstore.Entry{Content: "   \n\t  "}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, err := resolvePromptKey("blank-spec")
	if err == nil {
		t.Fatal("resolvePromptKey: expected error for whitespace-only content, got nil")
	}
	if !strings.Contains(err.Error(), `"blank-spec"`) || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("resolvePromptKey error = %q, want it to mention the key and \"is empty\"", err.Error())
	}
}
