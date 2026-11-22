package windowstate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapPrompt(t *testing.T) {
	if got := CapPrompt("short"); got != "short" {
		t.Errorf("got %q", got)
	}
	got := CapPrompt(strings.Repeat("é", MaxPromptBytes)) // 2 bytes per rune
	if len(got) > MaxPromptBytes || !utf8.ValidString(got) {
		t.Errorf("len %d valid %v", len(got), utf8.ValidString(got))
	}
}
