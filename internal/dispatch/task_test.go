package dispatch

import "testing"

func TestTaskFor(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{"ad-hoc prompt", Request{CustomPrompt: "## Task\n\nBuild the **recap** line"}, "Build the recap line"},
		{"ticket dispatch records nothing", Request{Ticket: 7, TicketTitle: "T", CustomPrompt: "extra"}, ""},
		{"no prompt", Request{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TaskFor(tt.req); got != tt.want {
				t.Errorf("TaskFor = %q, want %q", got, tt.want)
			}
		})
	}
}
