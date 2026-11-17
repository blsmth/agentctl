package dispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestListOllamaModels(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    []string
		wantErr error
		anyErr  bool
	}{
		{name: "tags", status: 200, body: `{"models":[{"name":"qwen3:latest"},{"model":"llama3:8b"}]}`, want: []string{"qwen3:latest", "llama3:8b"}},
		{name: "empty list", status: 200, body: `{"models":[]}`, want: []string{}},
		{name: "not ollama", status: 200, body: `{"ok":true}`, wantErr: ErrNoModelList},
		{name: "non-200", status: 404, body: `nope`, wantErr: ErrNoModelList},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tags" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := ListOllamaModels(context.Background(), config.BackendConfig{BaseURL: srv.URL + "/"})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListOllamaModelsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := ListOllamaModels(context.Background(), config.BackendConfig{BaseURL: url})
	if err == nil || errors.Is(err, ErrNoModelList) {
		t.Fatalf("err = %v, want unreachable error", err)
	}
}

func TestApplyOverrides(t *testing.T) {
	tests := []struct {
		name       string
		ac         config.AgentConfig
		req        Request
		def        string
		wantModel  string
		wantEffort string
		wantExpl   string
		wantErr    bool
	}{
		{name: "effort override", ac: config.AgentConfig{EffortLevel: "low"}, req: Request{Effort: "max"}, wantEffort: "max"},
		{name: "effort inherit clears", ac: config.AgentConfig{EffortLevel: "low"}, req: Request{Effort: Inherit}, wantEffort: ""},
		{name: "no effort keeps config", ac: config.AgentConfig{EffortLevel: "high"}, wantEffort: "high"},
		{name: "bad effort", req: Request{Effort: "turbo"}, wantErr: true},
		{name: "model override", ac: config.AgentConfig{Model: "opus"}, req: Request{Model: "haiku"}, wantModel: "haiku", wantExpl: "haiku"},
		{name: "model inherit clears", ac: config.AgentConfig{Model: "opus"}, req: Request{Model: Inherit}, wantModel: ""},
		{name: "mode default", req: Request{}, def: "sonnet", wantModel: "sonnet"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ac := tc.ac
			expl, err := applyOverrides(&ac, tc.req, tc.def)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if ac.Model != tc.wantModel || ac.EffortLevel != tc.wantEffort || expl != tc.wantExpl {
				t.Errorf("model=%q effort=%q explicit=%q", ac.Model, ac.EffortLevel, expl)
			}
		})
	}
}

func TestBuildBaseFlagsEffort(t *testing.T) {
	ac := config.AgentConfig{EffortLevel: "low"}
	if _, err := applyOverrides(&ac, Request{Effort: "max"}, ""); err != nil {
		t.Fatal(err)
	}
	got := BuildBaseFlags(ClaudeInvocation{Agent: "a"}, ac)
	if want := "claude --agent 'a' --effort 'max'"; got != want {
		t.Errorf("flags = %q, want %q", got, want)
	}
}
