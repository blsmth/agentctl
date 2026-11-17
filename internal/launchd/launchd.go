// Package launchd renders macOS LaunchAgent property lists and loads or
// unloads them with launchctl. It is shared by `agentctl serve install` and
// `agentctl cron install`.
package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// UTF8Locale is the LANG/LC_ALL value every agentctl LaunchAgent sets.
// launchd starts jobs with no locale, and without a UTF-8 one tmux replaces
// tabs and non-ASCII bytes in -F output with "_", which breaks window
// listing (#69).
const UTF8Locale = "en_US.UTF-8"

// EnvVar is one EnvironmentVariables entry. A slice (not a map) keeps the
// rendered plist stable.
type EnvVar struct {
	Key, Value string
}

// Job describes a LaunchAgent. Zero-valued optional fields are omitted.
type Job struct {
	Label            string
	ProgramArguments []string
	RunAtLoad        bool
	KeepAlive        bool
	// StartInterval runs the job every N seconds; 0 omits the key.
	StartInterval    int
	ProcessType      string
	WorkingDirectory string
	// LogPath receives both stdout and stderr.
	LogPath string
	Env     []EnvVar
}

// Plist renders j as an XML property list.
func (j Job) Plist() []byte {
	var b bytes.Buffer
	str := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return "<string>" + e.String() + "</string>"
	}
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t%s\n", str(j.Label))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range j.ProgramArguments {
		fmt.Fprintf(&b, "\t\t%s\n", str(a))
	}
	b.WriteString("\t</array>\n")
	if j.RunAtLoad {
		b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	}
	if j.KeepAlive {
		b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	}
	if j.StartInterval > 0 {
		fmt.Fprintf(&b, "\t<key>StartInterval</key>\n\t<integer>%d</integer>\n", j.StartInterval)
	}
	if j.ProcessType != "" {
		fmt.Fprintf(&b, "\t<key>ProcessType</key>\n\t%s\n", str(j.ProcessType))
	}
	if j.WorkingDirectory != "" {
		fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t%s\n", str(j.WorkingDirectory))
	}
	if j.LogPath != "" {
		fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t%s\n", str(j.LogPath))
		fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t%s\n", str(j.LogPath))
	}
	if len(j.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, e := range j.Env {
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t%s\n", xmlEscape(e.Key), str(e.Value))
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func xmlEscape(s string) string {
	var e bytes.Buffer
	_ = xml.EscapeText(&e, []byte(s))
	return e.String()
}

// PlistPath returns ~/Library/LaunchAgents/<label>.plist under home.
func PlistPath(home, label string) string {
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// Runner runs an external command; a seam for tests.
type Runner func(name string, args ...string) ([]byte, error)

// ExecRunner runs the command for real.
func ExecRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Agent installs and removes LaunchAgents in the user's gui domain.
type Agent struct {
	Home  string
	UID   int
	Run   Runner
	Sleep func(time.Duration)
}

// NewAgent returns an Agent for the current user that runs launchctl for
// real.
func NewAgent() (Agent, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Agent{}, err
	}
	return Agent{Home: home, UID: os.Getuid(), Run: ExecRunner, Sleep: time.Sleep}, nil
}

// Domain is the launchctl domain target, gui/<uid>.
func (a Agent) Domain() string { return "gui/" + strconv.Itoa(a.UID) }

// Install writes the plist and (re)bootstraps it, so a second install picks
// up a changed binary path. It returns the plist path.
func (a Agent) Install(label string, plist []byte) (string, error) {
	path := PlistPath(a.Home, label)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, plist, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	// bootout fails when the job is not loaded; that is the normal case.
	_, _ = a.Run("launchctl", "bootout", a.Domain()+"/"+label)
	// bootout returns before the old job is fully gone, and bootstrapping
	// over it fails with an I/O error, so retry briefly.
	var out []byte
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if out, err = a.Run("launchctl", "bootstrap", a.Domain(), path); err == nil {
			return path, nil
		}
		a.Sleep(500 * time.Millisecond)
	}
	return path, fmt.Errorf("launchctl bootstrap %s %s: %v: %s", a.Domain(), path, err, bytes.TrimSpace(out))
}

// Uninstall unloads the job and removes the plist. Missing pieces are fine.
func (a Agent) Uninstall(label string) (string, error) {
	path := PlistPath(a.Home, label)
	_, _ = a.Run("launchctl", "bootout", a.Domain()+"/"+label)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return path, fmt.Errorf("removing %s: %w", path, err)
	}
	return path, nil
}

// Loaded reports whether launchd has the job loaded (launchctl print
// succeeds for it).
func (a Agent) Loaded(label string) bool {
	_, err := a.Run("launchctl", "print", a.Domain()+"/"+label)
	return err == nil
}
