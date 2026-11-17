package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EnvPiProvider carries the backend provider the pi bridge registers with
// pi.registerProvider at load, as JSON {"name", "config"}. It holds no
// secret: a stored key is fetched at request time by the "!command" apiKey.
const EnvPiProvider = "AGENTCTL_PI_PROVIDER"

// piProviderPrefix namespaces agentctl's providers so they never merge with a
// provider of the same name in the user's own ~/.pi/agent/models.json.
const piProviderPrefix = "agentctl-"

// piDefaultContextWindow is assumed when the backend reports none.
const piDefaultContextWindow = 131072

// piMaxOutputTokens caps a registered model's output limit.
const piMaxOutputTokens = 16384

type piProvider struct {
	Name   string           `json:"name"`
	Config piProviderConfig `json:"config"`
}

type piProviderConfig struct {
	BaseURL string    `json:"baseUrl"`
	API     string    `json:"api"`
	APIKey  string    `json:"apiKey"`
	Models  []piModel `json:"models"`
}

type piModel struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Reasoning     bool        `json:"reasoning"`
	Input         []string    `json:"input"`
	Cost          piModelCost `json:"cost"`
	ContextWindow int         `json:"contextWindow"`
	MaxTokens     int         `json:"maxTokens"`
}

// piModelCost is zero: agentctl prices sessions itself (internal/tokens).
type piModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// piBackendProvider returns the provider name, launch model and window env
// that make pi talk to b. Both Ollama and Nous Portal are reached through
// pi's OpenAI Chat Completions implementation at <base>/v1.
func piBackendProvider(b Backend) (provider, model string, env map[string]string, err error) {
	if b.Name == "" || len(b.Models) == 0 || b.Models[0] == "" {
		return "", "", nil, fmt.Errorf("pi: backend %q has no model", b.Name)
	}
	if b.BaseURL == "" {
		return "", "", nil, fmt.Errorf("pi: backend %q has no base URL", b.Name)
	}
	var apiKey string
	switch {
	case b.KeyCommand != "":
		apiKey = "!" + b.KeyCommand
	case b.AuthToken != "":
		apiKey = piLiteral(b.AuthToken)
	default:
		return "", "", nil, fmt.Errorf("pi: backend %q has no credential", b.Name)
	}
	ctx := b.ContextWindow
	if ctx <= 0 {
		ctx = piDefaultContextWindow
	}
	maxOut := min(piMaxOutputTokens, ctx/2)
	p := piProvider{
		Name: piProviderPrefix + b.Name,
		Config: piProviderConfig{
			BaseURL: strings.TrimRight(b.BaseURL, "/") + "/v1",
			API:     "openai-completions",
			APIKey:  apiKey,
		},
	}
	seen := map[string]bool{}
	for _, id := range b.Models {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		p.Config.Models = append(p.Config.Models, piModel{
			ID: id, Name: id, Input: []string{"text"}, ContextWindow: ctx, MaxTokens: maxOut,
		})
	}
	data, err := json.Marshal(p)
	if err != nil {
		return "", "", nil, fmt.Errorf("pi: encoding provider: %w", err)
	}
	return p.Name, b.Models[0], map[string]string{EnvPiProvider: string(data)}, nil
}

// piLiteral escapes s for pi's credential syntax, where "$NAME" interpolates
// an environment variable and a leading "!" runs a command.
func piLiteral(s string) string {
	s = strings.ReplaceAll(s, "$", "$$")
	if strings.HasPrefix(s, "!") {
		s = "$" + s
	}
	return s
}
