package config

import (
	"reflect"
	"testing"
)

func TestEffectiveTitles(t *testing.T) {
	const nous = `"kind":"nous","model":"qwen/qwen3-coder-next"`
	tests := []struct {
		name        string
		global      string
		wantEnabled bool
		wantBackend string
		wantModel   string
	}{
		{name: "no backends", global: `{}`},
		{name: "ollama only", global: `{"backends":{"o":{"base_url":"http://h:11434","model":"m"}}}`},
		{
			name:        "default uses models.haiku",
			global:      `{"backends":{"nous":{` + nous + `,"models":{"haiku":"openai/gpt-oss-120b"}}}}`,
			wantEnabled: true, wantBackend: "nous", wantModel: "openai/gpt-oss-120b",
		},
		{
			name:        "default falls back to model",
			global:      `{"backends":{"nous":{` + nous + `}}}`,
			wantEnabled: true, wantBackend: "nous", wantModel: "qwen/qwen3-coder-next",
		},
		{
			name:        "first nous backend by name",
			global:      `{"backends":{"zeta":{"kind":"nous","model":"z/z"},"alpha":{"kind":"nous","model":"a/a"},"aaa":{"base_url":"http://h","model":"o"}}}`,
			wantEnabled: true, wantBackend: "alpha", wantModel: "a/a",
		},
		{
			name:   "explicit off",
			global: `{"titles":{"enabled":false},"backends":{"nous":{` + nous + `}}}`,
		},
		{
			name:        "explicit backend and model",
			global:      `{"titles":{"backend":"zeta","model":"meta-llama/llama-3.1-8b-instruct"},"backends":{"zeta":{"kind":"nous","model":"z/z"},"alpha":{"kind":"nous","model":"a/a"}}}`,
			wantEnabled: true, wantBackend: "zeta", wantModel: "meta-llama/llama-3.1-8b-instruct",
		},
		{
			name:   "backend not nous",
			global: `{"titles":{"backend":"o"},"backends":{"o":{"base_url":"http://h:11434","model":"m"}}}`,
		},
		{
			name:   "backend missing",
			global: `{"titles":{"enabled":true,"backend":"gone"},"backends":{"nous":{` + nous + `}}}`,
		},
		{
			name:   "enabled with no backend",
			global: `{"titles":{"enabled":true}}`,
		},
		{
			name:   "bad model slug",
			global: `{"titles":{"model":"a b; rm"},"backends":{"nous":{` + nous + `}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loadWith(t, tt.global, `{}`).EffectiveTitles()
			if got.Enabled != tt.wantEnabled || got.BackendName != tt.wantBackend || got.Model != tt.wantModel {
				t.Fatalf("EffectiveTitles = %+v, want enabled=%v backend=%q model=%q",
					got, tt.wantEnabled, tt.wantBackend, tt.wantModel)
			}
			if got.Enabled && !got.Backend.IsNous() {
				t.Fatalf("backend config not carried: %+v", got.Backend)
			}
		})
	}
}

// A project-local config can neither switch titles on nor pick the backend
// or model, and Save never writes the block into the project layer.
func TestProjectLayerTitlesIgnored(t *testing.T) {
	const global = `{"titles":{"enabled":false},"backends":{"nous":{"kind":"nous","model":"a/a"}}}`
	const project = `{"titles":{"enabled":true,"backend":"nous","model":"evil/expensive"}}`
	cfg := loadWith(t, global, project)
	if want := (TitlesConfig{Enabled: boolPtr(false)}); !reflect.DeepEqual(cfg.Titles, want) {
		t.Fatalf("Titles = %+v, want global %+v", cfg.Titles, want)
	}
	if cfg.EffectiveTitles().Enabled {
		t.Fatal("project config enabled titles")
	}
	if !warnedContaining(`"titles"`) {
		t.Error("ignored titles key was not reported")
	}

	cfg = loadWith(t, `{"backends":{"nous":{"kind":"nous","model":"a/a"}}}`, `{"titles":{"model":"evil/expensive"}}`)
	if got := cfg.EffectiveTitles().Model; got != "a/a" {
		t.Fatalf("project model honored: %q", got)
	}
	cfg.Titles.Model = "x/y"
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	layer, err := readLayer(FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if layer.Titles.Model == "x/y" {
		t.Error("titles saved into the project-local layer")
	}
}
