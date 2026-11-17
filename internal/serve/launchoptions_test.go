package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/launchopts"
)

type fakeLaunchCfg struct{ cfg LaunchConfig }

func (f fakeLaunchCfg) LaunchConfig(_, _, _ string) (LaunchConfig, error) { return f.cfg, nil }

type fakeOllama struct {
	mu     sync.Mutex
	calls  int
	models []string
	err    error
	block  chan struct{} // when set, the call ignores its context and blocks
}

func (f *fakeOllama) list(ctx context.Context, _ config.BackendConfig) ([]string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	return f.models, f.err
}

func testLaunchConfig() LaunchConfig {
	return LaunchConfig{
		Backends: map[string]config.BackendConfig{
			"local": {BaseURL: "http://127.0.0.1:11434", Model: "qwen3"},
			"nous":  {Kind: config.BackendKindNous, Model: "anthropic/claude-sonnet"},
		},
		AgentEffort: "high",
		NousModels:  []string{"anthropic/claude-sonnet", "x/y"},
	}
}

func newLaunchEnv(t *testing.T, fo *fakeOllama) *writeEnv {
	t.Helper()
	return newWriteEnvWith(t, true, func(o *Options) {
		o.LaunchConfig = fakeLaunchCfg{testLaunchConfig()}
		o.OllamaModels = fo.list
	})
}

func (e *writeEnv) get(path string) *httptest.ResponseRecorder {
	return do(e.Handler(), "GET", path, withCookie(e.tok))
}

const optsPath = "/v1/launch-options?agent=go-engineer&project_dir=/code/proj"

func TestLaunchOptionsShape(t *testing.T) {
	fo := &fakeOllama{models: []string{"qwen3:latest", "llama3"}}
	e := newLaunchEnv(t, fo)
	w := e.get(optsPath)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got launchOptionsBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, b := range got.Backends {
		kinds[b.Name] = b.Kind
		if b.Default != (b.Name == "anthropic") {
			t.Errorf("default flag wrong for %+v", b)
		}
	}
	if kinds["anthropic"] != "anthropic" || kinds["local"] != "ollama" || kinds["nous"] != "nous" || len(kinds) != 3 {
		t.Errorf("kinds = %v", kinds)
	}
	if d := got.Defaults; d.Backend != "anthropic" || d.Model != "inherit" || d.Effort != "high" {
		t.Errorf("defaults = %+v", d)
	}
	if ms := got.Models["local"].Models; len(ms) != 2 || ms[0].Value != "qwen3" || !ms[0].Default || ms[1].Value != "llama3" {
		t.Errorf("local models = %+v", ms)
	}
	if got.Models["nous"].Models[0].Value != "anthropic/claude-sonnet" || len(got.Models["anthropic"].Models) < 4 {
		t.Errorf("models = %+v", got.Models)
	}
	if len(got.Efforts) == 0 || got.Efforts[0] != "inherit" {
		t.Errorf("efforts = %v", got.Efforts)
	}
	for _, f := range e.Server.router.Features() {
		if f == "launch-options" {
			return
		}
	}
	t.Error("launch-options feature not advertised")
}

func TestLaunchOptionsBadTarget(t *testing.T) {
	e := newLaunchEnv(t, &fakeOllama{})
	for _, p := range []string{
		"/v1/launch-options?agent=nope&project_dir=/code/proj",
		"/v1/launch-options?agent=go-engineer&project_dir=/code/other",
		"/v1/launch-options?agent=go-engineer&project_dir=proj",
		"/v1/launch-options?project_dir=/code/proj",
	} {
		if w := e.get(p); w.Code != 400 {
			t.Errorf("%s: status %d: %s", p, w.Code, w.Body)
		}
	}
}

func TestLaunchOptionsOllamaError(t *testing.T) {
	e := newLaunchEnv(t, &fakeOllama{err: errors.New("boom secret-host")})
	var got launchOptionsBody
	w := e.get(optsPath)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	l := got.Models["local"]
	if l.ModelListError == "" || len(l.Models) != 1 || l.Models[0].Value != "qwen3" {
		t.Errorf("local = %+v", l)
	}
	if got.Models["nous"].ModelListError != "" {
		t.Error("nous must not report a list error")
	}
	if strings.Contains(w.Body.String(), "secret-host") {
		t.Error("raw fetch error leaked")
	}
}

func TestOllamaTimeoutDoesNotBlock(t *testing.T) {
	fo := &fakeOllama{block: make(chan struct{})}
	defer close(fo.block)
	c := &ollamaCache{list: fo.list, now: time.Now}
	start := time.Now()
	models, msg := c.models("local", config.BackendConfig{BaseURL: "http://x"})
	if d := time.Since(start); d > ollamaFetchTimeout+time.Second {
		t.Errorf("blocked %v", d)
	}
	if msg == "" || models != nil {
		t.Errorf("models=%v msg=%q", models, msg)
	}
}

func TestOllamaCache(t *testing.T) {
	fo := &fakeOllama{models: []string{"a"}}
	clock := newClock()
	c := &ollamaCache{list: fo.list, now: clock.Now}
	b := config.BackendConfig{BaseURL: "http://x"}
	for range 3 {
		if m, msg := c.models("local", b); msg != "" || len(m) != 1 {
			t.Fatalf("models=%v msg=%q", m, msg)
		}
	}
	if fo.calls != 1 {
		t.Errorf("calls = %d, want 1 (cache hit)", fo.calls)
	}
	clock.Advance(ollamaCacheTTL + time.Second)
	c.models("local", b)
	if fo.calls != 2 {
		t.Errorf("calls = %d after expiry, want 2", fo.calls)
	}
}

func TestDispatchLaunchChoices(t *testing.T) {
	const base = `"agent":"go-engineer","project_dir":"/code/proj","prompt":"p"`
	bad := []struct{ name, extra string }{
		{"invalid backend", `,"backend":"nope"`},
		{"invalid effort", `,"effort":"extreme"`},
		{"model not offered anthropic", `,"model":"gpt-9"`},
		{"model not offered ollama", `,"backend":"local","model":"nothere"`},
		{"model not offered nous", `,"backend":"nous","model":"a/b"`},
		{"inherit on ollama", `,"backend":"local","model":"inherit"`},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			e := newLaunchEnv(t, &fakeOllama{models: []string{"qwen3", "llama3"}})
			w := e.post("/v1/dispatch", `{`+base+tt.extra+`}`)
			if w.Code != 400 || errCode(t, w) != codeBadRequest {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if len(e.disp.got) != 0 {
				t.Errorf("dispatched %+v", e.disp.got)
			}
		})
	}

	ok := []struct {
		name, extra            string
		backend, model, effort string
	}{
		{"defaults are no override", `,"backend":"anthropic","model":"inherit","effort":"high"`, "", "", ""},
		{"anthropic override", `,"backend":"anthropic","model":"opus","effort":"max"`, "", "opus", "max"},
		{"ollama pick", `,"backend":"local","model":"llama3","effort":"inherit"`, "local", "llama3", "inherit"},
		{"ollama default model", `,"backend":"local","model":"qwen3"`, "local", "", ""},
		{"nous pick", `,"backend":"nous","model":"x/y"`, "nous", "x/y", ""},
	}
	for _, tt := range ok {
		t.Run(tt.name, func(t *testing.T) {
			e := newLaunchEnv(t, &fakeOllama{models: []string{"qwen3", "llama3"}})
			w := e.post("/v1/dispatch", `{`+base+tt.extra+`}`)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if len(e.disp.got) != 1 {
				t.Fatalf("dispatch = %+v", e.disp.got)
			}
			got := e.disp.got[0]
			if got.Backend != tt.backend || got.Model != tt.model || got.Effort != tt.effort {
				t.Errorf("got backend=%q model=%q effort=%q, want %q %q %q", got.Backend, got.Model, got.Effort, tt.backend, tt.model, tt.effort)
			}
			if lines := e.auditLines(t); len(lines) == 0 || lines[0].Backend == "" && tt.backend != "" {
				t.Errorf("audit lacks backend: %+v", lines)
			}
		})
	}

	t.Run("ollama list failed allows regex model", func(t *testing.T) {
		e := newLaunchEnv(t, &fakeOllama{err: errors.New("down")})
		w := e.post("/v1/dispatch", `{`+base+`,"backend":"local","model":"custom:7b"}`)
		if w.Code != 200 || e.disp.got[0].Model != "custom:7b" || e.disp.got[0].Backend != "local" {
			t.Fatalf("status %d: %s; %+v", w.Code, w.Body, e.disp.got)
		}
		w = e.post("/v1/dispatch", `{`+base+`,"backend":"local","model":"x; rm"}`)
		if w.Code != 400 {
			t.Errorf("unsafe model status %d", w.Code)
		}
	})
}

func TestDispatchAgentBackendForcedAnthropic(t *testing.T) {
	cfg := testLaunchConfig()
	cfg.AgentBackend = "local"
	e := newWriteEnvWith(t, true, func(o *Options) {
		o.LaunchConfig = fakeLaunchCfg{cfg}
		o.OllamaModels = (&fakeOllama{models: []string{"qwen3"}}).list
	})
	w := e.post("/v1/dispatch", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"p","backend":"anthropic"}`)
	if w.Code != 200 || e.disp.got[0].Backend != launchopts.BackendNone {
		t.Fatalf("status %d: %s; %+v", w.Code, w.Body, e.disp.got)
	}
}
