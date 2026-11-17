package launchopts

import (
	"slices"
	"testing"
)

func values(cs []ModelChoice) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Value)
	}
	return out
}

func testInputs() Inputs {
	return Inputs{
		Backends: map[string]BackendSpec{
			"local": {Model: "qwen3"},
			"nous": {Nous: true, Model: "anthropic/claude-sonnet", Aliases: []ModelNote{
				{"anthropic/claude-opus", "opus alias"}, {"", "sonnet alias"},
			}},
		},
		AnthropicIDs: []ModelNote{{"claude-opus-4", "Opus 4"}, {"opus", "dup"}},
		NousModels:   []string{"anthropic/claude-sonnet", "x/y"},
	}
}

func TestBackendNames(t *testing.T) {
	tests := []struct {
		name, agentBE string
		wantDef       string
	}{
		{"none", "", BackendAnthropic},
		{"configured", "local", "local"},
		{"unknown", "gone", BackendAnthropic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := testInputs()
			in.AgentBackend = tt.agentBE
			names, def := in.BackendNames()
			if def != tt.wantDef {
				t.Errorf("def = %q, want %q", def, tt.wantDef)
			}
			if want := []string{"anthropic", "local", "nous"}; !slices.Equal(names, want) {
				t.Errorf("names = %v", names)
			}
		})
	}
}

func TestModelChoices(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Inputs)
		backend string
		want    []string
		defVal  string
	}{
		{"anthropic inherit", func(*Inputs) {}, BackendAnthropic,
			[]string{"inherit", "opus", "sonnet", "haiku", "fable", "claude-opus-4"}, "inherit"},
		{"anthropic agent model", func(in *Inputs) { in.AgentModel = "opus" }, BackendAnthropic,
			[]string{"opus", "inherit", "sonnet", "haiku", "fable", "claude-opus-4"}, "opus"},
		{"ollama with list", func(in *Inputs) {
			in.OllamaModels = map[string][]string{"local": {"qwen3:latest", "llama3"}}
		}, "local", []string{"qwen3", "llama3"}, "qwen3"},
		{"ollama list error", func(*Inputs) {}, "local", []string{"qwen3"}, "qwen3"},
		{"nous catalog", func(*Inputs) {}, "nous",
			[]string{"anthropic/claude-sonnet", "anthropic/claude-opus", "x/y"}, "anthropic/claude-sonnet"},
		{"agent backend configured", func(in *Inputs) { in.AgentBackend = "local"; in.AgentModel = "ignored" }, BackendAnthropic,
			[]string{"inherit", "opus", "sonnet", "haiku", "fable", "claude-opus-4"}, "inherit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := testInputs()
			tt.mutate(&in)
			got := in.ModelChoices(tt.backend)
			if !slices.Equal(values(got), tt.want) {
				t.Fatalf("values = %v, want %v", values(got), tt.want)
			}
			if !got[0].Default || got[0].Value != tt.defVal && tt.name != "anthropic agent model" {
				t.Errorf("first = %+v", got[0])
			}
		})
	}
}

func TestEffortOptions(t *testing.T) {
	in := Inputs{}
	if in.DefaultEffort() != Inherit || !slices.Equal(in.EffortOptions(), EffortChoices) {
		t.Errorf("defaults wrong: %v", in.EffortOptions())
	}
	in.AgentEffort = "xhigh"
	if in.DefaultEffort() != "xhigh" || !slices.Contains(in.EffortOptions(), "xhigh") {
		t.Errorf("configured effort not offered: %v", in.EffortOptions())
	}
	// EffortOptions must not mutate the shared slice.
	if slices.Contains(EffortChoices, "xhigh") {
		t.Error("EffortChoices mutated")
	}
}

func TestResults(t *testing.T) {
	in := testInputs()
	in.AgentEffort = "high"
	in.AgentModel = "opus"
	tests := []struct{ name, got, want string }{
		{"anthropic agent model", in.ResultModel(BackendAnthropic, "opus"), ""},
		{"anthropic inherit", in.ResultModel(BackendAnthropic, Inherit), Inherit},
		{"anthropic other", in.ResultModel(BackendAnthropic, "haiku"), "haiku"},
		{"ollama default", in.ResultModel("local", "qwen3"), ""},
		{"ollama other", in.ResultModel("local", "llama3"), "llama3"},
		{"backend anthropic", in.ResultBackend(BackendAnthropic), ""},
		{"backend named", in.ResultBackend("local"), "local"},
		{"effort default", in.ResultEffort("high"), ""},
		{"effort other", in.ResultEffort("low"), "low"},
		{"effort inherit", in.ResultEffort(Inherit), Inherit},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	in.AgentBackend = "local"
	if got := in.ResultBackend(BackendAnthropic); got != BackendNone {
		t.Errorf("forced anthropic = %q", got)
	}
}
