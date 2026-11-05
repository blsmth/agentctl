package highlight

import (
	"strings"
	"testing"
)

func TestHighlight_GoFile(t *testing.T) {
	content := `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`
	out, err := Highlight(content, "main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should contain ANSI escape sequences for syntax highlighting.
	if !strings.Contains(out, "\x1b[") {
		t.Error("expected ANSI escape sequences in highlighted Go output")
	}
}

func TestHighlight_PythonFile(t *testing.T) {
	content := `def hello():
    print("hello")
`
	out, err := Highlight(content, "hello.py")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("expected ANSI escape sequences in highlighted Python output")
	}
}

func TestHighlight_JavaScriptFile(t *testing.T) {
	content := `function hello() {
  console.log("hello");
}
`
	out, err := Highlight(content, "hello.js")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("expected ANSI escape sequences in highlighted JavaScript output")
	}
}

func TestHighlight_UnknownExtension(t *testing.T) {
	content := "some plain text content\nwith multiple lines"
	out, err := Highlight(content, "unknownfile.xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still return some output even for unknown extensions (fallback lexer).
	if out == "" {
		t.Error("expected non-empty output for unknown extension")
	}
}

func TestHighlight_EmptyContent(t *testing.T) {
	out, err := Highlight("", "main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output for empty content, got %q", out)
	}
}

func TestHighlight_BinaryContent(t *testing.T) {
	// Content with null bytes = binary.
	content := "hello\x00world"
	out, err := Highlight(content, "binary.bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return empty string for binary content.
	if out != "" {
		t.Errorf("expected empty output for binary content, got %q", out)
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"plain text", "hello world", false},
		{"go code", "package main\n", false},
		{"null byte", "hello\x00world", true},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBinary(tt.content); got != tt.want {
				t.Errorf("IsBinary(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}
