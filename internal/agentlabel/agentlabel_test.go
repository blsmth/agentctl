package agentlabel

import "testing"

func TestInfer(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"alias", []string{"bug", "python"}, "python-engineer"},
		{"exact name", []string{"terraform-engineer"}, "terraform-engineer"},
		{"exact beats alias", []string{"python", "go-engineer"}, "go-engineer"},
		{"infra is not mapped", []string{"infra"}, ""},
		{"other-agent is not mapped", []string{"other-agent"}, ""},
		{"none", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Infer(tc.labels); got != tc.want {
				t.Errorf("Infer(%v) = %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

func TestInferOrDefault(t *testing.T) {
	if got := InferOrDefault([]string{"infra"}); got != Default {
		t.Errorf("got %q, want %q", got, Default)
	}
}

func TestNoPrivateAgents(t *testing.T) {
	for k, v := range aliases {
		if v == "other-agent" || k == "infra" {
			t.Errorf("private agent mapping present: %q -> %q", k, v)
		}
	}
}
