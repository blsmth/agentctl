package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tokens"
)

// backendAnthropic is the first entry of the backend selector: no backend,
// plain Claude Code against Anthropic.
const backendAnthropic = launchopts.BackendAnthropic


// modelOpt is one entry of the model selector.
type modelOpt = launchopts.ModelChoice

// agentOpts is the editable launch configuration for the highlighted agent.
type agentOpts struct {
	agent     string
	backends  []string // backendAnthropic first, then configured names (sorted)
	backend   string   // selected entry of backends
	model     string   // selected model value
	effort    string
	dir       string
	worktree  bool
	defaultBE string // backends entry the agent config selects
}

// inputs gathers the pure choice inputs for a from config and the fetched
// model lists.
func (m MenuModel) inputs(a MenuAgent) launchopts.Inputs {
	in := launchopts.Inputs{
		AgentBackend: a.Backend,
		AgentModel:   a.Model,
		AgentEffort:  a.Effort,
		Backends:     launchopts.SpecsFromConfig(m.cfg.Backends),
		AnthropicIDs: launchopts.AnthropicIDNotes(),
		NousModels:   m.nousModels(),
		OllamaModels: map[string][]string{},
	}
	for be, st := range m.ollama {
		if st != nil && st.done && st.err == nil {
			in.OllamaModels[be] = st.models
		}
	}
	return in
}

// newAgentOpts prefills the options for a from its effective config.
func (m MenuModel) newAgentOpts(a MenuAgent) agentOpts {
	in := m.inputs(a)
	names, def := in.BackendNames()
	return agentOpts{
		agent:     a.Name,
		backends:  names,
		defaultBE: def,
		backend:   def,
		model:     in.DefaultModel(def),
		effort:    in.DefaultEffort(),
		dir:       a.Dir,
		worktree:  true,
	}
}

// defaultModel is the model the agent would run on the given backend.
func (m MenuModel) defaultModel(a MenuAgent, backend string) string {
	return m.inputs(a).DefaultModel(backend)
}

// isNous reports whether the named backend is a Nous Portal backend.
func (m MenuModel) isNous(backend string) bool {
	b, ok := m.cfg.Backends[backend]
	return ok && b.IsNous()
}

// nousModels returns the cached Nous catalog ids.
func (m MenuModel) nousModels() []string {
	if m.cfg.NousModels != nil {
		return m.cfg.NousModels()
	}
	return tokens.CatalogModelIDs()
}

// modelChoices lists the selectable models for the current backend. The
// default entry comes first.
func (m MenuModel) modelChoices() []modelOpt {
	a, ok := m.current()
	if !ok {
		return nil
	}
	return m.inputs(a).ModelChoices(m.opts.backend)
}

// resultModel is the Request.Model for the current selection: "" lets the
// agent config / backend decide.
func (m MenuModel) resultModel(a MenuAgent) string {
	return m.inputs(a).ResultModel(m.opts.backend, m.opts.model)
}

// resultBackend is the Request.Backend for the current selection.
func (m MenuModel) resultBackend(a MenuAgent) string {
	return m.inputs(a).ResultBackend(m.opts.backend)
}

// resultEffort is the Request.Effort for the current selection.
func (m MenuModel) resultEffort(a MenuAgent) string {
	return m.inputs(a).ResultEffort(m.opts.effort)
}

// effortOptions lists the effort values, adding a configured value outside the
// standard set so the default stays selectable.
func (m MenuModel) effortOptions() []string {
	a, _ := m.current()
	return m.inputs(a).EffortOptions()
}

// validateModel applies the launch-time rules to a model value for backend:
// charset, and for nous an explicit model must be a provider/model slug
// (mirrors launchopts.ResolveBackend).
func (m MenuModel) validateModel(backend, value string) error {
	if value == "" || value == launchopts.Inherit {
		return nil
	}
	if !launchopts.ValidModel(value) {
		return fmt.Errorf("model %q has invalid characters", value)
	}
	if m.isNous(backend) && !strings.Contains(value, "/") {
		return fmt.Errorf("model %q is not a Nous model slug (expected provider/model, e.g. %s)", value, m.cfg.Backends[backend].Model)
	}
	return nil
}

// validate checks the whole selection before launch and returns the options
// field the first error belongs to.
func (m MenuModel) validate(a MenuAgent) (optField, error) {
	if strings.TrimSpace(m.opts.dir) == "" {
		return fDir, fmt.Errorf("working directory is empty")
	}
	if err := launchopts.ValidateEffort(m.opts.effort); err != nil {
		return fEffort, err
	}
	if rm := m.resultModel(a); rm != "" {
		return fModel, m.validateModel(m.opts.backend, rm)
	}
	return fModel, nil
}

// expandHome expands a leading "~" in dir.
func expandHome(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	return dir
}

// modelWarning explains why the selected Ollama model may not launch.
func (m MenuModel) modelWarning() string {
	be := m.opts.backend
	b, ok := m.cfg.Backends[be]
	if !ok || be == backendAnthropic || b.IsNous() {
		return ""
	}
	st := m.ollama[be]
	if st == nil || !st.done || st.err != nil {
		return ""
	}
	for _, id := range st.models {
		if launchopts.SameModel(id, m.opts.model) {
			return ""
		}
	}
	return fmt.Sprintf("%s is not on the server; launch preflight will fail", m.opts.model)
}

// billingFor classifies the billing of model on backend and returns the
// model id to price ("" when there is nothing to look up).
func (m MenuModel) billingFor(be, model string) (sessions.Billing, string) {
	var class sessions.Billing
	lookup := model
	if be == backendAnthropic {
		if model == launchopts.Inherit {
			lookup = ""
		}
		if lookup == "" || !strings.Contains(lookup, "/") {
			class = sessions.BillingSub
		}
		return class, lookup
	}
	return sessions.ClassifyBilling(model, be, m.cfg.Backends), lookup
}

// priceHint describes cost for the selected model: per-MTok price and billing
// class. Unknown prices are reported as unknown, never guessed.
func (m MenuModel) priceHint() string {
	class, lookup := m.billingFor(m.opts.backend, m.opts.model)
	label := class.String()
	switch class {
	case sessions.BillingFree:
		return "$0 · free (local)"
	case sessions.BillingUnknown:
		label = "billing unknown"
	}
	if lookup != "" {
		if p := tokens.Lookup(lookup); p.Source != "family" && p.HasPricing() && p.Known {
			return fmt.Sprintf("$%s in / $%s out per MTok · %s", priceNum(p.Pricing.InputPerMTok), priceNum(p.Pricing.OutputPerMTok), label)
		}
	}
	return "price unknown · " + label
}

// priceShort is the compact per-row price of the model picker: "free",
// "sub" (covered by a subscription), "$in/$out" per MTok, or "" when unknown.
func (m MenuModel) priceShort(be, model string) string {
	class, lookup := m.billingFor(be, model)
	if class == sessions.BillingFree {
		return "free"
	}
	if lookup != "" {
		if p := tokens.Lookup(lookup); p.Source != "family" && p.HasPricing() && p.Known {
			return "$" + priceNum(p.Pricing.InputPerMTok) + "/$" + priceNum(p.Pricing.OutputPerMTok)
		}
	}
	if class == sessions.BillingSub {
		return "sub"
	}
	return ""
}

// priceNum formats a $/MTok figure compactly.
func priceNum(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}
