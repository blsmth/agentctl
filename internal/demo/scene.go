// Package demo builds a fake, fully isolated agentctl world for screenshots:
// a private tmux server, a temp HOME with fake repos, agents and Claude
// transcripts, and windows running canned Claude Code output instead of
// `claude`. A scene file (demo/scenes/<name>.yaml) describes one world and the
// keys a vhs tape sends to it; see demo/scenes/README.md for the schema.
//
// Every per-window record is written through internal/windowstate, so the
// demo stays in step with the schema the hooks write.
package demo

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/blsmth/agentctl/internal/theme"
)

// Scene is one demo world plus the steps that capture it.
type Scene struct {
	// Name is the scene's file name without extension; set by Load.
	Name string `yaml:"-"`
	// Dir is the directory the scene file is in; set by Load.
	Dir string `yaml:"-"`

	Description string    `yaml:"description"`
	Terminal    Terminal  `yaml:"terminal"`
	Config      Config    `yaml:"config"`
	Projects    []Project `yaml:"projects"`
	Windows     []Window  `yaml:"windows"`
	Steps       []Step    `yaml:"steps"`
	// Phone, when set, drives the phone web app over the same fleet.
	Phone *Phone `yaml:"phone"`
	// Output is "png" (default: one file per screenshot step) or "gif" (the
	// whole visible tape is recorded; screenshot steps still write PNGs).
	Output string `yaml:"output"`
}

// Terminal is the vhs terminal the scene is captured in.
type Terminal struct {
	Cols     int    `yaml:"cols"`
	Rows     int    `yaml:"rows"`
	Theme    string `yaml:"theme"`     // vhs theme name
	FontSize int    `yaml:"font_size"` // vhs font size in px
	Padding  int    `yaml:"padding"`   // vhs padding in px
	// HubLeft is the hub list's width in columns, as if dragged there (the
	// live hub otherwise caps the list at 56). 0 keeps the default.
	HubLeft int `yaml:"hub_left"`
}

// Config is the subset of agentctl config a scene may set. Everything that
// could reach the network or the desktop is forced off regardless.
type Config struct {
	Theme            string `yaml:"theme"`             // agentctl theme
	DispatchApproval string `yaml:"dispatch_approval"` // all, non-review, off
	StatuslineFormat string `yaml:"statusline_format"` // rich, complementary, full, mobile, minimal
	// Serve fakes a running agentctl serve (a stub listening on the control
	// socket in the demo's own state dir), so the hub shows "📱 serve".
	Serve bool `yaml:"serve"`
}

// Project is one fake git repository and its agentctl session.
type Project struct {
	Name    string   `yaml:"name"`
	Branch  string   `yaml:"branch"`  // default main
	Commits []string `yaml:"commits"` // commit subjects, oldest first
	Dirty   []string `yaml:"dirty"`   // files left modified in the working tree
}

// Window is one fake agent window.
type Window struct {
	Key      string `yaml:"key"`
	Project  string `yaml:"project"`
	Agent    string `yaml:"agent"`
	Instance string `yaml:"instance"` // default: agent, then agent:2, agent:3 ...
	Mode     string `yaml:"mode"`     // bare (default), dispatched, adhoc, review-pr
	State    string `yaml:"state"`    // working, blocked, idle (default), done
	// Seen overrides the ✓ rule: default false for done (shows ✓), true
	// otherwise.
	Seen *bool `yaml:"seen"`

	Title       string   `yaml:"title"` // short title
	Kind        string   `yaml:"kind"`  // short-title kind (go, review, docs ...)
	Ticket      int      `yaml:"ticket"`
	TicketTitle string   `yaml:"ticket_title"`
	Task        string   `yaml:"task"`
	ReviewPR    int      `yaml:"review_pr"`
	PR          int      `yaml:"pr"` // PR this window opened
	Branch      string   `yaml:"branch"`
	Worktree    *bool    `yaml:"worktree"`   // default: true for dispatched, adhoc and review-pr
	Dirty       []string `yaml:"dirty"`      // files left modified in the window's worktree
	SpawnedBy   string   `yaml:"spawned_by"` // key of the dispatching window (same project)

	Model    string  `yaml:"model"`
	CostUSD  float64 `yaml:"cost_usd"`
	CtxPct   int     `yaml:"ctx_pct"`
	Turns    int     `yaml:"turns"`
	Since    string  `yaml:"since"`     // how long ago the window entered its state, e.g. 4m
	LastTool string  `yaml:"last_tool"` // last tool detail; default: derived from the transcript

	PendingTool   string      `yaml:"pending_tool"`   // blocked: tool awaiting permission
	PendingDetail string      `yaml:"pending_detail"` // blocked: its command or path
	Permission    *Permission `yaml:"permission"`     // blocked: dialog drawn in the pane

	Spinner    string        `yaml:"spinner"`    // working: the "✻ Verb… (38s · ↓ 2.1k tokens)" line
	Transcript TranscriptRef `yaml:"transcript"` // fixture name or inline transcript
	// Approval makes the pane run the real hook-validate-bash with this
	// command when Enter is pressed in it, which opens agentctl's dispatch
	// approval popup the way Claude Code's PreToolUse hook would.
	Approval string `yaml:"approval"`
	// Select makes this window the session's current window.
	Select bool `yaml:"select"`
}

// Permission is the permission dialog a blocked pane shows.
type Permission struct {
	Title       string `yaml:"title"` // default "Bash command"
	Command     string `yaml:"command"`
	Description string `yaml:"description"`
	Option2     string `yaml:"option2"` // the "Yes, and don't ask again ..." text
}

// Step is one tape action. Exactly one field is set. The scalar forms
// "screenshot", "hide" and "show" are accepted as shorthand.
type Step struct {
	Attach     string `yaml:"attach"`     // window key: attach a client to it
	Prefix     string `yaml:"prefix"`     // key sent after the tmux prefix (C-b)
	Type       string `yaml:"type"`       // text typed as-is
	Key        string `yaml:"key"`        // Enter, Escape, Tab, Space, Backspace, Up, Down, Left, Right
	Ctrl       string `yaml:"ctrl"`       // a letter sent with Ctrl
	Repeat     int    `yaml:"repeat"`     // with key: how many times (default 1)
	Sleep      string `yaml:"sleep"`      // Go duration
	Wait       string `yaml:"wait"`       // regexp the screen must match
	Screenshot string `yaml:"screenshot"` // optional file suffix
	Hide       bool   `yaml:"hide"`
	Show       bool   `yaml:"show"`

	// shot marks a screenshot step (Screenshot may be empty).
	shot bool
}

var (
	validModes  = []string{"bare", "dispatched", "adhoc", "review-pr"}
	validStates = []string{"working", "blocked", "idle", "done"}
	validKeys   = []string{"Enter", "Escape", "Tab", "Space", "Backspace", "Up", "Down", "Left", "Right", "PageUp", "PageDown"}
	validFormat = []string{"", "rich", "complementary", "full", "mobile", "minimal"}
	validApprov = []string{"", "all", "non-review", "off"}
	nameRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	keyRE       = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// LoadScene reads, strictly decodes, defaults and validates a scene file.
func LoadScene(path string) (*Scene, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading scene: %w", err)
	}
	s, err := ParseScene(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving scene path: %w", err)
	}
	s.Name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	s.Dir = filepath.Dir(abs)
	return s, nil
}

// ParseScene decodes scene YAML (unknown keys are errors), applies defaults
// and validates it.
func ParseScene(data []byte) (*Scene, error) {
	var s Scene
	if err := strictDecode(data, &s); err != nil {
		return nil, err
	}
	s.applyDefaults()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// strictDecode decodes YAML into v, rejecting unknown keys.
func strictDecode(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, errEOF) {
			return fmt.Errorf("empty document")
		}
		return err
	}
	return nil
}

// errEOF is what yaml.v3 returns for an empty document.
var errEOF = func() error {
	var v struct{}
	return yaml.NewDecoder(bytes.NewReader(nil)).Decode(&v)
}()

// strictNode re-decodes a node strictly: yaml.v3's Node.Decode ignores
// KnownFields, so custom unmarshalers round-trip through bytes.
func strictNode(n *yaml.Node, v any) error {
	data, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	if err := strictDecode(data, v); err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	return nil
}

// UnmarshalYAML accepts the scalar shorthands and a strict mapping.
func (st *Step) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		switch n.Value {
		case "screenshot":
			st.shot = true
		case "hide":
			st.Hide = true
		case "show":
			st.Show = true
		default:
			return fmt.Errorf("line %d: unknown step %q (want screenshot, hide, show or a mapping)", n.Line, n.Value)
		}
		return nil
	}
	type plain Step
	var p plain
	if err := strictNode(n, &p); err != nil {
		return err
	}
	*st = Step(p)
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "screenshot" {
			st.shot = true
		}
	}
	return nil
}

// IsScreenshot reports whether the step takes a screenshot.
func (st Step) IsScreenshot() bool { return st.shot }

func (s *Scene) applyDefaults() {
	t := &s.Terminal
	if t.Cols == 0 {
		t.Cols = 160
	}
	if t.Rows == 0 {
		t.Rows = 45
	}
	if t.Theme == "" {
		t.Theme = "Catppuccin Mocha"
	}
	if t.FontSize == 0 {
		t.FontSize = 14
	}
	if t.Padding == 0 {
		t.Padding = 20
	}
	if s.Output == "" {
		s.Output = "png"
	}
	if s.Config.DispatchApproval == "" {
		s.Config.DispatchApproval = "all"
	}
	if s.Phone != nil {
		s.Phone.applyDefaults()
	}
	for i := range s.Projects {
		if s.Projects[i].Branch == "" {
			s.Projects[i].Branch = "main"
		}
		if len(s.Projects[i].Commits) == 0 {
			s.Projects[i].Commits = []string{"Initial commit"}
		}
	}
	counts := map[string]int{}
	for i := range s.Windows {
		w := &s.Windows[i]
		if w.Mode == "" {
			w.Mode = "bare"
		}
		if w.State == "" {
			w.State = "idle"
		}
		if w.Model == "" {
			w.Model = "claude-opus-5-5"
		}
		if w.Instance == "" && w.Agent != "" {
			k := w.Project + "\x00" + w.Agent
			counts[k]++
			w.Instance = w.Agent
			if n := counts[k]; n > 1 {
				w.Instance = fmt.Sprintf("%s:%d", w.Agent, n)
			}
		}
		if w.Branch == "" {
			w.Branch = w.defaultBranch()
		}
		if w.State == "blocked" && w.Permission != nil && w.Permission.Title == "" {
			w.Permission.Title = "Bash command"
		}
	}
}

// defaultBranch is the branch agentctl would have cut for this window, or ""
// (the project's branch) for a bare one.
func (w Window) defaultBranch() string {
	switch {
	case w.Mode == "review-pr":
		return fmt.Sprintf("agent/%s/review-pr-%d-demo", w.Agent, w.ReviewPR)
	case w.Mode == "dispatched" && w.Ticket > 0:
		return fmt.Sprintf("agent/%s/issue-%d", w.Agent, w.Ticket)
	case w.Mode == "dispatched" || w.Mode == "adhoc":
		return fmt.Sprintf("agent/%s/adhoc-%s", w.Agent, w.Key)
	}
	return ""
}

// HasWorktree reports whether the window runs in its own git worktree.
func (w Window) HasWorktree() bool {
	if w.Worktree != nil {
		return *w.Worktree
	}
	return w.Mode != "bare"
}

// IsSeen is the window's effective Seen flag.
func (w Window) IsSeen() bool {
	if w.Seen != nil {
		return *w.Seen
	}
	return w.State != "done"
}

// SinceDuration parses Since (0 when empty).
func (w Window) SinceDuration() time.Duration {
	d, _ := time.ParseDuration(w.Since)
	return d
}

// Validate reports the first problem with the scene, naming the field.
func (s *Scene) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	t := s.Terminal
	if t.Cols < 40 || t.Cols > 400 {
		add("terminal.cols: %d out of range 40-400", t.Cols)
	}
	if t.HubLeft != 0 && (t.HubLeft < 30 || t.HubLeft > t.Cols-21) {
		add("terminal.hub_left: %d out of range 30-%d", t.HubLeft, t.Cols-21)
	}
	if t.Rows < 8 || t.Rows > 200 {
		add("terminal.rows: %d out of range 8-200", t.Rows)
	}
	if t.FontSize < 8 || t.FontSize > 48 {
		add("terminal.font_size: %d out of range 8-48", t.FontSize)
	}
	if s.Output != "png" && s.Output != "gif" {
		add("output: %q is not png or gif", s.Output)
	}
	if c := s.Config.Theme; c != "" && !slices.Contains(theme.Names(), c) {
		add("config.theme: unknown agentctl theme %q (have %s)", c, strings.Join(theme.Names(), ", "))
	}
	if !slices.Contains(validApprov, s.Config.DispatchApproval) {
		add("config.dispatch_approval: %q is not all, non-review or off", s.Config.DispatchApproval)
	}
	if !slices.Contains(validFormat, s.Config.StatuslineFormat) {
		add("config.statusline_format: unknown format %q", s.Config.StatuslineFormat)
	}

	if len(s.Projects) == 0 {
		add("projects: at least one project is required")
	}
	projects := map[string]bool{}
	for i, p := range s.Projects {
		f := fmt.Sprintf("projects[%d]", i)
		switch {
		case !nameRE.MatchString(p.Name):
			add("%s.name: %q must be lowercase letters, digits and dashes", f, p.Name)
		case projects[p.Name]:
			add("%s.name: duplicate project %q", f, p.Name)
		}
		projects[p.Name] = true
		for _, d := range p.Dirty {
			if err := checkRelPath(d); err != nil {
				add("%s.dirty: %v", f, err)
			}
		}
	}

	keys := map[string]int{}
	instances := map[string]bool{}
	for i, w := range s.Windows {
		f := fmt.Sprintf("windows[%d]", i)
		if w.Key != "" {
			f = fmt.Sprintf("windows[%d] (%s)", i, w.Key)
		}
		switch {
		case !keyRE.MatchString(w.Key):
			add("%s.key: %q must be letters, digits, - or _", f, w.Key)
		case keys[w.Key] > 0:
			add("%s.key: duplicate key %q", f, w.Key)
		}
		keys[w.Key] = i + 1
		if !projects[w.Project] {
			add("%s.project: unknown project %q", f, w.Project)
		}
		if !nameRE.MatchString(w.Agent) {
			add("%s.agent: %q must be lowercase letters, digits and dashes", f, w.Agent)
		}
		if instances[w.Project+"\x00"+w.Instance] {
			add("%s.instance: duplicate instance %q in project %s", f, w.Instance, w.Project)
		}
		instances[w.Project+"\x00"+w.Instance] = true
		if !slices.Contains(validModes, w.Mode) {
			add("%s.mode: %q is not one of %s", f, w.Mode, strings.Join(validModes, ", "))
		}
		if !slices.Contains(validStates, w.State) {
			add("%s.state: %q is not one of %s", f, w.State, strings.Join(validStates, ", "))
		}
		if w.Since != "" {
			if d, err := time.ParseDuration(w.Since); err != nil || d < 0 {
				add("%s.since: %q is not a duration like 4m or 2h", f, w.Since)
			}
		}
		if w.ReviewPR != 0 && w.Mode != "review-pr" {
			add("%s.review_pr: only valid with mode review-pr", f)
		}
		if w.Mode == "review-pr" && w.ReviewPR <= 0 {
			add("%s.review_pr: required for mode review-pr", f)
		}
		if w.Ticket != 0 && w.Mode != "dispatched" {
			add("%s.ticket: only valid with mode dispatched", f)
		}
		if w.Mode == "bare" && w.HasWorktree() {
			add("%s.worktree: bare windows run in the project checkout", f)
		}
		if len(w.Dirty) > 0 && !w.HasWorktree() {
			add("%s.dirty: needs a worktree (use projects[].dirty for the checkout)", f)
		}
		for _, d := range w.Dirty {
			if err := checkRelPath(d); err != nil {
				add("%s.dirty: %v", f, err)
			}
		}
		if w.State != "blocked" && (w.PendingTool != "" || w.PendingDetail != "" || w.Permission != nil) {
			add("%s.pending_tool/permission: only valid with state blocked", f)
		}
		if w.State == "blocked" && w.PendingTool == "" {
			add("%s.pending_tool: required with state blocked", f)
		}
		if w.Spinner != "" && w.State != "working" {
			add("%s.spinner: only valid with state working", f)
		}
		if w.CtxPct < 0 || w.CtxPct > 100 {
			add("%s.ctx_pct: %d out of range 0-100", f, w.CtxPct)
		}
		if w.Transcript.Name != "" && !nameRE.MatchString(w.Transcript.Name) {
			add("%s.transcript: fixture name %q must be lowercase letters, digits and dashes", f, w.Transcript.Name)
		}
		if w.Transcript.Inline != nil {
			if err := w.Transcript.Inline.validate(); err != nil {
				add("%s.transcript: %v", f, err)
			}
		}
	}
	selected := map[string]string{}
	for i, w := range s.Windows {
		f := fmt.Sprintf("windows[%d] (%s)", i, w.Key)
		if w.SpawnedBy != "" {
			j := keys[w.SpawnedBy]
			switch {
			case j == 0:
				add("%s.spawned_by: no window with key %q", f, w.SpawnedBy)
			case w.SpawnedBy == w.Key:
				add("%s.spawned_by: a window cannot spawn itself", f)
			case s.Windows[j-1].Project != w.Project:
				add("%s.spawned_by: %q is in project %s, not %s", f, w.SpawnedBy, s.Windows[j-1].Project, w.Project)
			case j-1 > i:
				add("%s.spawned_by: %q must be listed before the windows it spawns", f, w.SpawnedBy)
			}
			if w.Mode == "bare" {
				add("%s.spawned_by: bare windows have no dispatcher; use mode dispatched, adhoc or review-pr", f)
			}
		}
		if w.Select {
			if prev, ok := selected[w.Project]; ok {
				add("%s.select: %q is already selected in project %s", f, prev, w.Project)
			}
			selected[w.Project] = w.Key
		}
	}

	if len(s.Steps) == 0 {
		add("steps: at least one step is required")
	}
	shots := 0
	for i, st := range s.Steps {
		f := fmt.Sprintf("steps[%d]", i)
		if err := st.validate(keys); err != nil {
			add("%s: %v", f, err)
		}
		if st.IsScreenshot() {
			shots++
		}
	}
	if s.Output == "png" && shots == 0 && len(s.Steps) > 0 {
		add("steps: output png needs at least one screenshot step")
	}
	if s.Phone != nil {
		if err := s.Phone.validate(s); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (st Step) validate(keys map[string]int) error {
	set := 0
	for _, b := range []bool{st.Attach != "", st.Prefix != "", st.Type != "", st.Key != "", st.Ctrl != "",
		st.Sleep != "", st.Wait != "", st.shot, st.Hide, st.Show} {
		if b {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("exactly one action per step (attach, prefix, type, key, ctrl, sleep, wait, screenshot, hide, show), got %d", set)
	}
	if st.Repeat != 0 && st.Key == "" {
		return fmt.Errorf("repeat: only valid with key")
	}
	switch {
	case st.Attach != "" && keys[st.Attach] == 0:
		return fmt.Errorf("attach: no window with key %q", st.Attach)
	case st.Key != "" && !slices.Contains(validKeys, st.Key):
		return fmt.Errorf("key: %q is not one of %s", st.Key, strings.Join(validKeys, ", "))
	case st.Prefix != "" && len([]rune(st.Prefix)) != 1:
		return fmt.Errorf("prefix: %q must be a single key", st.Prefix)
	case st.Ctrl != "" && !regexp.MustCompile(`^[a-z]$`).MatchString(st.Ctrl):
		return fmt.Errorf("ctrl: %q must be one lowercase letter", st.Ctrl)
	case st.Repeat < 0 || st.Repeat > 50:
		return fmt.Errorf("repeat: %d out of range 0-50", st.Repeat)
	case st.Screenshot != "" && !keyRE.MatchString(st.Screenshot):
		return fmt.Errorf("screenshot: suffix %q must be letters, digits, - or _", st.Screenshot)
	}
	if st.Sleep != "" {
		if d, err := time.ParseDuration(st.Sleep); err != nil || d <= 0 || d > time.Minute {
			return fmt.Errorf("sleep: %q is not a duration up to 1m", st.Sleep)
		}
	}
	if st.Wait != "" {
		if _, err := regexp.Compile(st.Wait); err != nil {
			return fmt.Errorf("wait: %v", err)
		}
		if strings.Contains(st.Wait, "/") {
			return fmt.Errorf("wait: %q must not contain /", st.Wait)
		}
	}
	if strings.ContainsAny(st.Type, "\"`\n") {
		return fmt.Errorf("type: %q must not contain quotes, backticks or newlines", st.Type)
	}
	return nil
}

// checkRelPath rejects absolute paths and paths that escape their root.
func checkRelPath(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(filepath.Clean(p), "..") || strings.HasPrefix(p, ".git") {
		return fmt.Errorf("%q must be a relative path inside the repo", p)
	}
	return nil
}

// Window returns the window with key k.
func (s *Scene) Window(k string) (Window, bool) {
	for _, w := range s.Windows {
		if w.Key == k {
			return w, true
		}
	}
	return Window{}, false
}

// WindowsIn returns the project's windows in scene order.
func (s *Scene) WindowsIn(project string) []Window {
	var out []Window
	for _, w := range s.Windows {
		if w.Project == project {
			out = append(out, w)
		}
	}
	return out
}

// Agents returns the distinct agent names in the scene, sorted.
func (s *Scene) Agents() []string {
	var out []string
	for _, w := range s.Windows {
		if !slices.Contains(out, w.Agent) {
			out = append(out, w.Agent)
		}
	}
	slices.Sort(out)
	return out
}
