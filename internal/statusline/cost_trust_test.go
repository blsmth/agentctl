package statusline

import (
	"strings"
	"testing"
)

func TestPushedCostTrusted(t *testing.T) {
	tests := []struct {
		name  string
		model *Model
		want  bool
	}{
		{"no model", nil, true},
		{"model without id", &Model{DisplayName: strPtr("Sonnet")}, true},
		{"claude", &Model{ID: strPtr("claude-opus-5-5")}, true},
		{"nous", &Model{ID: strPtr("openai/gpt-6-astra")}, false},
		{"ollama", &Model{ID: strPtr("qwen3.6:35b-a3b")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pushedCostTrusted(&Payload{Model: tt.model}); got != tt.want {
				t.Errorf("pushedCostTrusted = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNonClaudeModelHidesPushedCost(t *testing.T) {
	p := &Payload{
		Model: &Model{ID: strPtr("openai/gpt-6-astra"), DisplayName: strPtr("gpt-6-astra")},
		Cost:  &Cost{TotalCostUSD: f64Ptr(12.34)},
	}
	if got := renderLine(p, "complementary"); strings.Contains(got, "$") {
		t.Errorf("complementary = %q, want no cost for a non-claude model", got)
	}
	line, _ := renderRichLine2(p, 200, nil)
	if strings.Contains(stripANSI(line), "$") {
		t.Errorf("rich line 2 = %q, want no cost for a non-claude model", stripANSI(line))
	}

	p.Model.ID = strPtr("claude-opus-5-5")
	if got := renderLine(p, "complementary"); !strings.Contains(got, "$12.34") {
		t.Errorf("claude model should still show cost, got %q", got)
	}
}
