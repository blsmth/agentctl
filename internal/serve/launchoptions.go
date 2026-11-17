package serve

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tokens"
)

const (
	// ollamaFetchTimeout bounds the live model list fetch of one request.
	ollamaFetchTimeout = 3 * time.Second
	// ollamaCacheTTL is how long a fetched list is reused per backend;
	// ollamaErrorTTL is the shorter reuse of a failure.
	ollamaCacheTTL = 60 * time.Second
	ollamaErrorTTL = 10 * time.Second
)

// LaunchConfig is the configuration a launch-options answer is built from,
// read for one project session and agent.
type LaunchConfig struct {
	Backends     map[string]config.BackendConfig
	AgentBackend string
	AgentModel   string
	AgentEffort  string
	NousModels   []string
}

// LaunchConfigSource reads the effective launch configuration of agent in a
// session's project.
type LaunchConfigSource interface {
	LaunchConfig(session, runtimeDir, agent string) (LaunchConfig, error)
}

// OllamaLister lists an Ollama backend's live models.
type OllamaLister func(ctx context.Context, b config.BackendConfig) ([]string, error)

// configLaunchSource reads the real config under the project's session.
type configLaunchSource struct{}

func (configLaunchSource) LaunchConfig(session, runtimeDir, agent string) (LaunchConfig, error) {
	var out LaunchConfig
	err := withSession(session, runtimeDir, func() error {
		cfg := config.Load()
		ac := cfg.ForAgent(agent)
		out = LaunchConfig{
			Backends:     cfg.Backends,
			AgentBackend: ac.Backend,
			AgentModel:   ac.Model,
			AgentEffort:  ac.EffortLevel,
			NousModels:   tokens.CatalogModelIDs(),
		}
		return nil
	})
	return out, err
}

type ollamaEntry struct {
	models []string
	err    string
	at     time.Time
}

// ollamaCache fetches and caches Ollama model lists per backend.
type ollamaCache struct {
	list OllamaLister
	now  func() time.Time

	mu sync.Mutex
	m  map[string]ollamaEntry
}

func (c *ollamaCache) lookup(key string) (ollamaEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return ollamaEntry{}, false
	}
	ttl := ollamaCacheTTL
	if e.err != "" {
		ttl = ollamaErrorTTL
	}
	return e, c.now().Sub(e.at) < ttl
}

func (c *ollamaCache) store(key string, e ollamaEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]ollamaEntry)
	}
	e.at = c.now()
	c.m[key] = e
}

// models returns the backend's model list, or an error message. It never
// blocks longer than ollamaFetchTimeout, even if the lister ignores its
// context.
func (c *ollamaCache) models(name string, b config.BackendConfig) ([]string, string) {
	key := name + "\x00" + b.BaseURL
	if e, fresh := c.lookup(key); fresh {
		return e.models, e.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ollamaFetchTimeout)
	defer cancel()
	type result struct {
		models []string
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		m, err := c.list(ctx, b)
		ch <- result{m, err}
	}()
	var e ollamaEntry
	select {
	case r := <-ch:
		if r.err != nil {
			logging.Warn("serve: listing models of backend %s: %v", name, r.err)
			e.err = "the model list could not be fetched"
		} else {
			e.models = r.models
		}
	case <-ctx.Done():
		logging.Warn("serve: listing models of backend %s: timed out", name)
		e.err = "the model list fetch timed out"
	}
	c.store(key, e)
	return e.models, e.err
}

// launchChoices is the shared view of the options one agent in one project
// offers, used by GET /v1/launch-options and to validate POST /v1/dispatch.
type launchChoices struct {
	in launchopts.Inputs
	// listErrors maps an Ollama backend whose list could not be fetched to
	// the reason.
	listErrors map[string]string
}

func (s *Server) launchChoices(session, runtimeDir, agent string) (launchChoices, error) {
	lc, err := s.launchCfg.LaunchConfig(session, runtimeDir, agent)
	if err != nil {
		return launchChoices{}, err
	}
	in := launchopts.Inputs{
		AgentBackend: lc.AgentBackend,
		AgentModel:   lc.AgentModel,
		AgentEffort:  lc.AgentEffort,
		Backends:     launchopts.SpecsFromConfig(lc.Backends),
		AnthropicIDs: launchopts.AnthropicIDNotes(),
		NousModels:   lc.NousModels,
		OllamaModels: map[string][]string{},
	}
	out := launchChoices{in: in, listErrors: map[string]string{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for name, b := range lc.Backends {
		if b.IsNous() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			models, msg := s.ollama.models(name, b)
			mu.Lock()
			defer mu.Unlock()
			if msg != "" {
				out.listErrors[name] = msg
			} else {
				in.OllamaModels[name] = models
			}
		}()
	}
	wg.Wait()
	return out, nil
}

type launchBackend struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
}

type launchModels struct {
	Models         []launchopts.ModelChoice `json:"models"`
	ModelListError string                   `json:"model_list_error,omitempty"`
}

type launchDefaults struct {
	Backend string `json:"backend"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

type launchOptionsBody struct {
	Backends []launchBackend         `json:"backends"`
	Models   map[string]launchModels `json:"models"`
	Efforts  []string                `json:"efforts"`
	Defaults launchDefaults          `json:"defaults"`
}

func (c launchChoices) body() launchOptionsBody {
	names, def := c.in.BackendNames()
	b := launchOptionsBody{
		Models:  map[string]launchModels{},
		Efforts: c.in.EffortOptions(),
		Defaults: launchDefaults{
			Backend: def, Model: c.in.DefaultModel(def), Effort: c.in.DefaultEffort(),
		},
	}
	for _, n := range names {
		kind := "anthropic"
		switch {
		case n == launchopts.BackendAnthropic:
		case c.in.IsNous(n):
			kind = "nous"
		default:
			kind = "ollama"
		}
		b.Backends = append(b.Backends, launchBackend{Name: n, Kind: kind, Default: n == def})
		b.Models[n] = launchModels{Models: c.in.ModelChoices(n), ModelListError: c.listErrors[n]}
	}
	return b
}

// handleLaunchOptions answers GET /v1/launch-options?agent=&project_dir=,
// validating both exactly as POST /v1/dispatch does.
func (s *Server) handleLaunchOptions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	g, out, ok := s.dispatchTarget(q.Get("agent"), q.Get("project_dir"), nil)
	if !ok {
		writeError(w, out.status, out.code, out.msg)
		return
	}
	c, err := s.launchChoices(g.Session, g.RuntimeDir, q.Get("agent"))
	if err != nil {
		logging.Warn("serve: launch options: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "reading launch options")
		return
	}
	writeJSON(w, http.StatusOK, c.body())
}
