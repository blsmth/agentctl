package tui

import (
	"context"
	"image/color"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/theme"
)

// Popup size of the launch menu in terminal cells, tmux border included. The
// keybinding in internal/tmux uses the same numbers (tmux needs them before the
// model exists); the model lays itself out from the real size it is given.
const (
	MenuPopupWidth  = 120
	MenuPopupHeight = 40
)

// ollamaFetchTimeout bounds the live model-list request.
const ollamaFetchTimeout = 2 * time.Second

// sessionMaxAge and sessionLimit bound the recent-sessions list.
const (
	sessionMaxAge = 7 * 24 * time.Hour
	sessionLimit  = 5
)

// MenuSection groups agents in the list.
type MenuSection int

// The sections, in display order.
const (
	SectionPinned MenuSection = iota
	SectionProject
	SectionUser
	SectionBuiltin
)

// Label is the section header text.
func (s MenuSection) Label() string {
	switch s {
	case SectionPinned:
		return "Pinned"
	case SectionProject:
		return "Project"
	case SectionUser:
		return "User"
	default:
		return "Built-in"
	}
}

// MenuAgent represents an agent entry in the launch menu.
type MenuAgent struct {
	Name        string
	Description string
	Instances   []string // running instance window names
	Section     MenuSection
	Backend     string // backend the agent config selects ("" = Anthropic)
	Model       string // model the agent config selects
	Effort      string // effort_level the agent config selects
	Dir         string // default working directory
}

// MenuSession is a resumable session of an agent.
type MenuSession struct {
	ID      string
	When    time.Time // end time, or start time when there is no end
	Preview string
	Project string
	Live    bool // currently open in a window: resuming would collide
}

// MenuConfig is everything the menu needs from the outside world. The
// functions are injected so the model does no I/O of its own.
type MenuConfig struct {
	Agents   []MenuAgent
	Backends map[string]config.BackendConfig
	// FetchOllama lists the live models of an Ollama backend (name from
	// Backends). It is called from a tea.Cmd under a short timeout.
	FetchOllama func(ctx context.Context, backend string) ([]string, error)
	// LoadSessions lists recent sessions for an agent; called lazily from a
	// tea.Cmd, once per highlighted agent.
	LoadSessions func(agent string) []MenuSession
	// NousModels lists cached Nous catalog ids (nil = tokens.CatalogModelIDs).
	NousModels func() []string
	// Now is the clock (nil = time.Now).
	Now func() time.Time
}

// MenuResult is the output of the menu popup.
type MenuResult struct {
	Cancelled  bool
	Action     string // "switch", "launch", "resume", "create"
	Agent      string // base agent name
	Instance   string // specific instance to switch to
	Backend    string // dispatch.Request.Backend ("", "none" or a name)
	Model      string // dispatch.Request.Model
	Effort     string // dispatch.Request.Effort
	NoWorktree bool
	Dir        string
	SessionID  string // resume only
}

// Menu-specific styles. The hint and cursor styles are shared by other popups.
var (
	menuHintKeyStyle   lipgloss.Style
	menuHintLabelStyle lipgloss.Style
	menuHintDotStyle   lipgloss.Style
	cursorActiveStyle  lipgloss.Style

	menuTitleStyle   lipgloss.Style
	mnSectionStyle   lipgloss.Style
	mnRuleStyle      lipgloss.Style
	mnNameStyle      lipgloss.Style
	mnNameSelStyle   lipgloss.Style
	mnDescStyle      lipgloss.Style
	mnBadgeStyle     lipgloss.Style
	mnRunningStyle   lipgloss.Style
	mnStoppedStyle   lipgloss.Style
	mnLabelStyle     lipgloss.Style
	mnLabelFocusSty  lipgloss.Style
	mnValueStyle     lipgloss.Style
	mnValueFocusSty  lipgloss.Style
	mnNoteStyle      lipgloss.Style
	mnWarnStyle      lipgloss.Style
	mnErrStyle       lipgloss.Style
	mnCreateStyle    lipgloss.Style
	mnSelectedBG     color.Color
	mnHeadingStyle   lipgloss.Style
	mnSessionDimSty  lipgloss.Style
	mnSessionTextSty lipgloss.Style
)

// initMenuStyles initialises all menu-specific styles from the active theme.
func initMenuStyles() {
	t := theme.Current()
	fg := func(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }

	menuTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Align(lipgloss.Center)
	menuHintKeyStyle = fg(t.Accent).Bold(true)
	menuHintLabelStyle = fg(t.Muted)
	menuHintDotStyle = fg(t.Surface)
	cursorActiveStyle = fg(t.Accent).Bold(true)

	mnSectionStyle = fg(t.Sky).Bold(true)
	mnRuleStyle = fg(t.Surface)
	mnNameStyle = fg(t.Text)
	mnNameSelStyle = fg(t.Accent).Bold(true)
	mnDescStyle = fg(t.Muted)
	mnBadgeStyle = fg(t.Info)
	mnRunningStyle = fg(t.Green)
	mnStoppedStyle = fg(t.Overlay)
	mnLabelStyle = fg(t.Muted)
	mnLabelFocusSty = fg(t.Accent).Bold(true)
	mnValueStyle = fg(t.Text)
	mnValueFocusSty = fg(t.Bright).Bold(true)
	mnNoteStyle = fg(t.Subtle).Italic(true)
	mnWarnStyle = fg(t.Yellow)
	mnErrStyle = fg(t.Red)
	mnCreateStyle = fg(t.Subtle).Italic(true)
	mnSelectedBG = lipgloss.Color(t.Surface)
	mnHeadingStyle = fg(t.Bright).Bold(true)
	mnSessionDimSty = fg(t.Overlay).Italic(true)
	mnSessionTextSty = fg(t.Text)
}

// menuFocus is the pane that receives keys.
type menuFocus int

const (
	menuFocusList menuFocus = iota
	menuFocusOpts
	menuFocusSessions
)

// optField is a row of the options panel.
type optField int

const (
	fBackend optField = iota
	fModel
	fEffort
	fDir
	fWorktree
	numFields
)

// editMode says which free-text input is active.
type editMode int

const (
	editNone editMode = iota
	editModel
	editDir
)

// ollamaState is the live model list of one Ollama backend.
type ollamaState struct {
	done   bool
	models []string
	err    error
}

// sessionState is the lazily loaded session list of one agent.
type sessionState struct {
	done bool
	list []MenuSession
}

type ollamaMsg struct {
	backend string
	models  []string
	err     error
}

type sessionsMsg struct {
	agent string
	list  []MenuSession
}

// MenuModel is the Bubble Tea model for the one-screen launch menu.
type MenuModel struct {
	cfg     MenuConfig
	agents  []MenuAgent
	visible []int // indexes into agents that pass the filter, in order
	cursor  int   // index into visible; len(visible) is the create row

	filter    textinput.Model
	filtering bool

	focus      menuFocus
	field      optField
	opts       agentOpts
	edit       textinput.Model
	editing    editMode
	errMsg     string
	sessCursor int

	ollama   map[string]*ollamaState
	sessions map[string]*sessionState

	width, height int
	result        MenuResult
}

// NewMenuModel builds the launch menu.
func NewMenuModel(cfg MenuConfig) MenuModel {
	agents := append([]MenuAgent(nil), cfg.Agents...)
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].Section < agents[j].Section })
	cfg.Agents = agents

	fi := textinput.New()
	fi.CharLimit = 60
	fi.Prompt = ""
	ei := textinput.New()
	ei.CharLimit = 200
	ei.Prompt = ""

	m := MenuModel{
		cfg:      cfg,
		agents:   agents,
		filter:   fi,
		edit:     ei,
		ollama:   map[string]*ollamaState{},
		sessions: map[string]*sessionState{},
		width:    MenuPopupWidth - 2,
		height:   MenuPopupHeight - 2,
	}
	m.applyFilter()
	return m
}

// Result returns the popup output after it quits.
func (m MenuModel) Result() MenuResult { return m.result }

// PopupSize returns the tmux display-popup dimensions for the launch menu.
func (m MenuModel) PopupSize() PopupSize {
	return PopupSize{Width: MenuPopupWidth, Height: MenuPopupHeight}
}

// Init starts the lazy loads for the initially highlighted agent.
func (m MenuModel) Init() tea.Cmd { return m.fetchCmds() }

// current returns the highlighted agent; false on the create row.
func (m MenuModel) current() (MenuAgent, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return MenuAgent{}, false
	}
	return m.agents[m.visible[m.cursor]], true
}

// applyFilter recomputes the visible agents from the filter text, keeping the
// highlighted agent when it still matches.
func (m *MenuModel) applyFilter() {
	prev, hadPrev := m.current()
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	m.visible = m.visible[:0:0]
	for i, a := range m.agents {
		if q == "" || matchesFilter(a, q) {
			m.visible = append(m.visible, i)
		}
	}
	m.cursor = 0
	if hadPrev {
		for i, idx := range m.visible {
			if m.agents[idx].Name == prev.Name {
				m.cursor = i
				break
			}
		}
	}
	m.syncOpts()
}

// matchesFilter reports whether a contains the lowercase query in its name,
// description or backend.
func matchesFilter(a MenuAgent, q string) bool {
	for _, s := range []string{a.Name, a.Description, a.Backend, a.Model} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// syncOpts rebuilds the options when the highlighted agent changed.
func (m *MenuModel) syncOpts() {
	a, ok := m.current()
	if !ok {
		m.opts = agentOpts{}
		m.sessCursor = 0
		return
	}
	if m.opts.agent != a.Name {
		m.opts = m.newAgentOpts(a)
		m.sessCursor = 0
		m.errMsg = ""
	}
}

// fetchCmds returns the loads the current highlight needs and has not started.
func (m MenuModel) fetchCmds() tea.Cmd {
	a, ok := m.current()
	if !ok {
		return nil
	}
	var cmds []tea.Cmd
	if m.sessions[a.Name] == nil && m.cfg.LoadSessions != nil {
		m.sessions[a.Name] = &sessionState{}
		load, name, now := m.cfg.LoadSessions, a.Name, m.now()
		cmds = append(cmds, func() tea.Msg {
			return sessionsMsg{agent: name, list: NormalizeSessions(load(name), now, sessionMaxAge, sessionLimit)}
		})
	}
	be := m.opts.backend
	if _, known := m.cfg.Backends[be]; known && !m.isNous(be) && m.ollama[be] == nil && m.cfg.FetchOllama != nil {
		m.ollama[be] = &ollamaState{}
		fetch := m.cfg.FetchOllama
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), ollamaFetchTimeout)
			defer cancel()
			models, err := fetch(ctx, be)
			return ollamaMsg{backend: be, models: models, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m MenuModel) now() time.Time {
	if m.cfg.Now != nil {
		return m.cfg.Now()
	}
	return time.Now()
}

// NormalizeSessions drops live and too-old sessions, sorts the rest by most
// recent activity and keeps at most limit.
func NormalizeSessions(in []MenuSession, now time.Time, maxAge time.Duration, limit int) []MenuSession {
	var out []MenuSession
	for _, s := range in {
		if s.Live || s.When.IsZero() || now.Sub(s.When) > maxAge {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// currentSessions returns the loaded sessions of the highlighted agent.
func (m MenuModel) currentSessions() []MenuSession {
	if a, ok := m.current(); ok {
		if st := m.sessions[a.Name]; st != nil {
			return st.list
		}
	}
	return nil
}

func (m MenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case ollamaMsg:
		m.ollama[msg.backend] = &ollamaState{done: true, models: msg.models, err: msg.err}
		return m, nil
	case sessionsMsg:
		m.sessions[msg.agent] = &sessionState{done: true, list: msg.list}
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	if m.filtering || m.editing != editNone {
		return m.updateInput(msg)
	}
	return m, nil
}

// updateInput forwards non-key messages (cursor blink) to the active input.
func (m MenuModel) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.editing != editNone {
		m.edit, cmd = m.edit.Update(msg)
	} else {
		m.filter, cmd = m.filter.Update(msg)
	}
	return m, cmd
}

func (m MenuModel) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m.cancel()
	}
	switch {
	case m.filtering:
		return m.keyFilter(msg)
	case m.editing != editNone:
		return m.keyEdit(msg)
	case m.focus == menuFocusSessions:
		return m.keySessions(key)
	case m.focus == menuFocusOpts:
		return m.keyOpts(key)
	}
	return m.keyList(key)
}

func (m MenuModel) cancel() (tea.Model, tea.Cmd) {
	m.result = MenuResult{Cancelled: true}
	return m, tea.Quit
}

func (m MenuModel) keyFilter(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.filtering = false
		m.filter.Blur()
		return m, m.fetchCmds()
	case "esc":
		m.filtering = false
		m.filter.Blur()
		m.filter.SetValue("")
		m.applyFilter()
		return m, m.fetchCmds()
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.applyFilter()
	return m, tea.Batch(cmd, m.fetchCmds())
}

func (m MenuModel) keyList(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m.cancel()
	case "esc":
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.applyFilter()
			return m, m.fetchCmds()
		}
		return m.cancel()
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.visible) {
			m.cursor++
		}
	case "/":
		m.filtering = true
		return m, m.filter.Focus()
	case "n":
		return m.create()
	case "tab":
		if _, ok := m.current(); ok {
			m.focus, m.field = menuFocusOpts, fBackend
		}
	case "shift+tab":
		if _, ok := m.current(); ok {
			m.focus, m.field = menuFocusOpts, fWorktree
		}
	case "r":
		if len(m.currentSessions()) > 0 {
			m.focus = menuFocusSessions
		}
	case "d":
		return m.startEdit(fDir)
	case "enter":
		return m.launch()
	}
	m.syncOpts()
	return m, m.fetchCmds()
}

func (m MenuModel) keyOpts(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.focus = menuFocusList
		m.errMsg = ""
	case "q":
		return m.cancel()
	case "tab", "down", "j":
		if m.field == fWorktree {
			m.focus = menuFocusList
		} else {
			m.field++
		}
	case "shift+tab", "up", "k":
		if m.field == fBackend {
			m.focus = menuFocusList
		} else {
			m.field--
		}
	case "right", "l":
		return m.cycle(1)
	case "left", "h":
		return m.cycle(-1)
	case "e":
		return m.startEdit(m.field)
	case "d":
		return m.startEdit(fDir)
	case "r":
		if len(m.currentSessions()) > 0 {
			m.focus = menuFocusSessions
		}
	case "enter":
		return m.launch()
	}
	return m, nil
}

func (m MenuModel) keySessions(key string) (tea.Model, tea.Cmd) {
	ss := m.currentSessions()
	switch key {
	case "esc":
		m.focus = menuFocusList
	case "q":
		return m.cancel()
	case "up", "k":
		if m.sessCursor > 0 {
			m.sessCursor--
		}
	case "down", "j":
		if m.sessCursor < len(ss)-1 {
			m.sessCursor++
		}
	case "enter":
		a, ok := m.current()
		if !ok || m.sessCursor >= len(ss) {
			return m, nil
		}
		m.result = MenuResult{Action: "resume", Agent: a.Name, Dir: expandHome(m.opts.dir), SessionID: ss[m.sessCursor].ID}
		return m, tea.Quit
	}
	return m, nil
}

// keyEdit handles the free-text model and dir inputs.
func (m MenuModel) keyEdit(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = editNone
		m.errMsg = ""
		m.edit.Blur()
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.edit.Value())
		switch m.editing {
		case editModel:
			if val != "" {
				if err := m.validateModel(m.opts.backend, val); err != nil {
					m.errMsg = err.Error()
					return m, nil
				}
				m.opts.model = val
				if !hasModel(m.modelChoices(), val) {
					m.opts.custom = val
				}
			}
		case editDir:
			if val != "" {
				m.opts.dir = expandHome(val)
			}
		}
		m.editing = editNone
		m.errMsg = ""
		m.edit.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.edit, cmd = m.edit.Update(msg)
	return m, cmd
}

func hasModel(cs []modelOpt, v string) bool {
	for _, c := range cs {
		if c.Value == v {
			return true
		}
	}
	return false
}

// startEdit opens the free-text input for the model or dir field.
func (m MenuModel) startEdit(f optField) (tea.Model, tea.Cmd) {
	if _, ok := m.current(); !ok {
		return m, nil
	}
	switch f {
	case fModel:
		m.editing = editModel
		m.edit.SetValue(m.opts.model)
	case fDir:
		m.editing = editDir
		m.edit.SetValue(m.opts.dir)
	default:
		return m, nil
	}
	m.focus, m.field = menuFocusOpts, f
	m.errMsg = ""
	return m, m.edit.Focus()
}

// cycle moves the focused option's value by delta.
func (m MenuModel) cycle(delta int) (tea.Model, tea.Cmd) {
	a, ok := m.current()
	if !ok {
		return m, nil
	}
	m.errMsg = ""
	switch m.field {
	case fBackend:
		i := indexOf(m.opts.backends, m.opts.backend)
		m.opts.backend = m.opts.backends[wrap(i+delta, len(m.opts.backends))]
		m.opts.custom = ""
		m.opts.model = m.defaultModel(a, m.opts.backend)
		return m, m.fetchCmds()
	case fModel:
		cs := m.modelChoices()
		if len(cs) > 0 {
			m.opts.model = cs[wrap(modelIndex(cs, m.opts.model)+delta, len(cs))].Value
		}
	case fEffort:
		es := m.effortOptions()
		m.opts.effort = es[wrap(indexOf(es, m.opts.effort)+delta, len(es))]
	case fWorktree:
		m.opts.worktree = !m.opts.worktree
	}
	return m, nil
}

func indexOf(xs []string, v string) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return 0
}

func wrap(i, n int) int { return ((i % n) + n) % n }

func (m MenuModel) create() (tea.Model, tea.Cmd) {
	m.result = MenuResult{Action: "create"}
	return m, tea.Quit
}

// launch validates the options and quits with a fresh-launch result.
func (m MenuModel) launch() (tea.Model, tea.Cmd) {
	a, ok := m.current()
	if !ok {
		return m.create()
	}
	if err := m.validate(a); err != nil {
		m.errMsg = err.Error()
		m.focus, m.field = menuFocusOpts, fModel
		return m, nil
	}
	m.result = MenuResult{
		Action:     "launch",
		Agent:      a.Name,
		Backend:    m.resultBackend(a),
		Model:      m.resultModel(a),
		Effort:     m.resultEffort(a),
		NoWorktree: !m.opts.worktree,
		Dir:        expandHome(m.opts.dir),
	}
	return m, tea.Quit
}

// shortenDir returns a display-friendly directory path, replacing $HOME with ~.
func shortenDir(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return dir
	}
	if strings.HasPrefix(dir, home) {
		return "~" + dir[len(home):]
	}
	return dir
}

// truncW truncates s to at most w display cells, ending in an ellipsis when
// cut. It measures terminal width, never bytes.
func truncW(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// padW pads s with spaces to exactly w display cells (truncating if longer).
func padW(s string, w int) string {
	s = truncW(s, w)
	if n := w - ansi.StringWidth(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}
