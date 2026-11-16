package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tmux"
)

// BackendNone is the --backend value that forces Anthropic even when the
// agent config selects a backend.
const BackendNone = "none"

// EnvBackend carries the active backend's name into the window (and from
// there into windowstate) so restore and status can find it again.
const EnvBackend = "AGENTCTL_BACKEND"

// Backend is a resolved, named backend.
type Backend struct {
	Name string
	config.BackendConfig
}

// BackendEnv returns the environment that points Claude Code at b instead of
// Anthropic. ANTHROPIC_API_KEY is present but empty so a real key in the
// parent environment cannot leak to the endpoint. The alias and subagent model
// vars matter because Claude Code uses haiku for background work and subagents
// default to aliases, none of which exist on a self-hosted server.
//
// For kind nous it returns only non-secret variables and deliberately omits
// ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN and ANTHROPIC_API_KEY: the base URL
// and the credential helper travel together in the settings file
// (BuildAgentSettingsJSON), so a launch can never carry the URL without the
// helper (which would send the user's Anthropic login to Nous) or the other
// way round. The key itself is never in env.
func BackendEnv(b config.BackendConfig) map[string]string {
	if b.IsNous() {
		return nousEnv(b)
	}
	token := b.AuthToken
	if token == "" {
		token = config.DefaultBackendAuthToken
	}
	env := map[string]string{
		"ANTHROPIC_BASE_URL":             b.BaseURL,
		"ANTHROPIC_AUTH_TOKEN":           token,
		"ANTHROPIC_API_KEY":              "",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   b.Model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": b.Model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  b.Model,
		"CLAUDE_CODE_SUBAGENT_MODEL":     b.Model,
	}
	if b.MaxContextTokens > 0 {
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = fmt.Sprintf("%d", b.MaxContextTokens)
	}
	return env
}

// nousUnsetEnv lists credential variables removed from a nous launch so an
// inherited one cannot shadow the apiKeyHelper or be sent to Nous.
const nousUnsetEnv = "ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN"

func nousEnv(b config.BackendConfig) map[string]string {
	env := map[string]string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   b.AliasModel("opus"),
		"ANTHROPIC_DEFAULT_SONNET_MODEL": b.AliasModel("sonnet"),
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  b.AliasModel("haiku"),
		"CLAUDE_CODE_SUBAGENT_MODEL":     b.AliasModel("subagent"),
		// Claude Code's experimental beta headers are unverified against
		// Nous; the plain request shape is the safe default.
		"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
		tmux.EnvUnset:                            nousUnsetEnv,
	}
	if b.MaxContextTokens > 0 {
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = fmt.Sprintf("%d", b.MaxContextTokens)
	}
	return env
}

// ResolveBackend picks the backend for a launch. override is the CLI/request
// value: "" defers to ac.Backend, BackendNone forces Anthropic, anything else
// names a backend. It returns nil, nil when no backend applies. An unknown
// name is an error: never fall back to Anthropic silently.
//
// When a backend applies, ac.Model becomes the backend model (an "opus" alias
// is meaningless there) unless explicitModel is set, which wins over both.
// For a nous backend an explicitModel must be a slug (contain "/"): a bare
// Claude model name would otherwise run on the Anthropic subscription while
// windowstate records nous.
func ResolveBackend(cfg *config.Config, ac *config.AgentConfig, override, explicitModel string) (*Backend, error) {
	name := override
	if name == "" {
		name = ac.Backend
	}
	if name == "" || name == BackendNone {
		return nil, nil
	}
	b, err := cfg.ResolveBackend(name)
	if err != nil {
		return nil, err
	}
	if explicitModel == "" {
		ac.Model = b.Model
	} else if b.IsNous() && !strings.Contains(explicitModel, "/") {
		return nil, fmt.Errorf("model %q is not a Nous model slug (expected provider/model, e.g. %s); refusing to launch a Claude model on backend %q", explicitModel, b.Model, name)
	}
	return &Backend{Name: name, BackendConfig: b}, nil
}

// ApplyBackendEnv merges the backend's env (and AGENTCTL_BACKEND) into env.
// A nil backend is a no-op.
func ApplyBackendEnv(env map[string]string, be *Backend) {
	if be == nil {
		return
	}
	for k, v := range BackendEnv(be.BackendConfig) {
		env[k] = v
	}
	env[EnvBackend] = be.Name
}

// PrepareBackend resolves the backend, runs the preflight check, and merges
// its env into env. It is the one call every launch path makes, and it must
// run before the launch's settings files are written: it records the
// effective backend on ac.Backend (an override wins; BackendNone clears it)
// so BuildAgentSettingsJSON can emit the right settings. workDir is where the
// agent will run; nous launches scan its settings layers for credentials.
// A nil backend (and nil error) means the launch stays on Anthropic.
func PrepareBackend(cfg *config.Config, ac *config.AgentConfig, override, explicitModel, workDir string, env map[string]string) (*Backend, error) {
	be, err := ResolveBackend(cfg, ac, override, explicitModel)
	if err != nil {
		return nil, err
	}
	if be == nil {
		ac.Backend = ""
		return nil, nil
	}
	if be.IsNous() {
		info, err := preflightNous(be.Name, be.BackendConfig, workDir)
		if err != nil {
			return nil, fmt.Errorf("backend %q: %w", be.Name, err)
		}
		if be.MaxContextTokens == 0 {
			be.MaxContextTokens = info.ContextLength
		}
	} else if err := PreflightBackend(be.BackendConfig); err != nil {
		return nil, fmt.Errorf("backend %q: %w", be.Name, err)
	}
	ApplyBackendEnv(env, be)
	ac.Backend = be.Name
	return be, nil
}

// backendHTTPClient performs the preflight request; tests replace it.
var backendHTTPClient = &http.Client{}

// preflightTimeout bounds the /api/tags request.
var preflightTimeout = 3 * time.Second

// PreflightBackend fails fast when an Ollama backend cannot serve a launch (nous
// backends use preflightNous). It does
// GET <base_url>/api/tags: unreachable is an error; a 200 that parses as an
// Ollama tag list must contain b.Model (exact name match, since e.g.
// qwen3.6:35b and qwen3.6:35b-a3b are distinct names for one digest). Any
// other response skips the model check -- it may be a non-Ollama
// Anthropic-compatible server.
func PreflightBackend(b config.BackendConfig) error {
	url := strings.TrimRight(b.BaseURL, "/") + "/api/tags"
	ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", url, err)
	}
	resp, err := backendHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("backend unreachable at %s: %w", b.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	var tags struct {
		Models *[]struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &tags) != nil || tags.Models == nil {
		return nil
	}
	var have []string
	for _, m := range *tags.Models {
		if sameModel(m.Name, b.Model) || sameModel(m.Model, b.Model) {
			return nil
		}
		have = append(have, m.Name)
	}
	avail := "none"
	if len(have) > 0 {
		avail = strings.Join(have, ", ")
	}
	return fmt.Errorf("model %q is not available at %s (available: %s)", b.Model, b.BaseURL, avail)
}

// normalizeModel appends Ollama's implicit ":latest" tag when the final path
// segment has no tag. Colons earlier in the name (registry host:port) are not
// tag separators. Comparison-only; never used for the name passed to --model.
func normalizeModel(name string) string {
	last := name[strings.LastIndex(name, "/")+1:]
	if strings.Contains(last, ":") {
		return name
	}
	return name + ":latest"
}

// sameModel reports whether two model names refer to the same Ollama model.
func sameModel(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return normalizeModel(a) == normalizeModel(b)
}
