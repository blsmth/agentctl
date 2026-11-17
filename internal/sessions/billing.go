package sessions

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tokens"
)

// Billing says how a session's cost is actually paid.
type Billing string

const (
	// BillingMetered is real money per token (Nous Portal).
	BillingMetered Billing = "metered"
	// BillingSub is Anthropic via the Claude subscription: the dollar figure
	// is the API-equivalent value, not a charge.
	BillingSub Billing = "sub"
	// BillingFree is a local backend (Ollama): always $0.
	BillingFree Billing = "free"
	// BillingUnknown means neither the backend nor the model identify a class.
	BillingUnknown Billing = ""
)

// String renders the class for tables; "?" when unknown.
func (b Billing) String() string {
	if b == BillingUnknown {
		return "?"
	}
	return string(b)
}

// ClassifyBilling decides a session's billing class. backend is the config
// backend name the session ran against ("" = none recorded, i.e. Anthropic);
// backends is the global `backends` config (may be nil). Without a backend
// the model decides: claude-* is subscription, a model priced from the Nous
// catalog is metered, a bare non-Claude tag with an Ollama backend configured
// is free (legacy inference), anything else is unknown.
func ClassifyBilling(model, backend string, backends map[string]config.BackendConfig) Billing {
	if backend != "" {
		if b, ok := backends[backend]; ok {
			if b.IsNous() {
				return BillingMetered
			}
			return BillingFree // ollama (kind "" or "ollama")
		}
		// Backend no longer in config: fall through to the model.
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(m, "claude-") {
		return BillingSub
	}
	if src := tokens.Lookup(model).Source; src == "catalog" || src == "free" {
		return BillingMetered
	}
	// Sessions recorded before backends were tracked: a bare local-style tag
	// ("qwen3.6:35b-a3b") is not a Nous catalog id (those are "provider/model")
	// nor a Claude id, so when an Ollama backend is configured it was run there.
	if m != "" && !strings.Contains(m, "/") && hasOllamaBackend(backends) {
		return BillingFree
	}
	return BillingUnknown
}

func hasOllamaBackend(backends map[string]config.BackendConfig) bool {
	for _, b := range backends {
		if !b.IsNous() {
			return true
		}
	}
	return false
}

// backendHintDir holds one tiny file per session recording the backend it was
// launched on. Windowstate cannot serve this purpose: its file is deleted when
// the window goes away, but sessions are priced long afterwards. The directory
// name starts with "_" so it never collides with an encoded project dir, and
// readMetasFromDir ignores anything that is not *.meta.json.
func backendHintDir() string { return filepath.Join(sessionDataDir(), "_backend") }

// RecordBackend remembers that sessionID ran on the named config backend.
// An empty backend (Anthropic) is not recorded: absence means "none".
func RecordBackend(sessionID, backend string) error {
	if sessionID == "" || backend == "" || strings.ContainsAny(sessionID, `/\`) {
		return nil
	}
	dir := backendHintDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sessionID), []byte(backend), 0o644)
}

// RecordedBackend returns the backend recorded for sessionID, or "".
func RecordedBackend(sessionID string) string {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(backendHintDir(), sessionID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// BillingFor classifies session s priced at model. A Claude model run
// through another harness (pi) is not covered by the Claude subscription:
// that harness calls the API with its own credentials, so it is metered.
func BillingFor(s Session, model string, backends map[string]config.BackendConfig) Billing {
	if s.Harness != "" && s.Harness != "claude" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-") {
		return BillingMetered
	}
	return ClassifyBilling(model, RecordedBackend(s.ID), backends)
}
