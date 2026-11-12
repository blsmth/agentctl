package cmd

import (
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/state"
)

func TestRestoreOrder(t *testing.T) {
	in := []state.AgentState{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	tests := []struct {
		position string
		want     string
	}{
		{config.NewWindowLeft, "cba"},
		{config.NewWindowRight, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.position, func(t *testing.T) {
			got := ""
			for _, a := range restoreOrder(in, tt.position) {
				got += a.Name
			}
			if got != tt.want {
				t.Errorf("order = %q, want %q", got, tt.want)
			}
			if in[0].Name != "a" {
				t.Error("input was mutated")
			}
		})
	}
}
