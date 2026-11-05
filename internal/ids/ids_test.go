package ids_test

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/ids"
)

func TestNewUUID(t *testing.T) {
	t.Run("format", func(t *testing.T) {
		u := ids.NewUUID()
		parts := strings.Split(u, "-")
		if len(parts) != 5 {
			t.Fatalf("NewUUID() = %q, want 5 dash-separated groups", u)
		}
		lengths := []int{8, 4, 4, 4, 12}
		for i, p := range parts {
			if len(p) != lengths[i] {
				t.Errorf("group[%d] = %q (len %d), want len %d", i, p, len(p), lengths[i])
			}
		}
	})

	t.Run("hex_chars", func(t *testing.T) {
		u := ids.NewUUID()
		for _, ch := range strings.ReplaceAll(u, "-", "") {
			if !('0' <= ch && ch <= '9') && !('a' <= ch && ch <= 'f') {
				t.Errorf("non-hex character %q in UUID %q", ch, u)
			}
		}
	})

	t.Run("version4_variant", func(t *testing.T) {
		u := ids.NewUUID()
		parts := strings.Split(u, "-")
		// Third group: first hex digit must be '4' (version 4)
		if len(parts[2]) > 0 && parts[2][0] != '4' {
			t.Errorf("version nibble = %c, want '4' in UUID %q", parts[2][0], u)
		}
		// Fourth group: first hex digit must be 8, 9, a, or b (variant bits)
		if len(parts[3]) > 0 {
			ch := parts[3][0]
			if ch != '8' && ch != '9' && ch != 'a' && ch != 'b' {
				t.Errorf("variant nibble = %c, want one of 8/9/a/b in UUID %q", ch, u)
			}
		}
	})

	t.Run("unique", func(t *testing.T) {
		seen := make(map[string]bool)
		for i := 0; i < 100; i++ {
			u := ids.NewUUID()
			if seen[u] {
				t.Fatalf("duplicate UUID generated: %q", u)
			}
			seen[u] = true
		}
	})

	t.Run("slug_prefix_hex", func(t *testing.T) {
		// Verify that NewUUID()[:8] (used as ad-hoc branch slug) is 8 hex chars.
		slug := ids.NewUUID()[:8]
		if len(slug) != 8 {
			t.Errorf("slug len = %d, want 8", len(slug))
		}
		for _, ch := range slug {
			if !('0' <= ch && ch <= '9') && !('a' <= ch && ch <= 'f') {
				t.Errorf("non-hex character %q in slug %q", ch, slug)
			}
		}
	})
}
