package launchopts

import (
	"slices"
	"sort"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tokens"
)

// BackendAnthropic is the first entry of the backend selector: no backend,
// plain Claude Code against Anthropic.
const BackendAnthropic = "anthropic"

// AnthropicModels are the model aliases offered for the Anthropic backend.
var AnthropicModels = []string{Inherit, "opus", "sonnet", "haiku", "fable"}

// EffortChoices are the effort values offered for every agent.
var EffortChoices = []string{Inherit, "low", "medium", "high", "max"}

// ModelNote is a model id with a short label (for example "opus alias").
type ModelNote struct {
	Model string
	Note  string
}

// BackendSpec is the part of a configured backend the choices depend on.
type BackendSpec struct {
	Nous  bool
	Model string // the backend's configured default model
	// Aliases are a Nous backend's alias models, in display order.
	Aliases []ModelNote
}

// ModelChoice is one entry of a model selector.
type ModelChoice struct {
	Value   string `json:"value"`
	Note    string `json:"note"`
	Default bool   `json:"default"`
}

// Inputs is everything the choice logic needs, with no I/O.
type Inputs struct {
	// Agent defaults from the effective agent config.
	AgentBackend string
	AgentModel   string
	AgentEffort  string
	// Backends is the configured backends map.
	Backends map[string]BackendSpec
	// AnthropicIDs are the known Anthropic model ids with labels.
	AnthropicIDs []ModelNote
	// NousModels is the cached Nous catalog.
	NousModels []string
	// OllamaModels holds the fetched model list per Ollama backend. A backend
	// absent from the map (not fetched, or the fetch failed) contributes only
	// its default model.
	OllamaModels map[string][]string
}

// BackendNames returns the selectable backends, BackendAnthropic first then
// the configured names sorted, and the default entry.
func (in Inputs) BackendNames() (names []string, def string) {
	rest := make([]string, 0, len(in.Backends))
	for n := range in.Backends {
		rest = append(rest, n)
	}
	sort.Strings(rest)
	names = append([]string{BackendAnthropic}, rest...)
	def = BackendAnthropic
	if _, ok := in.Backends[in.AgentBackend]; ok && in.AgentBackend != "" {
		def = in.AgentBackend
	}
	return names, def
}

// DefaultModel is the model the agent would run on backend.
func (in Inputs) DefaultModel(backend string) string {
	if backend == BackendAnthropic {
		if in.AgentBackend == "" && in.AgentModel != "" {
			return in.AgentModel
		}
		return Inherit
	}
	return in.Backends[backend].Model
}

// DefaultEffort is the effort the agent would run with.
func (in Inputs) DefaultEffort() string {
	if in.AgentEffort != "" {
		return in.AgentEffort
	}
	return Inherit
}

// IsNous reports whether backend is a Nous Portal backend.
func (in Inputs) IsNous(backend string) bool {
	b, ok := in.Backends[backend]
	return ok && b.Nous
}

// ModelChoices lists the selectable models for backend, default first.
func (in Inputs) ModelChoices(backend string) []ModelChoice {
	var out []ModelChoice
	nous := in.IsNous(backend)
	add := func(v, note string, def bool) {
		if v == "" {
			return
		}
		for i := range out {
			if out[i].Value == v || (!nous && backend != BackendAnthropic && SameModel(out[i].Value, v)) {
				if note != "" && out[i].Note == "" {
					out[i].Note = note
				}
				return
			}
		}
		out = append(out, ModelChoice{Value: v, Default: def, Note: note})
	}
	def := in.DefaultModel(backend)
	switch {
	case backend == BackendAnthropic:
		add(def, "", true)
		for _, v := range AnthropicModels {
			add(v, "alias", false)
		}
		for _, id := range in.AnthropicIDs {
			add(id.Model, id.Note, false)
		}
	case nous:
		add(def, "", true)
		for _, a := range in.Backends[backend].Aliases {
			add(a.Model, a.Note, false)
		}
		for _, id := range in.NousModels {
			add(id, "", false)
		}
	default:
		add(def, "", true)
		for _, id := range in.OllamaModels[backend] {
			add(id, "", false)
		}
	}
	return out
}

// EffortOptions lists the effort values, adding a configured value outside the
// standard set so the default stays selectable.
func (in Inputs) EffortOptions() []string {
	out := slices.Clone(EffortChoices)
	if in.AgentEffort != "" && !slices.Contains(out, in.AgentEffort) {
		out = append(out, in.AgentEffort)
	}
	return out
}

// SpecsFromConfig converts the configured backends into BackendSpecs.
func SpecsFromConfig(backends map[string]config.BackendConfig) map[string]BackendSpec {
	out := make(map[string]BackendSpec, len(backends))
	for name, b := range backends {
		s := BackendSpec{Nous: b.IsNous(), Model: b.Model}
		if b.IsNous() && b.Models != nil {
			s.Aliases = []ModelNote{
				{b.Models.Opus, "opus alias"},
				{b.Models.Sonnet, "sonnet alias"},
				{b.Models.Haiku, "haiku alias"},
				{b.Models.Subagent, "subagent alias"},
			}
		}
		out[name] = s
	}
	return out
}

// AnthropicIDNotes lists the known Anthropic model ids with their labels.
func AnthropicIDNotes() []ModelNote {
	ids := tokens.AnthropicModelIDs()
	out := make([]ModelNote, 0, len(ids))
	for _, id := range ids {
		out = append(out, ModelNote{Model: id, Note: tokens.Lookup(id).Label})
	}
	return out
}

// ResultBackend is the dispatch Backend for the selected entry of
// BackendNames: "" lets the agent config decide, BackendNone forces Anthropic
// over a configured agent backend, else the backend name.
func (in Inputs) ResultBackend(selected string) string {
	if selected != BackendAnthropic {
		return selected
	}
	if in.AgentBackend != "" {
		return BackendNone
	}
	return ""
}

// ResultModel is the dispatch Model for selecting model on backend: "" lets
// the agent config / backend decide.
func (in Inputs) ResultModel(backend, model string) string {
	if backend == BackendAnthropic {
		if in.AgentBackend == "" && in.AgentModel != "" && model == in.AgentModel {
			return ""
		}
		if model == Inherit {
			if in.AgentModel != "" {
				return Inherit
			}
			return ""
		}
		return model
	}
	if model == in.Backends[backend].Model {
		return ""
	}
	return model
}

// ResultEffort is the dispatch Effort for selecting effort.
func (in Inputs) ResultEffort(effort string) string {
	if effort == in.AgentEffort || (effort == Inherit && in.AgentEffort == "") {
		return ""
	}
	return effort
}
