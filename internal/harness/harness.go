// Package harness abstracts the terminal coding-agent program that runs in an
// agentctl window: Claude Code (the default) or pi. Everything that depends
// on which program it is goes through a Harness: the launch command line and
// its environment, the files it needs in place before launch, the hook
// payload and output contracts, where its transcript lives, and how its pane
// reads. The tmux half of agentctl (windows, windowstate, winname, chrome,
// hub, mailbox storage, reconcile) never needs to know.
//
// Callers check Caps rather than harness names, so a feature a harness lacks
// degrades instead of failing. See docs/design/multi-harness.md.
package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/panestate"
)

// Harness names.
const (
	Claude = "claude"
	Pi     = "pi"
)

// Default is the harness used when none is configured or recorded.
const Default = Claude

// EnvHarness is the window env var carrying a non-default harness name.
// Claude windows do not get it (an absent value means claude), so their
// environment is unchanged from before harnesses existed.
const EnvHarness = "AGENTCTL_HARNESS"

// Caps declares what a harness can do. Callers degrade on a false field.
type Caps struct {
	// PreassignSessionID: agentctl picks the session ID at launch, so the
	// window's transcript is known before the harness starts.
	PreassignSessionID bool
	// ResumeByID: a saved session can be resumed by its ID on restore.
	ResumeByID bool
	// BlockingPreTool: the bash gate (blocked commands, approval popups) can
	// deny a tool call before it runs.
	BlockingPreTool bool
	// BlockedSignal: the harness itself reports a blocked-on-permission-prompt
	// state. Without it a window is only ever blocked while an agentctl
	// approval popup is open.
	BlockedSignal bool
	// ContextInjection: hook output can inject text (mailbox notices) into
	// the conversation.
	ContextInjection bool
	// NativeMessaging: the agent has SendMessage/ListAgents to talk to other
	// sessions. Without it the worker preamble does not mention them.
	NativeMessaging bool
	// Statusline: the harness pushes statusline JSON to agentctl.
	Statusline bool
	// AgentFlag: the harness loads the agent definition by name itself
	// (claude --agent). Without it agentctl injects the full expanded agent
	// body through the system prompt.
	AgentFlag bool
	// PermissionModes: the harness takes a --permission-mode.
	PermissionModes bool
	// Accounts: multi-account support (CLAUDE_CONFIG_DIR) applies.
	Accounts bool
	// Backends: agentctl backends (config `backends`, Ollama and Nous Portal)
	// apply.
	Backends bool
	// RegistersBackends: the harness registers a backend as a provider itself
	// (PrepareWorkspace with Workspace.Backend) instead of agentctl pointing
	// its Anthropic client at the endpoint through env and settings.
	RegistersBackends bool
	// EnvFile: SessionStart hands the hook an env file (CLAUDE_ENV_FILE) to
	// persist variables into the agent's shell tool.
	EnvFile bool
	// PaneClassifier: ClassifyPane can read this harness's pane (dialogs,
	// busy footer, idle prompt). Without it panes classify as Unknown.
	PaneClassifier bool
	// TrustDialog: the harness can stop on a folder-trust dialog before it
	// loads any hooks (Claude Code asks once per new path, so every fresh
	// worktree), which no hook can report. Launch paths start a launch
	// watcher (internal/launchwatch) for these harnesses.
	TrustDialog bool
}

// Invocation names the per-launch parts of a harness command line. Empty
// fields are omitted.
type Invocation struct {
	Agent string // agent name (claude --agent)
	Name  string // session display name (-n)
	// SessionID starts a new session with this ID (or, for harnesses whose
	// session-id flag resumes an existing session, continues it).
	SessionID   string
	Resume      string // continue this existing session
	ForkSession bool   // resume into a new session ID
	// PermissionMode is passed only to harnesses with Caps.PermissionModes.
	PermissionMode string
	Model          string
	Effort         string // agentctl effort level (see launchopts.Efforts)
	SettingsPath   string // claude --settings
	ExtensionPath  string // pi -e
	// Provider selects the provider Model belongs to (pi --provider), for a
	// backend the harness registers itself.
	Provider string
	// SystemPromptFile is appended to the harness's system prompt.
	SystemPromptFile string
	// Message is the first user message, quoted into the command.
	Message string
}

// EnvParams are the inputs to a harness's launch env.
type EnvParams struct {
	PermissionMode    string
	ShellPrefixScript string
}

// Workspace is what PrepareWorkspace needs.
type Workspace struct {
	// Dir is a private (0700) directory agentctl owns where harness files
	// such as the pi bridge extension are written.
	Dir string
	// Backend is the agentctl backend the launch runs against, for harnesses
	// that register it themselves (pi). Nil = the harness's own provider.
	Backend *Backend
	// WriteSettings writes agentctl's Claude Code settings (hooks and
	// permissions) for this launch and returns the --settings path. Only
	// harnesses that read Claude Code settings call it.
	WriteSettings func() (string, error)
}

// Prepared is what PrepareWorkspace produced.
type Prepared struct {
	SettingsPath  string
	ExtensionPath string
	// Provider and Model select the backend the harness registered (empty
	// when Workspace.Backend was nil); the launcher puts them in the
	// invocation.
	Provider string
	Model    string
	// Env is window env the prepared files need (the pi backend provider).
	Env map[string]string
}

// Backend is an agentctl backend (config `backends`) resolved for a launch,
// in harness-neutral terms. It never carries a secret: a key is fetched at
// request time by KeyCommand.
type Backend struct {
	Name    string // config name, e.g. "nous"
	Kind    string // "ollama" or "nous"
	BaseURL string // endpoint root without /v1
	// Models are the model IDs to register; the first is the launch model.
	Models        []string
	ContextWindow int // tokens; 0 = unknown
	// AuthToken is a literal, non-secret token (Ollama's dummy token).
	AuthToken string
	// KeyCommand is a shell command printing the API key (agentctl
	// backend-token <name>), for backends with a stored credential.
	KeyCommand string
}

// Event is a decoded hook payload. Raw is the payload in agentctl's
// canonical hook shape (Claude Code's: session_id, cwd, transcript_path,
// tool_name, tool_input, ...), which the shared hook handlers decode; the
// other fields are a typed view for callers and tests.
type Event struct {
	SessionID            string          `json:"session_id,omitempty"`
	Cwd                  string          `json:"cwd,omitempty"`
	TranscriptPath       string          `json:"transcript_path,omitempty"`
	ToolName             string          `json:"tool_name,omitempty"`
	ToolInput            json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse         json.RawMessage `json:"tool_response,omitempty"`
	Error                string          `json:"error,omitempty"`
	LastAssistantMessage string          `json:"last_assistant_message,omitempty"`
	StopHookActive       bool            `json:"stop_hook_active,omitempty"`
	Reason               string          `json:"reason,omitempty"`
	Prompt               string          `json:"prompt,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// DecisionKind is what a hook tells the harness.
type DecisionKind int

const (
	// Allow approves a tool call explicitly.
	Allow DecisionKind = iota
	// Deny blocks a tool call (the handler also exits 2).
	Deny
	// Context injects Text into the conversation for HookEvent.
	Context
	// SessionContext is SessionStart's orientation text.
	SessionContext
)

// Decision is a hook's output before it is encoded for a harness.
type Decision struct {
	Kind DecisionKind
	// HookEvent is the harness-neutral event the output answers, in Claude
	// Code's vocabulary ("PreToolUse", "PostToolUse", "UserPromptSubmit").
	HookEvent string
	Reason    string
	Text      string
}

// Harness is one terminal coding-agent program.
type Harness interface {
	Name() string
	Caps() Caps
	// Binary is the executable name, for process matching.
	Binary() string

	// Command returns the shell command line for inv. Every interpolated
	// value is shell-quoted.
	Command(inv Invocation) (string, error)
	// RestoreCommand relaunches a saved window: inv carries the identity
	// (agent, name, settings); sessionID is the saved session and resume
	// says whether it may be continued.
	RestoreCommand(inv Invocation, sessionID string, resume bool) (string, error)
	// LaunchEnv returns harness-specific window env vars.
	LaunchEnv(p EnvParams) map[string]string
	// PrepareWorkspace puts the files the harness needs in place.
	PrepareWorkspace(ws Workspace) (Prepared, error)

	// DecodeHook decodes a hook payload for the agentctl hook named hook
	// (e.g. "hook-pre-tool").
	DecodeHook(hook string, raw []byte) (Event, error)
	// EncodeDecision renders d for the harness, or nil when the harness has
	// nothing to receive.
	EncodeDecision(d Decision) []byte

	// SessionLogPath returns the transcript for a starting session, given
	// the hook payload's session ID, cwd and transcript path ("" if unknown).
	SessionLogPath(sessionID, cwd, transcriptPath string) string
	// LaunchLogPath returns the transcript path known before launch, or ""
	// when the harness only reports it once the session starts. configDir is
	// the account's config dir ("" = default).
	LaunchLogPath(configDir, workDir, sessionID string) string
	// ParseTranscript parses a transcript into log entries in the Claude
	// Code shape every reader understands.
	ParseTranscript(r io.Reader) ([]logstream.Entry, error)
	// ClassifyPane reads captured pane text.
	ClassifyPane(text string) panestate.Verdict
}

var registry = map[string]Harness{
	Claude: claudeHarness{},
	Pi:     piHarness{},
}

// Names lists the registered harnesses, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Lookup returns the named harness. "" is Default. An unknown name is an
// error naming the valid ones.
func Lookup(name string) (Harness, error) {
	if name == "" {
		name = Default
	}
	if h, ok := registry[strings.ToLower(name)]; ok {
		return h, nil
	}
	return nil, fmt.Errorf("unknown harness %q (valid: %s)", name, strings.Join(Names(), ", "))
}

// For returns the named harness, or Default when name is empty or unknown.
// Use it for recorded values (windowstate, restore.json), where an unknown
// name must not break readers; use Lookup for user input.
func For(name string) Harness {
	if h, err := Lookup(name); err == nil {
		return h
	}
	return registry[Default]
}

// Canonical returns the recorded form of a harness name: "" for Default, so
// Claude records stay byte-identical to the ones written before harnesses.
func Canonical(name string) string {
	name = strings.ToLower(name)
	if name == Default {
		return ""
	}
	return name
}

// Resolve picks the harness for a launch: the explicit override (dispatch
// --harness), then the agent's config, then its frontmatter, then Default.
// Config wins over frontmatter for the same reason it does for the model:
// the user's own config outranks a definition file that may have come with
// a cloned repo.
func Resolve(override, configured, frontmatter string) (Harness, error) {
	for _, n := range []string{override, configured, frontmatter} {
		if n != "" {
			return Lookup(n)
		}
	}
	return Lookup(Default)
}

// marshalLine renders v like json.Encoder.Encode (trailing newline).
func marshalLine(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return append(b, '\n')
}
