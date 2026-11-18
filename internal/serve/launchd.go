package serve

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/launchd"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/servectl"
)

// LaunchdLabel is the LaunchAgent label for agentctl serve.
const LaunchdLabel = servectl.Label

// LaunchdLogFile captures stdout/stderr of the launchd job (startup banner,
// panics); the structured log is serve.log.
const LaunchdLogFile = "launchd.log"

// PlistPath returns ~/Library/LaunchAgents/com.blsmth.agentctl.serve.plist.
func PlistPath(home string) string {
	return launchd.PlistPath(home, LaunchdLabel)
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
	var env []launchd.EnvVar
	if spec.Path != "" {
		env = append(env, launchd.EnvVar{Key: "PATH", Value: spec.Path})
	}
	// LANG matters: without a UTF-8 locale tmux replaces tabs and non-ASCII
	// bytes in -F output with "_", which breaks window listing.
	env = append(env, launchd.EnvVar{Key: "LANG", Value: launchd.UTF8Locale})
	return launchd.Job{
		Label:            LaunchdLabel,
		ProgramArguments: []string{spec.Exe, "serve"},
		RunAtLoad:        true,
		KeepAlive:        true,
		ProcessType:      "Interactive",
		WorkingDirectory: spec.Dir,
		LogPath:          filepath.Join(spec.Dir, LaunchdLogFile),
		Env:              env,
	}.Plist()
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
	b.WriteString("Environment=LANG=en_US.UTF-8\n")
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
	exe, err := selfpath.Spawnable()
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
	return launchd.ExecRunner(name, args...)
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

// agent is the shared launchd manager over l's seams.
func (l *Launchd) agent() launchd.Agent {
	return launchd.Agent{Home: l.home, UID: l.uid, Run: launchd.Runner(l.run), Sleep: l.sleep}
}

// Install writes the plist and (re)bootstraps it, so a second install picks
// up a changed binary path. It returns the plist path.
func (l *Launchd) Install(spec ServiceSpec) (string, error) {
	return l.agent().Install(LaunchdLabel, Plist(spec))
}

// Uninstall unloads the job and removes the plist. Missing pieces are fine.
func (l *Launchd) Uninstall() (string, error) {
	return l.agent().Uninstall(LaunchdLabel)
}
