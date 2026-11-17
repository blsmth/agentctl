package serve

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/selfpath"
)

// LaunchdLabel is the LaunchAgent label for agentctl serve.
const LaunchdLabel = "com.blsmth.agentctl.serve"

// LaunchdLogFile captures stdout/stderr of the launchd job (startup banner,
// panics); the structured log is serve.log.
const LaunchdLogFile = "launchd.log"

// PlistPath returns ~/Library/LaunchAgents/com.blsmth.agentctl.serve.plist.
func PlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist")
}

// ServiceSpec is what the service definition runs.
type ServiceSpec struct {
	// Exe is the agentctl binary (unresolved path, so upgrades through a
	// symlink keep working).
	Exe string
	// Dir is the serve state dir, used as working directory and log location.
	Dir string
	// Path is the PATH the server runs with (it shells out to tmux in later
	// PRs, which launchd's minimal PATH would not find).
	Path string
}

// Plist renders the LaunchAgent: run `agentctl serve` at load and keep it
// alive.
func Plist(spec ServiceSpec) []byte {
	var b bytes.Buffer
	str := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return "<string>" + e.String() + "</string>"
	}
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t%s\n", str(LaunchdLabel))
	fmt.Fprintf(&b, "\t<key>ProgramArguments</key>\n\t<array>\n\t\t%s\n\t\t%s\n\t</array>\n", str(spec.Exe), str("serve"))
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t%s\n", str(spec.Dir))
	logPath := filepath.Join(spec.Dir, LaunchdLogFile)
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t%s\n", str(logPath))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t%s\n", str(logPath))
	if spec.Path != "" {
		fmt.Fprintf(&b, "\t<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>PATH</key>\n\t\t%s\n\t</dict>\n", str(spec.Path))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// SystemdUnit renders a systemd user unit equivalent of the LaunchAgent.
func SystemdUnit(spec ServiceSpec) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=agentctl serve (mobile web app server)\nAfter=network-online.target\n\n")
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s serve\n", systemdQuote(spec.Exe))
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", systemdQuote(spec.Dir))
	if spec.Path != "" {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote("PATH="+spec.Path))
	}
	b.WriteString("Restart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return strconv.Quote(s)
}

// CurrentServiceSpec describes the running binary. It refuses a temporary
// (go run / go test) binary, which would vanish.
func CurrentServiceSpec(dir string) (ServiceSpec, error) {
	exe, err := os.Executable()
	if err != nil {
		return ServiceSpec{}, fmt.Errorf("locating executable: %w", err)
	}
	if selfpath.IsEphemeral(exe) {
		return ServiceSpec{}, fmt.Errorf("refusing to install a temporary binary (%s): install agentctl first", exe)
	}
	return ServiceSpec{Exe: exe, Dir: dir, Path: os.Getenv("PATH")}, nil
}

// runner runs an external command; a seam for tests.
type runner func(name string, args ...string) ([]byte, error)

func execRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Launchd installs and removes the LaunchAgent.
type Launchd struct {
	home  string
	uid   int
	run   runner
	sleep func(time.Duration)
}

// NewLaunchd returns the launchd manager for the current user.
func NewLaunchd() (*Launchd, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Launchd{home: home, uid: os.Getuid(), run: execRunner, sleep: time.Sleep}, nil
}

func (l *Launchd) domain() string { return "gui/" + strconv.Itoa(l.uid) }

// Install writes the plist and (re)bootstraps it, so a second install picks
// up a changed binary path. It returns the plist path.
func (l *Launchd) Install(spec ServiceSpec) (string, error) {
	path := PlistPath(l.home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, Plist(spec), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	// bootout fails when the job is not loaded; that is the normal case.
	_, _ = l.run("launchctl", "bootout", l.domain()+"/"+LaunchdLabel)
	// bootout returns before the old job is fully gone, and bootstrapping
	// over it fails with an I/O error, so retry briefly.
	var out []byte
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if out, err = l.run("launchctl", "bootstrap", l.domain(), path); err == nil {
			return path, nil
		}
		l.sleep(500 * time.Millisecond)
	}
	return path, fmt.Errorf("launchctl bootstrap %s %s: %v: %s", l.domain(), path, err, bytes.TrimSpace(out))
}

// Uninstall unloads the job and removes the plist. Missing pieces are fine.
func (l *Launchd) Uninstall() (string, error) {
	path := PlistPath(l.home)
	_, _ = l.run("launchctl", "bootout", l.domain()+"/"+LaunchdLabel)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return path, fmt.Errorf("removing %s: %w", path, err)
	}
	return path, nil
}
