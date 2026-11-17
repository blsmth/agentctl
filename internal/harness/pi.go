package harness

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/ids"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// piExtension is the bridge extension pi loads with -e. It forwards pi's
// lifecycle events to `agentctl hook-* --harness pi` (JSON on stdin) and
// turns a deny from hook-validate-bash into a blocked tool call.
//
//go:embed pi/agentctl-pi.ts
var piExtension []byte

// PiExtensionFile is the file name the bridge extension is written under.
const PiExtensionFile = "agentctl-pi.ts"

// piHarness is pi (earendil-works/pi). It has no shell-hook config and no
// permission system: agentctl's bridge extension is its hook layer, and the
// bash gate is its only approval layer.
type piHarness struct{}

func (piHarness) Name() string   { return Pi }
func (piHarness) Binary() string { return "pi" }

func (piHarness) Caps() Caps {
	return Caps{
		PreassignSessionID: true,
		ResumeByID:         true,
		BlockingPreTool:    true,
		ContextInjection:   true,
		Backends:           true,
		RegistersBackends:  true,
	}
}

// piThinking maps agentctl effort levels onto pi's --thinking levels.
// "auto" (and anything unknown) leaves pi's own default.
var piThinking = map[string]string{
	"low":    "low",
	"medium": "medium",
	"high":   "high",
	"xhigh":  "xhigh",
	"max":    "max",
}

// Command returns the pi command line:
//
//	pi --session-id <id> -n <name> [--provider <p>] [--model <m>] [--thinking <lvl>]
//	   -e <bridge> --append-system-prompt <file> [--fork <id>] [-- <message>]
//
// pi's --session-id both pre-assigns a new session's ID and resumes an
// existing one, so Resume is passed through it too. pi has no --agent and no
// --permission-mode: the persona travels in the system-prompt file and
// PermissionMode is ignored.
func (piHarness) Command(inv Invocation) (string, error) {
	var b strings.Builder
	b.WriteString("pi")
	sid := inv.SessionID
	if inv.Resume != "" && !inv.ForkSession {
		sid = inv.Resume
	}
	if sid != "" {
		b.WriteString(" --session-id " + shellquote.Quote(sid))
	}
	if inv.Name != "" {
		b.WriteString(" -n " + shellquote.Quote(inv.Name))
	}
	if inv.Provider != "" {
		b.WriteString(" --provider " + shellquote.Quote(inv.Provider))
	}
	if inv.Model != "" {
		b.WriteString(" --model " + shellquote.Quote(inv.Model))
	}
	if lvl, ok := piThinking[inv.Effort]; ok {
		b.WriteString(" --thinking " + lvl)
	}
	if inv.ExtensionPath != "" {
		b.WriteString(" -e " + shellquote.Quote(inv.ExtensionPath))
	}
	if inv.SystemPromptFile != "" {
		b.WriteString(" --append-system-prompt " + shellquote.Quote(inv.SystemPromptFile))
	}
	if inv.ForkSession {
		if inv.Resume == "" {
			return "", fmt.Errorf("pi: fork needs a session to fork from")
		}
		b.WriteString(" --fork " + shellquote.Quote(inv.Resume))
	}
	if inv.Message != "" {
		// "--" so a prompt starting with "-" is not read as a flag.
		b.WriteString(" -- " + shellquote.Quote(inv.Message))
	}
	return b.String(), nil
}

// RestoreCommand reopens the saved session by ID (pi resumes it, or creates
// it when the file is gone). A session that may not be resumed (a duplicate
// in the same restore batch) gets a fresh ID.
func (h piHarness) RestoreCommand(inv Invocation, sessionID string, resume bool) (string, error) {
	inv.Resume, inv.ForkSession = "", false
	inv.SessionID = sessionID
	if !resume || sessionID == "" {
		inv.SessionID = ids.NewUUID()
	}
	return h.Command(inv)
}

// LaunchEnv: pi needs nothing beyond the generic agentctl window env.
func (piHarness) LaunchEnv(EnvParams) map[string]string { return map[string]string{} }

// PrepareWorkspace writes the bridge extension into ws.Dir.
func (piHarness) PrepareWorkspace(ws Workspace) (Prepared, error) {
	if ws.Dir == "" {
		return Prepared{}, fmt.Errorf("pi: no directory for the bridge extension")
	}
	path, err := WritePiExtension(ws.Dir)
	if err != nil {
		return Prepared{}, err
	}
	prep := Prepared{ExtensionPath: path}
	if ws.Backend != nil {
		if prep.Provider, prep.Model, prep.Env, err = piBackendProvider(*ws.Backend); err != nil {
			return Prepared{}, err
		}
	}
	return prep, nil
}

// WritePiExtension writes the embedded bridge extension to dir and returns
// its path. The write goes through a temp file and a rename, so a pi
// starting concurrently never loads a half-written file, and a symlink
// planted at the destination is replaced rather than written through.
func WritePiExtension(dir string) (string, error) {
	path := filepath.Join(dir, PiExtensionFile)
	f, err := os.CreateTemp(dir, ".agentctl-pi-*.ts")
	if err != nil {
		return "", fmt.Errorf("writing pi extension: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(piExtension); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("writing pi extension: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("writing pi extension: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("writing pi extension: %w", err)
	}
	return path, nil
}

// PiExtensionSource returns the embedded bridge extension.
func PiExtensionSource() []byte { return append([]byte(nil), piExtension...) }

// SessionLogPath is the session file the bridge reports at session start.
func (piHarness) SessionLogPath(_, _, transcriptPath string) string {
	if transcriptPath != "" && filepath.IsAbs(transcriptPath) {
		return transcriptPath
	}
	return ""
}

// LaunchLogPath is unknown before launch: pi names the file
// <timestamp>_<session id>.jsonl, and the bridge reports it at session start.
func (piHarness) LaunchLogPath(_, _, _ string) string { return "" }

// ParseTranscript parses a pi session file; records are normalized into
// Claude Code's shape (internal/transcript) by logstream.
func (piHarness) ParseTranscript(r io.Reader) ([]logstream.Entry, error) {
	return logstream.ReadAll(r)
}

// ClassifyPane: pi has no permission dialogs and agentctl has no reader for
// its footer yet, so its panes are never classified.
func (piHarness) ClassifyPane(string) panestate.Verdict { return panestate.Unknown }

// PiSessionsDir returns pi's session storage root:
// $PI_CODING_AGENT_SESSION_DIR, else $PI_CODING_AGENT_DIR/sessions, else
// ~/.pi/agent/sessions. "" when the home dir is unknown.
func PiSessionsDir() string {
	if d := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return filepath.Join(d, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".pi", "agent", "sessions")
}
