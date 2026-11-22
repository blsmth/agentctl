package demo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/launchd"
)

// stubbed are programs the demo PATH shadows with a script that fails at
// once, so nothing in the demo can reach GitHub (gh) or raise a desktop
// notification. claude is shadowed too, by a fake that stays alive instead
// (claudeShim), so it can never spend tokens.
var stubbed = []string{"gh", "alerter", "notify-send", "terminal-notifier"}

// WriteWorld creates everything under the root that does not need tmux:
// the fake HOME, global config, agents, program stubs, repos with commits
// and worktrees, and the session transcripts. fixtures is the transcript
// fixture dir; now anchors every timestamp.
func WriteWorld(l Layout, s *Scene, fixtures string, now time.Time, self string) error {
	for _, d := range []string{l.Bin(), l.Tmp(), l.PaneDir(), l.ClaudeDir(), l.ConfigHome(),
		l.StateHome(), l.DataHome(), l.CacheHome(), filepath.Join(l.Home(), "code")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}
	steps := []func() error{
		func() error { return writeHome(l) },
		func() error { return writeStubs(l, self) },
		func() error { return writeConfig(l, s) },
		func() error { return writeAgents(l, s) },
		func() error { return writeFile(l.TmuxConf(), []byte(tmuxConf), 0o600) },
	}
	for _, f := range steps {
		if err := f(); err != nil {
			return err
		}
	}
	for _, p := range s.Projects {
		if err := writeRepo(l, p); err != nil {
			return fmt.Errorf("project %s: %w", p.Name, err)
		}
	}
	for _, w := range s.Windows {
		if err := writeWindowFiles(l, s, w, fixtures, now); err != nil {
			return fmt.Errorf("window %s: %w", w.Key, err)
		}
	}
	return writeSchedules(l, s, now)
}

// writeSchedules writes the scene's cron jobs to the global schedules.json,
// their runs to <stateDir>/cron/runs.ndjson and, so the hub shows no
// "scheduler not installed" warning, a placeholder ticker plist in the fake
// HOME (never loaded: nothing in the demo talks to launchd).
func writeSchedules(l Layout, s *Scene, now time.Time) error {
	if len(s.Schedules) == 0 {
		return nil
	}
	jobs := &cron.Jobs{}
	runsDir := filepath.Join(l.StateHome(), "agentctl", "cron")
	for _, sc := range s.Schedules {
		jobs.Jobs = append(jobs.Jobs, sc.Job(l.Repo(sc.Project)))
		for _, r := range sc.Runs {
			ago, _ := time.ParseDuration(r.Ago)
			e := cron.Entry{Time: now.Add(-ago), Job: sc.Name, Scheduled: now.Add(-ago), Result: r.Result, Note: r.Note}
			if err := cron.AppendRun(runsDir, e); err != nil {
				return fmt.Errorf("schedule %s: writing runs: %w", sc.Name, err)
			}
		}
	}
	dir := filepath.Join(l.ConfigHome(), "agentctl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := cron.SaveJobs(filepath.Join(dir, cron.SchedulesFile), jobs); err != nil {
		return err
	}
	plist := launchd.PlistPath(l.Home(), cron.LaunchdLabel)
	return writeFile(plist, []byte("<!-- agentctl demo placeholder: never loaded -->\n"), 0o600)
}

// tmuxConf is the demo server's whole config (the user's ~/.tmux.conf is
// never read): keep the server up between sessions and pass truecolor.
const tmuxConf = `set -s exit-empty off
set -g default-terminal "tmux-256color"
set -as terminal-features ",xterm-256color:RGB"
set -g escape-time 10
`

func writeHome(l Layout) error {
	zshrc := "# agentctl demo shell\nPROMPT='%F{blue}%~%f %F{magenta}❯%f '\nunsetopt BEEP\n"
	for name, body := range map[string]string{".zshrc": zshrc, ".hushlogin": "", ".bashrc": "PS1='\\w ❯ '\n"} {
		if err := writeFile(filepath.Join(l.Home(), name), []byte(body), 0o600); err != nil {
			return err
		}
	}
	return writeFile(filepath.Join(l.ClaudeDir(), "settings.json"), []byte("{}\n"), 0o600)
}

func writeStubs(l Layout, self string) error {
	if err := writeFile(filepath.Join(l.Bin(), "claude"), []byte(claudeShim(self)), 0o755); err != nil {
		return err
	}
	for _, name := range stubbed {
		body := fmt.Sprintf("#!/bin/sh\necho '%s: disabled in the agentctl demo' >&2\nexit 1\n", name)
		if err := writeFile(filepath.Join(l.Bin(), name), []byte(body), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// demoConfig is the global config: everything that could reach the network,
// the desktop or a real agent is off, whatever the scene says.
func demoConfig(s *Scene) config.Config {
	off, empty, cronOn := false, "", s.Config.Cron
	cfg := config.Config{
		NotifyCmd:         &empty,
		Agents:            map[string]config.AgentConfig{},
		Monitor:           &off,
		OpenPRInBrowser:   &off,
		BashPreview:       &off,
		Theme:             s.Config.Theme,
		DefaultAgent:      s.Config.DefaultAgent,
		DispatchApproval:  s.Config.DispatchApproval,
		StatuslineFormat:  s.Config.StatuslineFormat,
		NewWindowPosition: s.Config.NewWindowPosition,
		Metrics:           config.MetricsConfig{Enabled: false},
		Titles:            config.TitlesConfig{Enabled: &off},
		Cron:              config.CronConfig{Enabled: &cronOn},
	}
	for _, a := range s.Agents() {
		cfg.Agents[a] = config.AgentConfig{Alerts: false, AlertConfig: config.AlertConfig{}}
	}
	return cfg
}

func writeConfig(l Layout, s *Scene) error {
	data, err := json.MarshalIndent(demoConfig(s), "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	return writeFile(filepath.Join(l.ConfigHome(), "agentctl", "config.json"), append(data, '\n'), 0o600)
}

func writeAgents(l Layout, s *Scene) error {
	dir := filepath.Join(l.ClaudeDir(), "agents")
	for _, name := range s.Agents() {
		fm := agent.Frontmatter{Name: name, Description: "Demo " + name + " agent", Model: "opus"}
		if strings.Contains(name, "orchestrator") {
			fm.Role = agent.RoleOrchestrator
		}
		head, err := yaml.Marshal(fm)
		if err != nil {
			return fmt.Errorf("encoding agent %s: %w", name, err)
		}
		body := "---\n" + string(head) + "---\n\nYou are the demo " + name + ".\n"
		if err := writeFile(filepath.Join(dir, name+".md"), []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func writeRepo(l Layout, p Project) error {
	dir := l.Repo(p.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := git(l, dir, "init", "-q", "-b", p.Branch); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, ".gitignore"), []byte(".worktrees/\n.agentctl/\n"), 0o644); err != nil {
		return err
	}
	for i, subject := range p.Commits {
		name := "README.md"
		body := fmt.Sprintf("# %s\n\nDemo repository for agentctl screenshots.\n", p.Name)
		if i > 0 {
			name = "CHANGELOG.md"
			body = changelog(p.Commits[1 : i+1])
		}
		if err := writeFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return err
		}
		if err := git(l, dir, "add", "-A"); err != nil {
			return err
		}
		if err := git(l, dir, "commit", "-q", "-m", subject); err != nil {
			return err
		}
	}
	return dirty(dir, p.Dirty)
}

func changelog(subjects []string) string {
	var b strings.Builder
	b.WriteString("# Changelog\n\n")
	for _, s := range subjects {
		b.WriteString("- " + s + "\n")
	}
	return b.String()
}

// dirty leaves files modified (or created) in dir.
func dirty(dir string, files []string) error {
	for _, f := range files {
		path := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		fh, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = fh.WriteString("// work in progress\n")
		if cerr := fh.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// WorkDir is where a window's agent runs: its worktree, else the checkout.
func WorkDir(l Layout, w Window) string {
	if w.HasWorktree() {
		return l.Worktree(w.Project, w.Branch)
	}
	return l.Repo(w.Project)
}

// LogPath is the window's transcript path in the fake account.
func LogPath(l Layout, w Window) string {
	return claude.LogPathIn(l.ClaudeDir(), WorkDir(l, w), WindowSessionID(w))
}

// WindowSessionID is the window's stable Claude session ID.
func WindowSessionID(w Window) string { return SessionID(w.Project + "/" + w.Key) }

func writeWindowFiles(l Layout, s *Scene, w Window, fixtures string, now time.Time) error {
	if w.HasWorktree() {
		wt := WorkDir(l, w)
		if err := git(l, l.Repo(w.Project), "worktree", "add", "-q", "-b", w.Branch, wt); err != nil {
			return err
		}
		if err := dirty(wt, w.Dirty); err != nil {
			return err
		}
	}
	t, err := w.Transcript.Resolve(fixtures)
	if err != nil {
		return err
	}
	project := branchOf(s, w)
	data, err := t.JSONL(TranscriptMeta{
		SessionID: WindowSessionID(w),
		Cwd:       WorkDir(l, w),
		Branch:    project,
		Model:     w.Model,
		Project:   w.Project,
		End:       now.Add(-w.SinceDuration()),
	})
	if err != nil {
		return err
	}
	if err := writeFile(LogPath(l, w), data, 0o600); err != nil {
		return err
	}
	spec := PaneSpec{Window: w, Transcript: *t, Agentctl: l.Agentctl(), WorkDir: WorkDir(l, w),
		Branch: project, LogPath: LogPath(l, w), SessionID: WindowSessionID(w)}
	enc, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pane spec: %w", err)
	}
	return writeFile(l.PaneSpec(w.Project, w.Key), enc, 0o600)
}

// branchOf is the branch the window's agent is on.
func branchOf(s *Scene, w Window) string {
	if w.Branch != "" {
		return w.Branch
	}
	for _, p := range s.Projects {
		if p.Name == w.Project {
			return p.Branch
		}
	}
	return "main"
}

func git(l Layout, dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = l.Env("")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
