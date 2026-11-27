package tui

import "testing"

func TestFillPRURL(t *testing.T) {
	old := ticketRepoSlug
	t.Cleanup(func() { ticketRepoSlug = old })
	ticketRepoSlug = func(string) string { return "acme/shop" }

	tests := []struct {
		name string
		st   hubStatus
		want string
	}{
		{"number only", hubStatus{PRNumber: 9}, "https://github.com/acme/shop/pull/9"},
		{"url kept", hubStatus{PRNumber: 9, PRURL: "https://github.com/a/b/pull/9"}, "https://github.com/a/b/pull/9"},
		{"no PR", hubStatus{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fillPRURL(&tt.st, "/tmp/proj-fillpr/.agentctl")
			if tt.st.PRURL != tt.want {
				t.Errorf("PRURL = %q, want %q", tt.st.PRURL, tt.want)
			}
		})
	}
}
