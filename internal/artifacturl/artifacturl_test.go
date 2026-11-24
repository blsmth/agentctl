package artifacturl

import "testing"

func TestDirect(t *testing.T) {
	tests := []struct {
		base, id string
		rev      int
		want     string
	}{
		{"https://mac.local:8443", "abc", 0, "https://mac.local:8443/a/abc/latest/"},
		{"https://mac.local:8443/", "abc", 3, "https://mac.local:8443/a/abc/3/"},
	}
	for _, tt := range tests {
		if got := Direct(tt.base, tt.id, tt.rev); got != tt.want {
			t.Errorf("Direct(%q, %q, %d) = %q, want %q", tt.base, tt.id, tt.rev, got, tt.want)
		}
	}
}
