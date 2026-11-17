package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tokens"
)

// backendAnthropic is the first entry of the backend selector: no backend,
// plain Claude Code against Anthropic.
const backendAnthropic = "anthropic"

// anthropicModels are the model aliases offered for the Anthropic backend.
var anthropicModels = []string{launchopts.Inherit, "opus", "sonnet", "haiku"}

// effortChoices are the effort values offered for every agent.
var effortChoices = []string{launchopts.Inherit, "low", "medium", "high", "max"}

// safeModelRe is the charset allowed for a typed model name (the launch path
// shell-quotes it regardless).
var safeModelRe = regexp.MustCompile(`^[A-Za-z0-9._:/~\[\]-]+$`)

// modelOpt is one entry of the model selector.
type modelOpt struct {
	Value   string
	Default bool
	Note    string // short suffix such as "opus alias"
}

// agentOpts is the editable launch configuration for the highlighted agent.
type agentOpts struct {
	agent     string
	backends  []string // backendAnthropic first, then configured names (sorted)
	backend   string   // selected entry of backends
	model     string   // selected model value
	custom    string   // free-typed model, shown as an extra choice
	effort    string
	dir       string
	worktree  bool
	defaultBE string // backends entry the agent config selects
}

// newAgentOpts prefills the options for a from its effective config.
func (m MenuModel) newAgentOpts(a MenuAgent) agentOpts {
	names := make([]string, 0, len(m.cfg.Backends))
	for n := range m.cfg.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	o := agentOpts{
		agent:     a.Name,
		backends:  append([]string{backendAnthropic}, names...),
		defaultBE: backendAnthropic,
		effort:    launchopts.Inherit,
		dir:       a.Dir,
		worktree:  true,
	}
	if _, ok := m.cfg.Backends[a.Backend]; ok && a.Backend != "" {
		o.defaultBE = a.Backend
	}
	o.backend = o.defaultBE
	o.model = m.defaultModel(a, o.backend)
	if a.Effort != "" {
		o.effort = a.Effort
	}
	return o
}

// defaultModel is the model the agent would run on the given backend.
func (m MenuModel) defaultModel(a MenuAgent, backend string) string {
	if backend == backendAnthropic {
		if a.Backend == "" && a.Model != "" {
			return a.Model
		}
		return launchopts.Inherit
	}
	return m.cfg.Backends[backend].Model
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
// default entry comes first; the current free-typed value is appended when it
// is not already listed.
func (m MenuModel) modelChoices() []modelOpt {
	a, ok := m.current()
	if !ok {
		return nil
	}
	be := m.opts.backend
	var out []modelOpt
	add := func(v, note string, def bool) {
		if v == "" {
			return
		}
		for i := range out {
			if out[i].Value == v || (!m.isNous(be) && be != backendAnthropic && launchopts.SameModel(out[i].Value, v)) {
				if note != "" && out[i].Note == "" {
					out[i].Note = note
				}
				return
			}
		}
		out = append(out, modelOpt{Value: v, Default: def, Note: note})
	}
	def := m.defaultModel(a, be)
	switch {
	case be == backendAnthropic:
		add(def, "", true)
		for _, v := range anthropicModels {
			add(v, "", false)
		}
	case m.isNous(be):
		b := m.cfg.Backends[be]
		add(def, "", true)
		if b.Models != nil {
			add(b.Models.Opus, "opus alias", false)
			add(b.Models.Sonnet, "sonnet alias", false)
			add(b.Models.Haiku, "haiku alias", false)
			add(b.Models.Subagent, "subagent alias", false)
		}
		for _, id := range m.nousModels() {
			add(id, "", false)
		}
	default:
		add(def, "", true)
		if st := m.ollama[be]; st != nil && st.done && st.err == nil {
			for _, id := range st.models {
				add(id, "", false)
			}
		}
	}
	add(m.opts.custom, "custom", false)
	return out
}

// modelIndex returns the index of the selected model in choices (0 when absent).
func modelIndex(choices []modelOpt, sel string) int {
	for i, c := range choices {
		if c.Value == sel {
			return i
		}
	}
	return 0
}

// resultModel is the Request.Model for the current selection: "" lets the
// agent config / backend decide.
func (m MenuModel) resultModel(a MenuAgent) string {
	sel, be := m.opts.model, m.opts.backend
	if be == backendAnthropic {
		if a.Backend == "" && a.Model != "" && sel == a.Model {
			return ""
		}
		if sel == launchopts.Inherit {
			if a.Model != "" {
				return launchopts.Inherit
			}
			return ""
		}
		return sel
	}
	if sel == m.cfg.Backends[be].Model {
		return ""
	}
	return sel
}

// resultBackend is the Request.Backend for the current selection.
func (m MenuModel) resultBackend(a MenuAgent) string {
	if m.opts.backend != backendAnthropic {
		return m.opts.backend
	}
	if a.Backend != "" {
		return launchopts.BackendNone
	}
	return ""
}

// resultEffort is the Request.Effort for the current selection.
func (m MenuModel) resultEffort(a MenuAgent) string {
	sel := m.opts.effort
	if sel == a.Effort || (sel == launchopts.Inherit && a.Effort == "") {
		return ""
	}
	return sel
}

// effortOptions lists the effort values, adding a configured value outside the
// standard set so the default stays selectable.
func (m MenuModel) effortOptions() []string {
	out := slices.Clone(effortChoices)
	if a, ok := m.current(); ok && a.Effort != "" && !slices.Contains(out, a.Effort) {
		out = append(out, a.Effort)
	}
	return out
}

// validateModel applies the launch-time rules to a model value for backend:
// charset, and for nous an explicit model must be a provider/model slug
// (mirrors launchopts.ResolveBackend).
func (m MenuModel) validateModel(backend, value string) error {
	if value == "" || value == launchopts.Inherit {
		return nil
	}
	if !safeModelRe.MatchString(value) {
		return fmt.Errorf("model %q has invalid characters", value)
	}
	if m.isNous(backend) && !strings.Contains(value, "/") {
		return fmt.Errorf("model %q is not a Nous model slug (expected provider/model, e.g. %s)", value, m.cfg.Backends[backend].Model)
	}
	return nil
}

// validate checks the whole selection before launch.
func (m MenuModel) validate(a MenuAgent) error {
	if strings.TrimSpace(m.opts.dir) == "" {
		return fmt.Errorf("working directory is empty")
	}
	if err := launchopts.ValidateEffort(m.opts.effort); err != nil {
		return err
	}
	if rm := m.resultModel(a); rm != "" {
		return m.validateModel(m.opts.backend, rm)
	}
	return nil
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

// priceHint describes cost for the selected model: per-MTok price and billing
// class. Unknown prices are reported as unknown, never guessed.
func (m MenuModel) priceHint() string {
	be, model := m.opts.backend, m.opts.model
	var class sessions.Billing
	lookup := model
	if be == backendAnthropic {
		if model == launchopts.Inherit {
			lookup = ""
		}
		if lookup == "" || !strings.Contains(lookup, "/") {
			class = sessions.BillingSub
		}
	} else {
		class = sessions.ClassifyBilling(model, be, m.cfg.Backends)
	}
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

// priceNum formats a $/MTok figure compactly.
func priceNum(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}
