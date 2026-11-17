package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/launchd"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// CronLaunchdLabel is the LaunchAgent label of the cron ticker.
const CronLaunchdLabel = "com.blsmth.agentctl.cron"

// cronTickInterval is the launchd StartInterval, in seconds.
const cronTickInterval = 60

// cronLaunchdLog captures the ticker's stdout/stderr, under the cron dir.
const cronLaunchdLog = "launchd.log"

// cronFallbackPath is appended to the ticker's PATH after the directories of
// the binaries it needs, so system tools (git, sh) resolve too.
var cronFallbackPath = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// cronInstallEnv is the install/uninstall seam: no test may exec launchctl
// or resolve the real binary.
type cronInstallEnv struct {
	goos     string
	exe      func() (string, error)
	lookPath func(string) (string, error)
	stateDir func() (string, error)
	agent    func() (launchd.Agent, error)
}

var defaultCronInstallEnv = cronInstallEnv{
	goos:     runtime.GOOS,
	exe:      cronExe,
	lookPath: exec.LookPath,
	stateDir: cron.Dir,
	agent:    launchd.NewAgent,
}

// cronExe is the installed agentctl binary; a temporary (go run / go test)
// binary is refused because it would vanish.
func cronExe() (string, error) {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return "", fmt.Errorf("locating executable: %w", err)
	}
	if selfpath.IsEphemeral(exe) {
		return "", fmt.Errorf("refusing to install a temporary binary (%s): install agentctl first", exe)
	}
	return exe, nil
}

// cronPlistExists reports whether the LaunchAgent plist is on disk. Off
// macOS it cannot tell and reports true.
func cronPlistExists() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	_, err = os.Stat(launchd.PlistPath(home, CronLaunchdLabel))
	return err == nil
}

// tickPath builds the ticker's PATH: the directories of the agentctl binary
// and of claude, tmux and gh as resolved now, then the system fallbacks.
func (e cronInstallEnv) tickPath(exe string) string {
	var dirs []string
	add := func(d string) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	add(filepath.Dir(exe))
	for _, bin := range []string{"claude", "tmux", "gh"} {
		if p, err := e.lookPath(bin); err == nil {
			add(filepath.Dir(p))
		}
	}
	for _, d := range cronFallbackPath {
		add(d)
	}
	return strings.Join(dirs, ":")
}

// tickJob is the LaunchAgent: `agentctl cron tick` every 60s and at load,
// from the cron state dir, with a UTF-8 locale (#69).
func (e cronInstallEnv) tickJob(exe, dir string) launchd.Job {
	return launchd.Job{
		Label:            CronLaunchdLabel,
		ProgramArguments: []string{exe, "cron", "tick"},
		RunAtLoad:        true,
		StartInterval:    cronTickInterval,
		WorkingDirectory: dir,
		LogPath:          filepath.Join(dir, cronLaunchdLog),
		Env: []launchd.EnvVar{
			{Key: "PATH", Value: e.tickPath(exe)},
			{Key: "LANG", Value: launchd.UTF8Locale},
			{Key: "LC_ALL", Value: launchd.UTF8Locale},
		},
	}
}

// crontabLine is the non-macOS equivalent of the LaunchAgent.
func (e cronInstallEnv) crontabLine(exe, dir string) string {
	return fmt.Sprintf("* * * * * cd %s && PATH=%s LANG=%s LC_ALL=%s %s cron tick >> %s 2>&1",
		shellquote.Quote(dir), shellquote.Quote(e.tickPath(exe)), launchd.UTF8Locale, launchd.UTF8Locale,
		shellquote.Quote(exe), shellquote.Quote(filepath.Join(dir, cronLaunchdLog)))
}

// RunCronInstall installs the ticker LaunchAgent (macOS) or prints a crontab
// line (elsewhere).
func RunCronInstall(w io.Writer) error { return defaultCronInstallEnv.install(w) }

func (e cronInstallEnv) install(w io.Writer) error {
	exe, err := e.exe()
	if err != nil {
		return err
	}
	dir, err := e.stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if e.goos != "darwin" {
		fmt.Fprintln(w, "# Add this line with `crontab -e`:")
		fmt.Fprintln(w, e.crontabLine(exe, dir))
		return nil
	}
	a, err := e.agent()
	if err != nil {
		return err
	}
	path, err := a.Install(CronLaunchdLabel, e.tickJob(exe, dir).Plist())
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Installed %s\nagentctl cron tick now runs every %ds. Logs: %s\n", path, cronTickInterval, filepath.Join(dir, cronLaunchdLog))
	return nil
}

// RunCronUninstall removes the ticker LaunchAgent.
func RunCronUninstall(w io.Writer) error { return defaultCronInstallEnv.uninstall(w) }

func (e cronInstallEnv) uninstall(w io.Writer) error {
	if e.goos != "darwin" {
		fmt.Fprintln(w, "Remove the `agentctl cron tick` line with `crontab -e`.")
		return nil
	}
	a, err := e.agent()
	if err != nil {
		return err
	}
	path, err := a.Uninstall(CronLaunchdLabel)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Removed %s\n", path)
	return nil
}
