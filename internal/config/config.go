package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/agentname"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/tokens"
)

// AlertConfig holds per-transition-type notification toggles.
type AlertConfig struct {
	Blocked bool `json:"blocked"` // Working->Blocked (needs approval)
	Idle    bool `json:"idle"`    // Working->Idle (done)
	Gone    bool `json:"gone"`    // *->Gone (session ended); no hook-driven notification uses this yet
}

// HooksConfig maps lifecycle event names to ordered lists of shell commands.
// Each command is executed via "sh -c" with relevant env vars injected.
// Supported event keys: session_start, session_end, notification, stop,
// stop_failure, pre_tool, post_tool, dispatch_complete.
type HooksConfig map[string][]string

// DispatchConfig holds configuration for the dispatch system.
type DispatchConfig struct {
	// OnComplete is an optional shell command that runs when a dispatched agent
	// signals completion. Receives AGENTCTL_ISSUE, AGENTCTL_AGENT,
	// AGENTCTL_BRANCH, AGENTCTL_WORKDIR, AGENTCTL_PR, and AGENTCTL_WINDOW_ID
	// as environment variables.
	//
	// Deprecated: use hooks.dispatch_complete instead. OnComplete is still
	// honoured as a fallback when hooks.dispatch_complete is empty.
	OnComplete string `json:"on_complete,omitempty"`
}

// AgentConfig holds per-agent settings.
type AgentConfig struct {
	Alerts             bool        `json:"alerts"`
	AlertConfig        AlertConfig `json:"alert_config"`
	AutoImprove        bool        `json:"auto_improve"`                  // trigger improvement session on session end
	CrossContext       bool        `json:"cross_context"`                 // inject cross-agent summary at launch
	Dir                string      `json:"dir,omitempty"`                 // fixed working directory, empty = portable
	Model              string      `json:"model,omitempty"`               // "sonnet", "opus", "haiku", "" = inherit
	EffortLevel        string      `json:"effort_level,omitempty"`        // "low", "medium", "high", "max", "auto"
	PermissionMode     string      `json:"permission_mode,omitempty"`     // claude --permission-mode for dispatched workers; "" = auto
	MaxThinkingTokens  int         `json:"max_thinking_tokens,omitempty"` // 0 = use default
	BlockedCommands    []string    `json:"blocked_commands,omitempty"`    // substrings to block in Bash commands
	AllowedPermissions []string    `json:"allowed_permissions,omitempty"` // permission globs to allow (e.g. "WebFetch(*)")
	BlockedPatterns    []string    `json:"blocked_patterns,omitempty"`    // regex patterns to block
	AutoAccept         bool        `json:"auto_accept,omitempty"`         // auto-accept all permission prompts via PreToolUse hook
	// ContextKeys pins specific context-store entries to this agent. When
	// cross_context is true, these keys are always injected at launch in
	// addition to the agent-scoped entries.
	ContextKeys []string `json:"context_keys,omitempty"`
	// Dispatch holds per-agent dispatch settings. A nil value means inherit
	// from the global dispatch config.
	Dispatch *DispatchConfig `json:"dispatch,omitempty"`
	// Hooks holds per-agent lifecycle hook overrides. Commands here run after
	// global hooks for the same event.
	Hooks HooksConfig `json:"hooks,omitempty"`
	// WorkJournal enables auto-journaling of session summaries on SessionEnd via the configured llm provider.
	// Opt-in because it makes an API call (~$0.001/session) on every session end.
	WorkJournal bool `json:"work_journal,omitempty"`
	// AutoReclaimWorktree controls whether SessionEnd reclaims this agent's
	// worktree automatically when its session ends for a terminal reason and
	// Classify judges it safe (empty or landed -- see internal/cleanup).
	// nil (unset) means enabled; only an explicit false disables it. Use
	// EffectiveAutoReclaimWorktree to read the effective value.
	AutoReclaimWorktree *bool `json:"auto_reclaim_worktree,omitempty"`
	// BashPreview overrides the global bash_preview setting for this agent --
	// e.g. a global true with a per-agent false exempts one agent (typically
	// an unattended dispatched worker) from the popup. nil (unset) means
	// "inherit the global setting". Use EffectiveBashPreview to read the
	// effective value.
	BashPreview *bool `json:"bash_preview,omitempty"`
}

// BasicAuthConfig holds HTTP basic authentication credentials.
type BasicAuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// MetricsConfig holds configuration for the push-based metrics system.
type MetricsConfig struct {
	Enabled      bool              `json:"enabled"`
	Endpoint     string            `json:"endpoint,omitempty"`      // remote write URL
	PushInterval string            `json:"push_interval,omitempty"` // Go duration, default "15s"
	Port         int               `json:"port,omitempty"`          // local /metrics port, default 9101
	Labels       map[string]string `json:"labels,omitempty"`        // static labels (e.g. instance)
	BasicAuth    *BasicAuthConfig  `json:"basic_auth,omitempty"`
}

// EffectivePushInterval parses PushInterval as a Go duration. Returns 15s if
// the field is empty or unparseable.
func (m MetricsConfig) EffectivePushInterval() time.Duration {
	if m.PushInterval != "" {
		if d, err := time.ParseDuration(m.PushInterval); err == nil {
			return d
		}
	}
	return 15 * time.Second
}

// EffectivePort returns the configured metrics port, defaulting to 9101.
func (m MetricsConfig) EffectivePort() int {
	if m.Port > 0 {
		return m.Port
	}
	return 9101
}

// HasRemoteWrite reports whether a remote write endpoint is configured.
func (m MetricsConfig) HasRemoteWrite() bool {
	return m.Endpoint != ""
}

// ModelOverride is a partial override for a single model's registry entry.
// A zero/empty field means "don't override" — fall through to the built-in
// or family default. See internal/tokens for the resolution algorithm.
type ModelOverride struct {
	ContextWindow      int     `json:"context_window,omitempty"`
	MaxOutput          int     `json:"max_output,omitempty"`
	Short              string  `json:"short,omitempty"`
	InputPerMTok       float64 `json:"input_per_mtok,omitempty"`
	OutputPerMTok      float64 `json:"output_per_mtok,omitempty"`
	CacheReadPerMTok   float64 `json:"cache_read_per_mtok,omitempty"`
	CacheCreatePerMTok float64 `json:"cache_create_per_mtok,omitempty"`
}

// ModelsConfig holds model-registry configuration: overrides for teaching
// agentctl about new models without a release, plus context-accounting
// thresholds.
type ModelsConfig struct {
	// DefaultContextWindow overrides tokens.DefaultContextWindow for models
	// that resolve to nothing at all (no override, no built-in match, no
	// family fallback).
	DefaultContextWindow int `json:"default_context_window,omitempty"`
	// ContextWarnPct is the ctx% at which the warning threshold fires. Drives
	// the chrome bar's context toast and battery-gauge coloring. Defaults to
	// 75 when unset.
	ContextWarnPct int `json:"context_warn_pct,omitempty"`
	// ContextCriticalPct is the ctx% at which the critical threshold fires.
	// Drives the chrome bar's context toast and battery-gauge coloring.
	// Defaults to 90 when unset.
	ContextCriticalPct int `json:"context_critical_pct,omitempty"`
	// ReserveOutput controls whether the usable context window subtracts the
	// model's max-output reserve. Drives tokens.UsableWindow. Defaults to
	// true when unset (nil).
	ReserveOutput *bool `json:"reserve_output,omitempty"`
	// Overrides maps model name (or prefix) to a partial override. Merged
	// per-key (not whole-map-replace) so a project config can teach one
	// model without redeclaring all of them.
	Overrides map[string]ModelOverride `json:"overrides,omitempty"`
}

// defaultContextWarnPct and defaultContextCriticalPct are the fallback
// thresholds (in percent of usable context) used when ModelsConfig doesn't
// set ContextWarnPct / ContextCriticalPct, or when the configured pair is
// nonsensical (see clampContextPct below).
const (
	defaultContextWarnPct     = 75
	defaultContextCriticalPct = 90

	// minContextPct / maxContextPct bound the valid range for both
	// thresholds. minContextPct is 10 (not e.g. 1) because
	// internal/tui/chrome_bar.go re-arms the warn latch once ctxPct drops
	// below warnPct-10; anything lower than 10 makes that hysteresis window
	// go negative, so the toast would never re-arm.
	minContextPct = 10
	maxContextPct = 100
)

// clampContextPct clamps pct to [minContextPct, maxContextPct].
func clampContextPct(pct int) int {
	if pct < minContextPct {
		return minContextPct
	}
	if pct > maxContextPct {
		return maxContextPct
	}
	return pct
}

// EffectiveContextWarnPct returns the configured warn threshold, clamped to
// [10, 100] and defaulting to 75 when unset. If the clamped warn threshold
// would be greater than or equal to the clamped critical threshold, both
// fall back to their defaults (75/90) so the pair stays coherent.
func (m ModelsConfig) EffectiveContextWarnPct() int {
	warn, _ := m.effectiveContextPcts()
	return warn
}

// EffectiveContextCriticalPct returns the configured critical threshold,
// clamped to [10, 100] and defaulting to 90 when unset. If the clamped
// critical threshold would be less than or equal to the clamped warn
// threshold, both fall back to their defaults (75/90) so the pair stays
// coherent.
func (m ModelsConfig) EffectiveContextCriticalPct() int {
	_, crit := m.effectiveContextPcts()
	return crit
}

// effectiveContextPcts computes the clamped, coherent warn/critical pair
// shared by EffectiveContextWarnPct and EffectiveContextCriticalPct.
func (m ModelsConfig) effectiveContextPcts() (warn, crit int) {
	warn = defaultContextWarnPct
	if m.ContextWarnPct > 0 {
		warn = clampContextPct(m.ContextWarnPct)
	}
	crit = defaultContextCriticalPct
	if m.ContextCriticalPct > 0 {
		crit = clampContextPct(m.ContextCriticalPct)
	}
	if warn >= crit {
		return defaultContextWarnPct, defaultContextCriticalPct
	}
	return warn, crit
}

// EffectiveReserveOutput returns whether the usable context window should
// subtract the model's max-output reserve, defaulting to true when unset.
func (m ModelsConfig) EffectiveReserveOutput() bool {
	if m.ReserveOutput == nil {
		return true
	}
	return *m.ReserveOutput
}

// LLMConfig configures the local model provider used for text completion
// (currently: work journal summaries).
type LLMConfig struct {
	// Provider selects the backend. "ollama", "claude" (the retained internal/claude subprocess
	// provider; explicit opt-in only), or "" (disabled).
	Provider string       `json:"provider,omitempty"`
	Ollama   OllamaConfig `json:"ollama,omitempty"`
}

// OllamaConfig holds settings for the Ollama-backed LLM provider
// (internal/llm). See llm.New and llm.OllamaClient for how these are used.
type OllamaConfig struct {
	Host       string `json:"host,omitempty"`        // default "http://localhost:11434"
	Model      string `json:"model,omitempty"`       // required; no default
	NumCtx     int    `json:"num_ctx,omitempty"`     // default 8192
	NumPredict int    `json:"num_predict,omitempty"` // default 512
	KeepAlive  string `json:"keep_alive,omitempty"`  // default "30m"
	Timeout    string `json:"timeout,omitempty"`     // Go duration, default "30s"
}

// Default values applied by EffectiveLLM. Model is deliberately absent --
// see EffectiveLLM's doc comment.
const (
	defaultOllamaHost       = "http://localhost:11434"
	defaultOllamaNumCtx     = 8192
	defaultOllamaNumPredict = 512
	defaultOllamaKeepAlive  = "30m"
	defaultOllamaTimeout    = "30s"
)

// EffectiveLLM returns the configured LLMConfig with every Ollama default
// applied (Host, NumCtx, NumPredict, KeepAlive, Timeout). Model has no
// default on purpose: guessing a model name that is not pulled produces a
// confusing 404 from Ollama rather than a clear error. This does not mutate
// c; callers get a defaulted copy.
func (c Config) EffectiveLLM() LLMConfig {
	l := c.LLM
	if l.Ollama.Host == "" {
		l.Ollama.Host = defaultOllamaHost
	}
	if l.Ollama.NumCtx == 0 {
		l.Ollama.NumCtx = defaultOllamaNumCtx
	}
	if l.Ollama.NumPredict == 0 {
		l.Ollama.NumPredict = defaultOllamaNumPredict
	}
	if l.Ollama.KeepAlive == "" {
		l.Ollama.KeepAlive = defaultOllamaKeepAlive
	}
	if l.Ollama.Timeout == "" {
		l.Ollama.Timeout = defaultOllamaTimeout
	}
	return l
}

// RateLimitConfig holds settings for the account-level rate limit display.
type RateLimitConfig struct {
	// TokenCap is the estimated max tokens (input+output, excluding cache)
	// per 5-hour window. Default 88000. User can tune this based on their plan.
	TokenCap int `json:"token_cap,omitempty"`
}

// EffectiveTokenCap returns the configured token cap, defaulting to 88000.
func (r RateLimitConfig) EffectiveTokenCap() int {
	if r.TokenCap > 0 {
		return r.TokenCap
	}
	return 88000
}

// Config is the top-level configuration for agentctl.
type Config struct {
	// NotifyCmd is the shell command template used for desktop notifications.
	// Template variables: {{title}} (agent name), {{message}} (notification text).
	// nil (unset) uses the platform default (alerter on macOS, notify-send on
	// Linux when installed); an explicit empty string disables notifications.
	// Executed with sh -c, so it is honored from the global config only; the
	// project-local layer cannot set it. Use EffectiveNotifyCmd to read it.
	NotifyCmd *string `json:"notify_cmd,omitempty"`
	// Agents maps agent names to their per-agent settings.
	Agents  map[string]AgentConfig `json:"agents"`
	Metrics MetricsConfig          `json:"metrics,omitempty"`
	// AutoPoll enables the ticket browser autopilot mode.
	AutoPoll bool `json:"auto_poll"`
	// AutoPollInterval is the poll interval as a Go duration string (e.g. "5m"). Defaults to "5m".
	AutoPollInterval string `json:"auto_poll_interval,omitempty"`
	// Theme is the name of the active color theme. Defaults to "catppuccin-mocha".
	Theme string `json:"theme,omitempty"`
	// DefaultAgent is the agent launched by the quick-launch keybinding (prefix+A).
	// A project-local value overrides the global one when it passes
	// agent.ValidLaunchName (checked in sanitizeProjectConfig); the launch path
	// applies the remaining checks (no repo-only agent, no auto-approving
	// agent) and falls back to GlobalDefaultAgent when they fail.
	DefaultAgent string `json:"default_agent,omitempty"`
	// GlobalDefaultAgent is default_agent from the global layer alone, set by
	// Load. When DefaultAgent differs from it, DefaultAgent came from the
	// project-local config and is untrusted. Never serialized.
	GlobalDefaultAgent string `json:"-"`
	// Dispatch holds global dispatch configuration.
	Dispatch DispatchConfig `json:"dispatch,omitempty"`
	// SessionRetentionDays is how many days to keep session metadata before purging.
	// Defaults to 30 when zero.
	SessionRetentionDays int `json:"session_retention_days,omitempty"`
	// RestoreMaxAge is a Go duration string: the oldest saved state.json that
	// `agentctl restore` will still restore agents from. Defaults to "168h".
	// "0" or a negative value disables the gate. Global config only. Use
	// EffectiveRestoreMaxAge to read it.
	RestoreMaxAge string `json:"restore_max_age,omitempty"`
	// Hooks maps lifecycle event names to lists of shell commands. Commands run
	// after agentctl's built-in handling for each event. Per-agent hooks in
	// agents.<name>.hooks run after these global hooks.
	Hooks HooksConfig `json:"hooks,omitempty"`
	// RateLimit holds settings for the account-level rate limit display.
	RateLimit RateLimitConfig `json:"rate_limit,omitempty"`
	// WorkJournal is the global default for work journaling. When true, all
	// agents will journal session summaries unless overridden per-agent.
	WorkJournal bool `json:"work_journal,omitempty"`
	// Models holds model-registry overrides and context-accounting settings.
	Models ModelsConfig `json:"models,omitempty"`
	// OpenPRInBrowser controls whether a newly-created PR is opened in the
	// browser when a dispatched agent finishes. nil (unset) means enabled;
	// only an explicit false disables it. Use OpenPRInBrowserEnabled() to
	// read the effective value.
	OpenPRInBrowser *bool `json:"open_pr_in_browser,omitempty"`
	// StatuslineFormat controls what `agentctl statusline` renders
	// (internal/statusline): "rich" (default -- a colored 2-line
	// status line covering account rate limits on line 1 and
	// model/context/tokens/cache/turns/cost/duration on line 2, replacing
	// the equivalent rows the chrome bar used to own), "complementary"
	// (account-level rate limit/cost figures only), "full" (model + ctx% +
	// agent name), or "minimal" (a bare placeholder). Empty or any other
	// value falls back to "rich" -- use EffectiveStatuslineFormat to read the
	// normalized value; this never errors on an unrecognized string.
	StatuslineFormat string `json:"statusline_format,omitempty"`
	// StatuslineAlign controls whether the "rich" statusline format is
	// left-aligned (default, today's behavior) or right-aligned. Right
	// alignment pads each line with plain spaces so it is flush with the
	// right edge of the terminal -- see renderRich's doc comment and
	// rightAlignReserve in internal/statusline for the live-pane
	// measurements behind the safety margin that makes this work without
	// Claude Code wrapping or truncating the row. Empty or any other value
	// falls back to "left" -- use EffectiveStatuslineAlign to read the
	// normalized value; this never errors on an unrecognized string.
	StatuslineAlign string `json:"statusline_align,omitempty"`
	// BashPreview controls whether EVERY Bash PreToolUse command (not just
	// "agentctl dispatch", which is always gated regardless of this setting)
	// is routed through the human-in-the-loop preview popup
	// (internal/hooks.ValidateBash). nil (unset) means disabled; only an
	// explicit true enables it. Defaults to false because an unattended
	// dispatched agent that hits this popup with nobody attached would
	// otherwise deadlock or hijack another window -- see hasAttachedClient in
	// internal/hooks. Use EffectiveBashPreview to read the effective value,
	// and AgentConfig.BashPreview to override per-agent.
	BashPreview *bool `json:"bash_preview,omitempty"`
	// LLM configures the local model provider used for text completion (see
	// internal/llm). Empty/unset Provider means the feature is disabled.
	LLM LLMConfig `json:"llm,omitempty"`
}

// StringPtr returns a pointer to s, for setting optional string fields such as
// Config.NotifyCmd.
func StringPtr(s string) *string { return &s }

// EffectiveNotifyCmd returns the configured notify_cmd and whether it was
// explicitly set. When set is false the caller should apply its platform
// default; when set is true the value is used as-is, and "" means disabled.
func (c Config) EffectiveNotifyCmd() (cmd string, set bool) {
	if c.NotifyCmd == nil {
		return "", false
	}
	return *c.NotifyCmd, true
}

// EffectiveStatuslineFormat returns the configured statusline_format,
// normalized to one of "rich" (default), "complementary", "full", or
// "minimal". Any unrecognized value -- including empty/unset -- falls back
// to "rich" rather than erroring.
func (c Config) EffectiveStatuslineFormat() string {
	switch c.StatuslineFormat {
	case "full", "minimal", "complementary", "rich":
		return c.StatuslineFormat
	default:
		return "rich"
	}
}

// EffectiveStatuslineAlign returns the configured statusline_align,
// normalized to one of "left" (default) or "right". Any unrecognized
// value -- including empty/unset -- falls back to "left" rather than
// erroring, matching current (pre-alignment) behavior.
func (c Config) EffectiveStatuslineAlign() string {
	switch c.StatuslineAlign {
	case "right":
		return c.StatuslineAlign
	default:
		return "left"
	}
}

// OpenPRInBrowserEnabled returns whether newly-created PRs should be opened
// in the browser automatically, defaulting to true when unset.
func (c Config) OpenPRInBrowserEnabled() bool {
	return c.OpenPRInBrowser == nil || *c.OpenPRInBrowser
}

// EffectiveWorkJournal returns true if journaling is enabled for the given agent.
// Per-agent work_journal=true overrides global; otherwise falls back to global default.
func (c *Config) EffectiveWorkJournal(name string) bool {
	if ac, ok := c.Agents[name]; ok && ac.WorkJournal {
		return true
	}
	return c.WorkJournal
}

// EffectiveAutoReclaimWorktree returns whether SessionEnd should attempt to
// automatically reclaim the named agent's worktree, defaulting to true when
// unset. Unlike EffectiveWorkJournal (which ORs a false-default global with a
// true-default per-agent override), this setting defaults to true, so it uses
// a *bool to distinguish "not configured" from an explicit opt-out -- the
// same pattern as Config.OpenPRInBrowserEnabled.
func (c *Config) EffectiveAutoReclaimWorktree(name string) bool {
	if ac, ok := c.Agents[name]; ok && ac.AutoReclaimWorktree != nil {
		return *ac.AutoReclaimWorktree
	}
	return true
}

// EffectiveBashPreview returns whether every Bash PreToolUse command for the
// named agent should be gated behind the human preview popup, defaulting to
// false when unset. Per-agent override wins when set (so a global true can
// be overridden to false for one agent, e.g. an unattended dispatched
// worker); otherwise falls back to the global setting. Same *bool pattern as
// EffectiveAutoReclaimWorktree, but defaulting to false rather than true --
// this popup did not exist before, and every unattended agent must stay
// unaffected unless explicitly opted in.
func (c *Config) EffectiveBashPreview(name string) bool {
	if ac, ok := c.Agents[name]; ok && ac.BashPreview != nil {
		return *ac.BashPreview
	}
	if c.BashPreview != nil {
		return *c.BashPreview
	}
	return false
}

// EffectiveOnComplete returns the on_complete hook for the given agent config,
// falling back to the global config. Returns "" if none is set.
func (c *Config) EffectiveOnComplete(ac AgentConfig) string {
	if ac.Dispatch != nil && ac.Dispatch.OnComplete != "" {
		return ac.Dispatch.OnComplete
	}
	return c.Dispatch.OnComplete
}

// EffectiveHooks returns the merged list of shell commands for the given
// lifecycle event. Global hooks come first, followed by per-agent hooks.
//
// Backward-compat: when event is "dispatch_complete" and the merged list is
// empty, dispatch.on_complete (global or agent-level) is returned as a single
// fallback command so existing configs keep working without migration.
func (c *Config) EffectiveHooks(event string, agentCfg AgentConfig) []string {
	var cmds []string

	// Global hooks.
	if len(c.Hooks[event]) > 0 {
		cmds = append(cmds, c.Hooks[event]...)
	}

	// Per-agent hooks run after global hooks.
	if len(agentCfg.Hooks[event]) > 0 {
		cmds = append(cmds, agentCfg.Hooks[event]...)
	}

	// Backward compat: fall back to dispatch.on_complete for dispatch_complete.
	if event == "dispatch_complete" && len(cmds) == 0 {
		if oc := c.EffectiveOnComplete(agentCfg); oc != "" {
			cmds = []string{oc}
		}
	}

	return cmds
}

// EffectiveAutoPollInterval returns the configured poll interval, defaulting to 5 minutes.
func (c *Config) EffectiveAutoPollInterval() time.Duration {
	if c.AutoPollInterval != "" {
		if d, err := time.ParseDuration(c.AutoPollInterval); err == nil {
			return d
		}
	}
	return 5 * time.Minute
}

// DefaultRestoreMaxAge is the restore age gate when restore_max_age is unset
// or unparseable.
const DefaultRestoreMaxAge = 168 * time.Hour

// EffectiveRestoreMaxAge returns the restore age gate. A result <= 0 means the
// gate is disabled.
func (c *Config) EffectiveRestoreMaxAge() time.Duration {
	if c.RestoreMaxAge == "" {
		return DefaultRestoreMaxAge
	}
	d, err := time.ParseDuration(c.RestoreMaxAge)
	if err != nil {
		// Unparseable: fall back rather than silently disabling the gate.
		return DefaultRestoreMaxAge
	}
	return d
}

// ToggleAutoPoll flips the auto_poll setting and saves it to the global
// config, the only layer that honors it (autopilot dispatches issues
// unattended, so a cloned repo must not be able to switch it on).
func (c *Config) ToggleAutoPoll() (bool, error) {
	c.AutoPoll = !c.AutoPoll
	v := c.AutoPoll
	err := updateLayer(globalConfigPath, func(layer *Config) { layer.AutoPoll = v })
	return c.AutoPoll, err
}

// EffectiveMetricsConfig returns a MetricsConfig with defaults applied.
func (c *Config) EffectiveMetricsConfig() MetricsConfig {
	m := c.Metrics
	// Port and PushInterval defaults are applied lazily by the helper methods,
	// so no extra work is needed here — callers use m.EffectivePort() etc.
	return m
}

// DefaultAgentConfig returns the default settings for a new agent.
func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Alerts: true,
		AlertConfig: AlertConfig{
			Blocked: true,
			Idle:    true,
			Gone:    false,
		},
		AutoImprove:  false,
		CrossContext: true,
	}
}

// configPath is the primary (project-local) config file path, set once at
// startup by Init. Points to .agentctl/config.json inside the project root.
// Config is not concurrent-safe: Load returns a snapshot that callers own,
// and Save writes it atomically. Two concurrent Load→modify→Save sequences
// are a TOCTOU — acceptable because the monitor is single-goroutine and TUI
// popups are sequential.
var configPath string

// globalConfigPath is the user-wide fallback config: ~/.config/agentctl/config.json.
var globalConfigPath string

// FilePath returns the resolved project-local config file path (set by Init).
func FilePath() string { return configPath }

// legacyWarnOnce ensures the XDG-migration warning is logged once per process.
var legacyWarnOnce sync.Once

// GlobalDirIn returns the user-wide agentctl config directory for the given
// home. It honors XDG_CONFIG_HOME when set (<XDG_CONFIG_HOME>/agentctl), else
// <home>/.config/agentctl. Migration safety: if XDG_CONFIG_HOME is set,
// <xdg>/agentctl does not exist, and <home>/.config/agentctl does, the legacy
// dir is used (with a one-time warning suggesting a move) so existing config
// and journals are never silently orphaned.
func GlobalDirIn(home string) string {
	legacy := filepath.Join(home, ".config", "agentctl")
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		return legacy
	}
	dir := filepath.Join(xdg, "agentctl")
	if dirExists(dir) || !dirExists(legacy) {
		return dir
	}
	legacyWarnOnce.Do(func() {
		logging.Log("config: XDG_CONFIG_HOME is set but %s does not exist; using existing %s. Move it to %s to adopt XDG.", dir, legacy, dir)
	})
	return legacy
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// GlobalDir returns the user-wide agentctl config directory (see
// GlobalDirIn). It returns an error if the home directory is needed but
// cannot be resolved; with XDG_CONFIG_HOME set and no resolvable home, the XDG
// dir is used.
func GlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "agentctl"), nil
		}
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return GlobalDirIn(home), nil
}

// Init sets the config path. Must be called once at startup before Load or
// Save are used. The primary config lives at .agentctl/config.json inside the
// project root; the global fallback is <GlobalDir>/config.json. It returns an
// error if the global config directory cannot be resolved.
func Init() error {
	dir, err := GlobalDir()
	if err != nil {
		return fmt.Errorf("config init: %w", err)
	}
	globalConfigPath = filepath.Join(dir, "config.json")

	// Primary: project-local config in .agentctl/
	if runtimeDir := paths.RuntimeDir(); runtimeDir != "" {
		configPath = filepath.Join(runtimeDir, "config.json")
	} else {
		// Fallback when paths.Init hasn't been called (shouldn't happen in practice)
		configPath = globalConfigPath
	}
	return nil
}

// loadFromPath reads config from a specific path. Returns an empty config if
// the file doesn't exist or cannot be parsed.
func loadFromPath(path string) *Config {
	cfg := &Config{Agents: make(map[string]AgentConfig)}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		logging.Error("config: failed to parse %s: %v", path, err)
		return cfg
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
	}
	return cfg
}

// mergeConfig overlays non-zero fields from overlay onto base. The base is
// modified in place. For the agents map, project-level entries replace global
// ones per-agent (no deep merge within an agent config).
func mergeConfig(base, overlay *Config) {
	if overlay.DefaultAgent != "" {
		base.DefaultAgent = overlay.DefaultAgent
	}
	if overlay.NotifyCmd != nil {
		base.NotifyCmd = overlay.NotifyCmd
	}
	if overlay.Theme != "" {
		base.Theme = overlay.Theme
	}
	if overlay.AutoPoll {
		base.AutoPoll = overlay.AutoPoll
	}
	if overlay.AutoPollInterval != "" {
		base.AutoPollInterval = overlay.AutoPollInterval
	}
	if overlay.SessionRetentionDays != 0 {
		base.SessionRetentionDays = overlay.SessionRetentionDays
	}
	if overlay.WorkJournal {
		base.WorkJournal = overlay.WorkJournal
	}
	// Metrics: overlay if enabled
	if overlay.Metrics.Enabled {
		base.Metrics = overlay.Metrics
	}
	// Dispatch: overlay if non-zero
	if overlay.Dispatch.OnComplete != "" {
		base.Dispatch = overlay.Dispatch
	}
	// RateLimit: overlay if non-zero
	if overlay.RateLimit.TokenCap != 0 {
		base.RateLimit = overlay.RateLimit
	}
	// Hooks: overlay replaces entirely if non-empty
	if len(overlay.Hooks) > 0 {
		base.Hooks = overlay.Hooks
	}
	// OpenPRInBrowser: overlay if explicitly set (nil means "not specified").
	if overlay.OpenPRInBrowser != nil {
		base.OpenPRInBrowser = overlay.OpenPRInBrowser
	}
	if overlay.StatuslineFormat != "" {
		base.StatuslineFormat = overlay.StatuslineFormat
	}
	if overlay.StatuslineAlign != "" {
		base.StatuslineAlign = overlay.StatuslineAlign
	}
	// BashPreview: overlay if explicitly set (nil means "not specified"),
	// same pattern as OpenPRInBrowser -- an explicit false must survive the
	// merge, not be indistinguishable from "unset".
	if overlay.BashPreview != nil {
		base.BashPreview = overlay.BashPreview
	}
	// LLM: overlay if provider is explicitly set.
	if overlay.LLM.Provider != "" {
		base.LLM = overlay.LLM
	}
	// Agents: merge map (project agents override global per-agent)
	for name, ac := range overlay.Agents {
		base.Agents[name] = ac
	}
	// Models: scalar fields overlay when non-zero; Overrides merges per-key
	// (local key wins) rather than whole-map-replace, so a project can teach
	// one model without redeclaring all of them.
	if overlay.Models.DefaultContextWindow != 0 {
		base.Models.DefaultContextWindow = overlay.Models.DefaultContextWindow
	}
	if overlay.Models.ContextWarnPct != 0 {
		base.Models.ContextWarnPct = overlay.Models.ContextWarnPct
	}
	if overlay.Models.ContextCriticalPct != 0 {
		base.Models.ContextCriticalPct = overlay.Models.ContextCriticalPct
	}
	if overlay.Models.ReserveOutput != nil {
		base.Models.ReserveOutput = overlay.Models.ReserveOutput
	}
	if len(overlay.Models.Overrides) > 0 {
		if base.Models.Overrides == nil {
			base.Models.Overrides = make(map[string]ModelOverride, len(overlay.Models.Overrides))
		}
		for k, v := range overlay.Models.Overrides {
			base.Models.Overrides[k] = v
		}
	}
}

// warnedKeys dedupes sanitize warnings: Load runs on every monitor tick, and
// the same ignored key would otherwise flood the log.
var warnedKeys sync.Map

// safeLaunchValue is the conservative charset allowed for model and
// effort_level values coming from the project-local layer, since they end up
// in the agent launch command line.
var safeLaunchValue = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, l := range [][]string{a, b} {
		for _, v := range l {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func containsAll(have, want []string) bool {
	set := make(map[string]bool, len(have))
	for _, v := range have {
		set[v] = true
	}
	for _, v := range want {
		if !set[v] {
			return false
		}
	}
	return true
}

// sanitizeProjectConfig strips from a project-local config everything that
// would let a cloned repo's committed .agentctl/config.json run commands,
// widen permissions, weaken guards, or exfiltrate data. Ignored keys, each
// logged once with a one-line warning:
//   - shell-executed: notify_cmd, hooks, dispatch.on_complete, and per-agent
//     hooks / dispatch.on_complete
//   - permissions: agents.*.auto_accept, agents.*.allowed_permissions (a
//     project may not add), blocked_commands / blocked_patterns (a project
//     may add but never remove: the result is global ∪ project)
//   - agents.*.permission_mode (bypassPermissions/dontAsk would launch claude
//     with no approvals); the global agent's value is used instead
//   - default_agent when it fails agent.ValidLaunchName (the value becomes a
//     tmux window name and path component); the global value is kept. A
//     valid name is honored here, but because it picks what prefix+A
//     launches, the launch path (cmd.usableDefaultAgent) additionally
//     rejects agents defined only in the repo and auto-approving agents,
//     falling back to GlobalDefaultAgent
//   - agents.*.dir (would point an auto-mode worker at any directory, e.g. ~),
//     agents.*.cross_context and agents.*.context_keys (would pull the user's
//     global context store into the prompt of an agent processing attacker
//     input), and agents.*.auto_improve (starts an unattended improvement
//     session); the global agent's value is used instead, or the default
//     (DefaultAgentConfig) for an agent the global config does not define.
//     A project may still switch cross_context / auto_improve off.
//   - llm.* (provider/host could send session summaries elsewhere)
//   - metrics.endpoint / metrics.basic_auth (remote write could exfiltrate
//     session metrics); a project may still toggle metrics and the loopback
//     port
//   - auto_poll (autopilot dispatches every "ready" issue unattended)
//   - bash_preview / agents.*.bash_preview set to false when the global layer
//     enables it (a project may tighten the guard, never loosen it)
//   - agents.*.model / effort_level values outside [A-Za-z0-9._:-]+ (they are
//     interpolated into the launch command); the global value is kept instead
//
// For agents defined in both layers, the global agent's protected fields win.
func sanitizeProjectConfig(project, global *Config) {
	warn := func(key, why string) {
		msg := fmt.Sprintf("config: ignoring %q from project-local config %s (%s)", key, configPath, why)
		if _, dup := warnedKeys.LoadOrStore(msg, true); !dup {
			logging.Log("%s", msg)
		}
	}
	const globalOnly = "only honored in the global config"
	const noLoosen = "project may enable this guard but not disable it"
	if project.NotifyCmd != nil {
		warn("notify_cmd", globalOnly)
		project.NotifyCmd = nil
	}
	if project.DefaultAgent != "" && !agentname.ValidLaunch(project.DefaultAgent) {
		warn("default_agent", "invalid agent name")
		project.DefaultAgent = ""
	}
	if project.AutoPoll && !global.AutoPoll {
		warn("auto_poll", globalOnly)
	}
	project.AutoPoll = false
	if project.Metrics.Endpoint != "" && project.Metrics.Endpoint != global.Metrics.Endpoint {
		warn("metrics.endpoint", globalOnly)
	}
	if project.Metrics.BasicAuth != nil && !reflect.DeepEqual(project.Metrics.BasicAuth, global.Metrics.BasicAuth) {
		warn("metrics.basic_auth", globalOnly)
	}
	project.Metrics.Endpoint = global.Metrics.Endpoint
	project.Metrics.BasicAuth = global.Metrics.BasicAuth
	globalPreview := global.BashPreview != nil && *global.BashPreview
	if project.BashPreview != nil && !*project.BashPreview && globalPreview {
		warn("bash_preview", noLoosen)
		project.BashPreview = nil
	}
	if len(project.Hooks) > 0 {
		warn("hooks", globalOnly)
		project.Hooks = nil
	}
	if project.Dispatch.OnComplete != "" {
		warn("dispatch.on_complete", globalOnly)
		project.Dispatch = DispatchConfig{}
	}
	if project.LLM != (LLMConfig{}) {
		warn("llm", globalOnly)
		project.LLM = LLMConfig{}
	}
	for name, ac := range project.Agents {
		g, hasGlobal := global.Agents[name]
		pre := "agents." + name + "."
		if len(ac.Hooks) > 0 {
			warn(pre+"hooks", globalOnly)
		}
		if ac.Dispatch != nil && ac.Dispatch.OnComplete != "" {
			warn(pre+"dispatch.on_complete", globalOnly)
		}
		if ac.AutoAccept && !(hasGlobal && g.AutoAccept) {
			warn(pre+"auto_accept", globalOnly)
		}
		if len(ac.AllowedPermissions) > 0 {
			warn(pre+"allowed_permissions", globalOnly)
		}
		if ac.PermissionMode != "" && ac.PermissionMode != g.PermissionMode {
			warn(pre+"permission_mode", globalOnly)
		}
		// Fallback for the fields below when the global layer does not define
		// this agent: what ForAgent would hand out with no config at all.
		gb := g
		if !hasGlobal {
			gb = DefaultAgentConfig()
		}
		if ac.Dir != "" && ac.Dir != gb.Dir {
			warn(pre+"dir", globalOnly)
		}
		if ac.CrossContext && !gb.CrossContext {
			warn(pre+"cross_context", globalOnly)
		}
		if len(ac.ContextKeys) > 0 && !reflect.DeepEqual(ac.ContextKeys, gb.ContextKeys) {
			warn(pre+"context_keys", globalOnly)
		}
		if ac.AutoImprove && !gb.AutoImprove {
			warn(pre+"auto_improve", globalOnly)
		}
		if ac.BashPreview != nil && !*ac.BashPreview && global.EffectiveBashPreview(name) {
			warn(pre+"bash_preview", noLoosen)
			ac.BashPreview = g.BashPreview
		}
		// mergeConfig replaces whole agent entries, so a project entry that
		// merely omits bash_preview would otherwise drop the global agent's
		// setting. Inherit it explicitly.
		if ac.BashPreview == nil {
			ac.BashPreview = g.BashPreview
		}
		if hasGlobal && !containsAll(ac.BlockedCommands, g.BlockedCommands) {
			warn(pre+"blocked_commands", "project may add entries but not remove global ones")
		}
		if hasGlobal && !containsAll(ac.BlockedPatterns, g.BlockedPatterns) {
			warn(pre+"blocked_patterns", "project may add entries but not remove global ones")
		}
		if ac.Model != "" && !safeLaunchValue.MatchString(ac.Model) {
			warn(pre+"model", "invalid characters")
			ac.Model = g.Model
		}
		if ac.EffortLevel != "" && !safeLaunchValue.MatchString(ac.EffortLevel) {
			warn(pre+"effort_level", "invalid characters")
			ac.EffortLevel = g.EffortLevel
		}

		ac.Hooks, ac.Dispatch = g.Hooks, g.Dispatch
		ac.AutoAccept = g.AutoAccept
		ac.AllowedPermissions = g.AllowedPermissions
		ac.PermissionMode = g.PermissionMode
		// cross_context and auto_improve may be switched off by the project
		// (that only narrows what the agent does), never on.
		ac.Dir, ac.ContextKeys = gb.Dir, gb.ContextKeys
		ac.CrossContext = ac.CrossContext && gb.CrossContext
		ac.AutoImprove = ac.AutoImprove && gb.AutoImprove
		ac.BlockedCommands = unionStrings(g.BlockedCommands, ac.BlockedCommands)
		ac.BlockedPatterns = unionStrings(g.BlockedPatterns, ac.BlockedPatterns)
		project.Agents[name] = ac
	}
}

// Load reads the config from disk. Merges global (~/.config/agentctl/config.json)
// and project-local (.agentctl/config.json) configs, with project-local taking
// precedence on a field-by-field basis.
func Load() *Config {
	// Start with global defaults.
	cfg := loadFromPath(globalConfigPath)
	cfg.GlobalDefaultAgent = cfg.DefaultAgent

	// Overlay project config when it's distinct from global.
	if configPath != globalConfigPath {
		projectCfg := loadFromPath(configPath)
		sanitizeProjectConfig(projectCfg, cfg)
		mergeConfig(cfg, projectCfg)
	}

	// Convert config.ModelOverride to tokens.ModelOverride and push into the
	// registry. Every process (monitor, chrome bar pane, CLI commands) picks
	// overrides up for free without each remembering to wire it.
	tokOverrides := make(map[string]tokens.ModelOverride, len(cfg.Models.Overrides))
	for k, v := range cfg.Models.Overrides {
		tokOverrides[k] = tokens.ModelOverride{
			ContextWindow:      v.ContextWindow,
			MaxOutput:          v.MaxOutput,
			Short:              v.Short,
			InputPerMTok:       v.InputPerMTok,
			OutputPerMTok:      v.OutputPerMTok,
			CacheReadPerMTok:   v.CacheReadPerMTok,
			CacheCreatePerMTok: v.CacheCreatePerMTok,
		}
	}
	tokens.SetOverrides(tokOverrides, cfg.Models.DefaultContextWindow)
	tokens.SetReserveOutput(cfg.Models.EffectiveReserveOutput())

	return cfg
}

// readLayer reads a single config layer from path without merging or
// sanitizing. A missing file yields an empty config; an unparseable one is an
// error so a write never clobbers a file the user is mid-edit on.
func readLayer(path string) (*Config, error) {
	cfg := &Config{Agents: make(map[string]AgentConfig)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
	}
	return cfg, nil
}

// updateLayer loads only the layer at path, applies fn to it, and writes it
// back atomically.
func updateLayer(path string, fn func(layer *Config)) error {
	layer, err := readLayer(path)
	if err != nil {
		return err
	}
	fn(layer)
	return saveToPath(path, layer)
}

// saveToPath writes cfg as JSON to the given path, creating parent dirs as
// needed. Config files can hold credentials (metrics.basic_auth) and shell
// commands, so the file is 0600 and a newly created directory 0700.
//
// Top-level keys in the existing file that Config does not model (e.g.
// protected_branches and the top-level blocked_commands / blocked_patterns
// read via ProjectConfig) are preserved with their values and relative order;
// whitespace may be normalized. Keys Config owns are always rewritten from
// cfg, so clearing a field removes it.
func saveToPath(path string, cfg *Config) error {
	data, err := encodeLayer(cfg, unknownKeys(path))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	logging.Debug("config: saved %s (%d bytes)", path, len(data))
	return nil
}

// rawKey is one top-level key of a config file and its raw JSON value.
type rawKey struct {
	name  string
	value json.RawMessage
}

// configKeys returns the top-level JSON keys Config owns, in field order.
func configKeys() []string {
	t := reflect.TypeOf(Config{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

// unknownKeys returns, in document order, the top-level keys of the JSON
// object at path that Config does not own. A missing or unparseable file
// yields nil (callers that must not clobber a bad file check that first).
func unknownKeys(path string) []rawKey {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	owned := make(map[string]bool)
	for _, k := range configKeys() {
		owned[k] = true
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out []rawKey
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		name, ok := tok.(string)
		if !ok {
			return nil
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil
		}
		if !owned[name] {
			out = append(out, rawKey{name, v})
		}
	}
	return out
}

// encodeLayer encodes cfg as an indented JSON object: Config's keys first in
// field order, then extra in the given order.
func encodeLayer(cfg *Config, extra []rawKey) ([]byte, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var keys []rawKey
	for _, k := range configKeys() {
		if v, ok := doc[k]; ok {
			keys = append(keys, rawKey{k, v})
		}
	}
	keys = append(keys, extra...)

	var buf bytes.Buffer
	buf.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(",")
		}
		name, err := json.Marshal(k.name)
		if err != nil {
			return nil, err
		}
		buf.WriteString("\n  ")
		buf.Write(name)
		buf.WriteString(": ")
		if err := json.Indent(&buf, k.value, "  ", "  "); err != nil {
			return nil, fmt.Errorf("encoding %q: %w", k.name, err)
		}
	}
	if len(keys) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so readers never observe a partial file. If path is a
// symlink, the link target is replaced and the symlink itself is preserved.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Save persists the changes cfg carries, relative to the current merged view
// (Load), into the project-local layer. Only that layer is read from disk and
// rewritten, and top-level keys Config does not model are preserved.
//
// Top-level fields are copied only when they changed, so unchanged global
// values are not copied into the repository. Agents are the exception:
// because mergeConfig replaces whole agent entries, an agent whose merged
// entry changed is written as a whole entry, including fields it inherited
// from the global agent. Global-only fields (hooks, dispatch, auto_accept,
// allowed_permissions, permission_mode, dir, context_keys) are never taken
// from the merged entry; they keep whatever the project file already had, and blocked_*
// lists drop entries the global agent already contributes.
//
// Changes to global-only keys (see sanitizeProjectConfig) are not written to
// the project layer, where they would be ignored; use the dedicated setters
// (SetAgent, ToggleAutoAccept, ToggleAutoPoll, ToggleCrossContext,
// ToggleAutoImprove, SetDefaultAgent) for those.
//
// When the project and global paths coincide (a single trusted layer), cfg is
// written as-is.
func Save(cfg *Config) error {
	if configPath == globalConfigPath {
		return saveToPath(configPath, cfg)
	}
	before := Load()
	global, err := readLayer(globalConfigPath)
	if err != nil {
		// An unreadable global layer only weakens blocked_* deduplication.
		global = &Config{Agents: make(map[string]AgentConfig)}
	}
	return updateLayer(configPath, func(layer *Config) {
		applyProjectChanges(layer, before, cfg, global)
	})
}

// applyProjectChanges copies every field that differs between before and
// after onto the project layer, then restores the layer's own values for
// global-only keys so no inherited global value is written to the project
// file. Agents are diffed per entry.
func applyProjectChanges(layer, before, after, global *Config) {
	protected := *layer
	diffInto(reflect.ValueOf(layer).Elem(), reflect.ValueOf(before).Elem(), reflect.ValueOf(after).Elem())

	restore := func(key string, changed bool) {
		if changed {
			logging.Warn("config: not saving %q to project-local config %s (only honored in the global config)", key, configPath)
		}
	}
	restore("notify_cmd", !reflect.DeepEqual(layer.NotifyCmd, protected.NotifyCmd))
	restore("hooks", !reflect.DeepEqual(layer.Hooks, protected.Hooks))
	restore("dispatch", layer.Dispatch != protected.Dispatch)
	restore("llm", layer.LLM != protected.LLM)
	restore("auto_poll", layer.AutoPoll != protected.AutoPoll)
	restore("default_agent", layer.DefaultAgent != protected.DefaultAgent)
	layer.NotifyCmd, layer.Hooks, layer.Dispatch, layer.LLM, layer.AutoPoll, layer.DefaultAgent =
		protected.NotifyCmd, protected.Hooks, protected.Dispatch, protected.LLM, protected.AutoPoll, protected.DefaultAgent
	layer.Metrics.Endpoint, layer.Metrics.BasicAuth = protected.Metrics.Endpoint, protected.Metrics.BasicAuth

	layer.Agents = make(map[string]AgentConfig, len(protected.Agents))
	for name, ac := range protected.Agents {
		layer.Agents[name] = ac
	}
	for name, ac := range after.Agents {
		if b, ok := before.Agents[name]; ok && reflect.DeepEqual(b, ac) {
			continue
		}
		layer.Agents[name] = projectAgent(ac, protected.Agents[name], global.Agents[name])
	}
	for name := range before.Agents {
		if _, ok := after.Agents[name]; !ok {
			delete(layer.Agents, name)
		}
	}
}

// projectAgent returns ac as it should be stored in the project layer: the
// global-only fields keep whatever the project file already had (prev), and
// blocked_* lists drop entries the global agent already contributes.
func projectAgent(ac, prev, global AgentConfig) AgentConfig {
	ac.Hooks, ac.Dispatch = prev.Hooks, prev.Dispatch
	ac.AutoAccept = prev.AutoAccept
	ac.AllowedPermissions = prev.AllowedPermissions
	ac.PermissionMode = prev.PermissionMode
	ac.Dir, ac.ContextKeys = prev.Dir, prev.ContextKeys
	ac.BlockedCommands = minusStrings(ac.BlockedCommands, global.BlockedCommands)
	ac.BlockedPatterns = minusStrings(ac.BlockedPatterns, global.BlockedPatterns)
	return ac
}

// minusStrings returns the entries of a that are not in b, preserving order.
func minusStrings(a, b []string) []string {
	drop := make(map[string]bool, len(b))
	for _, v := range b {
		drop[v] = true
	}
	var out []string
	for _, v := range a {
		if !drop[v] {
			out = append(out, v)
		}
	}
	return out
}

// diffInto sets each field of dst whose value differs between before and
// after to after's value, recursing into nested structs so a change to one
// sub-field does not drag its siblings along. The Agents map is skipped;
// callers diff it per entry.
func diffInto(dst, before, after reflect.Value) {
	t := after.Type()
	for i := 0; i < t.NumField(); i++ {
		if !t.Field(i).IsExported() || (t == reflect.TypeOf(Config{}) && t.Field(i).Name == "Agents") {
			continue
		}
		a, b := after.Field(i), before.Field(i)
		if reflect.DeepEqual(a.Interface(), b.Interface()) {
			continue
		}
		if a.Kind() == reflect.Struct {
			diffInto(dst.Field(i), b, a)
			continue
		}
		dst.Field(i).Set(a)
	}
}

// setGlobalAutoAccept writes agents.<name>.auto_accept to the global layer,
// the only layer that honors it.
func setGlobalAutoAccept(name string, v bool) error {
	return setGlobalAgent(name, func(ac *AgentConfig) { ac.AutoAccept = v })
}

// setGlobalAgent applies fn to agents.<name> in the global layer (starting
// from DefaultAgentConfig when the agent is not defined there) and writes it.
// It is how changes to global-only agent fields (see sanitizeProjectConfig)
// are persisted where they are honored.
func setGlobalAgent(name string, fn func(*AgentConfig)) error {
	return updateLayer(globalConfigPath, func(layer *Config) {
		ac, ok := layer.Agents[name]
		if !ok {
			ac = DefaultAgentConfig()
		}
		fn(&ac)
		layer.Agents[name] = ac
	})
}

// SetDefaultAgent writes default_agent to the global layer. A project-local
// default_agent may still override it (see Config.DefaultAgent).
func SetDefaultAgent(name string) error {
	return updateLayer(globalConfigPath, func(layer *Config) {
		layer.DefaultAgent = name
	})
}

// globalAgentUpdate returns the change SetAgent must write to the global
// layer for an agent going from prev to next, or nil when there is none.
//
// auto_accept, dir and context_keys are global-only in both directions.
// cross_context and auto_improve are global-only only when switched ON: a
// project may switch them off (sanitizeProjectConfig ANDs the layers), so
// switching off is a project-layer change and must not leak to every other
// project via the global file.
func globalAgentUpdate(prev, next AgentConfig) func(*AgentConfig) {
	var fns []func(*AgentConfig)
	if prev.AutoAccept != next.AutoAccept {
		fns = append(fns, func(g *AgentConfig) { g.AutoAccept = next.AutoAccept })
	}
	if prev.Dir != next.Dir {
		fns = append(fns, func(g *AgentConfig) { g.Dir = next.Dir })
	}
	if !reflect.DeepEqual(prev.ContextKeys, next.ContextKeys) {
		fns = append(fns, func(g *AgentConfig) { g.ContextKeys = next.ContextKeys })
	}
	if next.CrossContext && !prev.CrossContext {
		fns = append(fns, func(g *AgentConfig) { g.CrossContext = true })
	}
	if next.AutoImprove && !prev.AutoImprove {
		fns = append(fns, func(g *AgentConfig) { g.AutoImprove = true })
	}
	if len(fns) == 0 {
		return nil
	}
	return func(g *AgentConfig) {
		for _, fn := range fns {
			fn(g)
		}
	}
}

// ForAgent returns the config for a specific agent, with defaults applied.
func (c *Config) ForAgent(name string) AgentConfig {
	if ac, ok := c.Agents[name]; ok {
		// Apply defaults for nested structs that may be missing from older configs
		if ac.AlertConfig == (AlertConfig{}) {
			ac.AlertConfig = DefaultAgentConfig().AlertConfig
		}
		return ac
	}
	return DefaultAgentConfig()
}

// AgentNames returns all configured agent names (keys of the agents map).
func (c *Config) AgentNames() []string {
	names := make([]string, 0, len(c.Agents))
	for name := range c.Agents {
		names = append(names, name)
	}
	return names
}

// SetAgent updates the config for a specific agent and saves to disk.
// Changes the project layer cannot express (see globalAgentUpdate) are
// written to the global layer; everything goes to the project layer via
// Save, which also records an enabled cross_context / auto_improve there so
// that an earlier project-level false no longer masks it.
func (c *Config) SetAgent(name string, ac AgentConfig) error {
	prev := c.ForAgent(name)
	c.Agents[name] = ac
	if fn := globalAgentUpdate(prev, ac); fn != nil {
		if err := setGlobalAgent(name, fn); err != nil {
			return err
		}
	}
	return Save(c)
}

// ToggleAlert flips the alerts setting for an agent and saves.
func (c *Config) ToggleAlert(name string) (bool, error) {
	ac := c.ForAgent(name)
	ac.Alerts = !ac.Alerts
	c.Agents[name] = ac
	err := Save(c)
	logging.Log("config: %s.alerts → %v", name, ac.Alerts)
	return ac.Alerts, err
}

// ToggleAutoImprove flips the auto_improve setting for an agent and saves.
func (c *Config) ToggleAutoImprove(name string) (bool, error) {
	ac := c.ForAgent(name)
	ac.AutoImprove = !ac.AutoImprove
	err := c.SetAgent(name, ac)
	logging.Log("config: %s.auto_improve → %v", name, ac.AutoImprove)
	return ac.AutoImprove, err
}

// ToggleAutoAccept flips the auto_accept setting for an agent and saves it to
// the global config, the only layer that honors it.
func (c *Config) ToggleAutoAccept(name string) (bool, error) {
	ac := c.ForAgent(name)
	ac.AutoAccept = !ac.AutoAccept
	c.Agents[name] = ac
	err := setGlobalAutoAccept(name, ac.AutoAccept)
	logging.Log("config: %s.auto_accept → %v", name, ac.AutoAccept)
	return ac.AutoAccept, err
}

// ToggleCrossContext flips the cross_context setting for an agent and saves.
func (c *Config) ToggleCrossContext(name string) (bool, error) {
	ac := c.ForAgent(name)
	ac.CrossContext = !ac.CrossContext
	err := c.SetAgent(name, ac)
	logging.Log("config: %s.cross_context → %v", name, ac.CrossContext)
	return ac.CrossContext, err
}

// ToggleWorkJournal flips the work_journal setting for an agent and saves.
func (c *Config) ToggleWorkJournal(name string) (bool, error) {
	ac := c.ForAgent(name)
	ac.WorkJournal = !ac.WorkJournal
	c.Agents[name] = ac
	err := Save(c)
	logging.Log("config: %s.work_journal → %v", name, ac.WorkJournal)
	return ac.WorkJournal, err
}

// SetConfigPathForTest points both the project-local and global config paths
// at path, so a test's single config file is treated as the trusted global
// config (command keys such as notify_cmd and hooks are honored). Tests that
// need distinct layers should use SetGlobalConfigPathForTest afterwards. Only
// for use in tests.
func SetConfigPathForTest(path string) {
	configPath = path
	globalConfigPath = path
}

// SetGlobalConfigPathForTest overrides the global config file path. Only for use in tests.
func SetGlobalConfigPathForTest(path string) {
	globalConfigPath = path
}
