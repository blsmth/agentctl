package cronedit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/tokens"
)

// Options is what a job's pinned launch fields (model, backend, account) may
// take on this machine. It is in-process data only (config and the cached
// catalogs); nothing here touches the network.
type Options struct {
	// Backends are the configured backend names (not "anthropic").
	Backends []string
	// Accounts are the configured account names.
	Accounts []string
	// DefaultAccount is default_account ("" when unset).
	DefaultAccount string
	// Models maps a backend ("anthropic" or a configured name) to the models
	// it is known to offer. A backend absent from the map has no list.
	Models map[string][]string
	// AgentBackend is the backend an agent launches on when the job pins
	// none ("" = anthropic). nil means anthropic.
	AgentBackend func(agent string) string
	// AgentModel is the model an agent launches with when the job pins none
	// ("" = the backend's own default). nil means none.
	AgentModel func(agent string) string
}

// DefaultOptions reads the options from the effective configuration.
func DefaultOptions() Options {
	cfg := config.Load()
	in := launchopts.Inputs{
		Backends:     launchopts.SpecsFromConfig(cfg.Backends),
		AnthropicIDs: launchopts.AnthropicIDNotes(),
		NousModels:   tokens.CatalogModelIDs(),
		OllamaModels: map[string][]string{},
	}
	names, _ := in.BackendNames()
	o := Options{
		DefaultAccount: cfg.DefaultAccount,
		Models:         map[string][]string{},
		AgentBackend:   func(agent string) string { return cfg.ForAgent(agent).Backend },
		AgentModel:     func(agent string) string { return cfg.ForAgent(agent).Model },
	}
	for _, n := range names {
		if n != launchopts.BackendAnthropic {
			o.Backends = append(o.Backends, n)
		}
		for _, m := range in.ModelChoices(n) {
			if m.Value != launchopts.Inherit {
				o.Models[n] = append(o.Models[n], m.Value)
			}
		}
	}
	for a := range cfg.Accounts {
		o.Accounts = append(o.Accounts, a)
	}
	slices.Sort(o.Accounts)
	return o
}

// EffectiveBackend is the backend the job runs on: its own pin, else its
// agent's, else anthropic.
func (o Options) EffectiveBackend(j cron.Job) string {
	switch b := j.Backend; {
	case b == launchopts.BackendNone || b == launchopts.BackendAnthropic:
		return launchopts.BackendAnthropic
	case b != "":
		return b
	}
	if o.AgentBackend != nil {
		if b := o.AgentBackend(j.Agent); b != "" && slices.Contains(o.Backends, b) {
			return b
		}
	}
	return launchopts.BackendAnthropic
}

// check validates the pinned model, backend and account of a job whose own
// fields are well-formed. Errors are values that cannot be launched (an
// unconfigured backend or account, an Anthropic model that is neither an
// alias nor a model id); warnings are values that merely are not in a list
// that cannot be complete (a Nous or Ollama model: the catalog is cached and
// the Ollama list is live, so a model outside it may still exist).
func (o Options) check(j cron.Job) (errs, warns []cron.FieldError) {
	add := func(dst *[]cron.FieldError, field, format string, args ...any) {
		*dst = append(*dst, cron.FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	backendOK := true
	if j.Backend != "" && j.Backend != launchopts.BackendNone && j.Backend != launchopts.BackendAnthropic &&
		!slices.Contains(o.Backends, j.Backend) {
		backendOK = false
		if len(o.Backends) == 0 {
			add(&errs, cron.FieldBackend, "unknown backend %q: none are configured", j.Backend)
		} else {
			add(&errs, cron.FieldBackend, "unknown backend %q: use %s", j.Backend, strings.Join(o.Backends, ", "))
		}
	}
	if j.Account != "" && !slices.Contains(o.Accounts, j.Account) {
		if len(o.Accounts) == 0 {
			add(&errs, cron.FieldAccount, "unknown account %q: none are configured", j.Account)
		} else {
			add(&errs, cron.FieldAccount, "unknown account %q: use %s", j.Account, strings.Join(o.Accounts, ", "))
		}
	}
	if j.Model == "" || j.Model == launchopts.Inherit || !backendOK {
		return errs, warns
	}
	backend := o.EffectiveBackend(j)
	base := baseModel(j.Model)
	known := slices.Contains(o.Models[backend], j.Model) || slices.Contains(o.Models[backend], base)
	switch {
	case known:
	case backend == launchopts.BackendAnthropic:
		// `claude --model` takes any full model id, so a claude-* id outside
		// the built-in registry is allowed; anything else must be an alias.
		if !strings.HasPrefix(base, "claude-") {
			add(&errs, cron.FieldModel, "unknown model %q: use %s, or a claude-… id", j.Model, strings.Join(launchopts.AnthropicModels[1:], ", "))
		}
	default:
		add(&warns, cron.FieldModel, "%q is not in the model list of backend %s; check the name", j.Model, backend)
	}
	return errs, warns
}

// baseModel drops a context-size suffix such as "opus[1m]".
func baseModel(m string) string {
	if i := strings.Index(m, "["); i > 0 && strings.HasSuffix(m, "]") {
		return m[:i]
	}
	return m
}

func (s Store) options() (Options, bool) {
	if s.Options == nil {
		return Options{}, false
	}
	return s.Options(), true
}

// optionErrors is the option check's errors for a normalized job.
func (s Store) optionErrors(j cron.Job) []cron.FieldError {
	o, ok := s.options()
	if !ok {
		return nil
	}
	errs, _ := o.check(j)
	return errs
}

// Warnings lists the non-blocking problems of a job (a model outside a list
// that cannot be complete).
func (s Store) Warnings(j cron.Job) []cron.FieldError {
	o, ok := s.options()
	if !ok {
		return []cron.FieldError{}
	}
	_, warns := o.check(normalize(j))
	if warns == nil {
		warns = []cron.FieldError{}
	}
	return warns
}
