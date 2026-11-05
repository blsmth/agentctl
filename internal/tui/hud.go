package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// maxLogEntries is the ring buffer size for log entries.
const maxLogEntries = 500

// maxLogLines is the cap for the Logs tab line buffer.
const maxLogLines = 1000

// hudTickInterval is the poll interval for state/log updates.
const hudTickInterval = 500 * time.Millisecond

// Tab index constants.
const (
	tabStream    = 0
	tabAgentLogs = 1
	tabSysLogs   = 2
)

// hudNumTabs is the number of HUD tabs.
const hudNumTabs = 3

// hudTickMsg is sent on each poll tick.
type hudTickMsg struct{}

// HudModel is the Bubble Tea model for the Agent HUD.
type HudModel struct {
	windowID   string
	agentName  string
	state      string // "working", "idle", "blocked", "done"
	logPath    string
	logOffset  int64
	logEntries []LogEntry
	scrollTop  int // line offset into streamLineBuffer (stream tab)
	width      int
	height     int
	startTime  time.Time
	done       bool

	// Stream interactivity state.
	streamCursor    int          // index into logEntries of selected entry (-1 = no selection)
	expandedEntries map[int]bool // set of logEntry indices that are expanded

	// Pre-rendered stream line buffer (rebuilt whenever entries or expansion changes).
	streamLineBuffer []string // flat list of rendered lines for the stream viewport
	streamEntryStart []int    // streamEntryStart[i] = first line index for logEntries[i]

	// Tab state.
	activeTab        int      // 0 = stream, 1 = agent logs, 2 = system logs
	streamAtBottom   bool     // whether Stream tab is pinned to bottom
	agentLogLines    []string // pre-rendered lines for Agent Logs tab
	agentLogScroll   int      // scroll position for Agent Logs tab
	agentLogOffset   int64    // read offset into the agent-specific log
	agentLogAtBottom bool     // whether Agent Logs tab is pinned to bottom

	// System log state.
	sysLogLines    []string // pre-rendered lines for System Logs tab
	sysLogScroll   int      // scroll position for System Logs tab
	sysLogAtBottom bool     // whether System Logs tab is pinned to bottom
	sysLogOffset   int64    // read offset into the global agentctl.log
	sysLogPath     string   // always the global agentctl.log

	// logPollTick counts consecutive poll ticks where @agentctl_log was
	// not yet set, used to trigger a filesystem-scan fallback.
	logPollTick int
	// agentctlLogPath caches the resolved agentctl log path (session-specific
	// or global fallback). Empty until resolved.
	agentctlLogPath string
	// agentctlSessionID holds the @agentctl_session_id value read from the
	// tmux window option (set by hook-session-start). Empty for pre-hook windows.
	agentctlSessionID string
	// agentctlLogIsGlobal is true when agentctlLogPath is the shared global
	// agentctl.log rather than a per-session file.
	agentctlLogIsGlobal bool
	// agentctlLogErr holds the last error from resolving the agentctl log path.
	agentctlLogErr string
}

// NewHudModel creates a new HUD model targeting the given tmux window ID.
func NewHudModel(windowID string) HudModel {
	return HudModel{
		windowID:         windowID,
		startTime:        time.Now(),
		width:            80,
		height:           24,
		streamAtBottom:   true,
		agentLogAtBottom: true,
		sysLogAtBottom:   true,
		streamCursor:     -1,
		expandedEntries:  make(map[int]bool),
	}
}

// Init starts the first tick.
func (m HudModel) Init() tea.Cmd {
	return hudTick()
}

// hudTick schedules the next poll tick.
func hudTick() tea.Cmd {
	return tea.Tick(hudTickInterval, func(t time.Time) tea.Msg {
		return hudTickMsg{}
	})
}

// Update handles Bubble Tea messages.
func (m HudModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.rebuildStreamLineBuffer()
		m.clampScroll()
		m.clampAgentLogScroll()
		m.clampSysLogScroll()
		return m, nil

	case tea.KeyPressMsg:
		// Global quit — handled before any tab-specific delegation.
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

		// Tab switching is universal: always handled regardless of active tab.
		switch msg.String() {
		case "tab", "shift+tab", "1", "2", "3":
			switch msg.String() {
			case "1":
				m.activeTab = tabStream
			case "2":
				m.activeTab = tabAgentLogs
			case "3":
				m.activeTab = tabSysLogs
			case "shift+tab":
				m.activeTab = (m.activeTab - 1 + hudNumTabs) % hudNumTabs
			default: // tab — cycle forward
				m.activeTab = (m.activeTab + 1) % hudNumTabs
			}
			return m, nil
		}

		// Key handling for Stream / Agent Logs / System Logs tabs.
		switch msg.String() {
		case "esc":
			if m.activeTab == tabStream && m.streamCursor != -1 {
				// Deactivate cursor (collapse any expansion too).
				m.streamCursor = -1
				m.expandedEntries = make(map[int]bool)
				m.rebuildStreamLineBuffer()
			} else {
				return m, tea.Quit
			}

		case "j", "down":
			switch m.activeTab {
			case tabAgentLogs:
				m.agentLogScroll += 3
				m.clampAgentLogScroll()
				m.agentLogAtBottom = m.isAgentLogAtBottom()
			case tabSysLogs:
				m.sysLogScroll += 3
				m.clampSysLogScroll()
				m.sysLogAtBottom = m.isSysLogAtBottom()
			default: // tabStream
				if m.streamCursor == -1 {
					// Activate cursor at last visible entry.
					m.streamCursor = m.lastVisibleEntry()
					if m.streamCursor < 0 && len(m.logEntries) > 0 {
						m.streamCursor = len(m.logEntries) - 1
					}
				} else if m.streamCursor < len(m.logEntries)-1 {
					m.streamCursor++
				}
				m.rebuildStreamLineBuffer()
				m.ensureCursorVisible()
				m.streamAtBottom = m.isStreamAtBottom()
			}

		case "k", "up":
			switch m.activeTab {
			case tabAgentLogs:
				m.agentLogScroll -= 3
				if m.agentLogScroll < 0 {
					m.agentLogScroll = 0
				}
				m.agentLogAtBottom = false
			case tabSysLogs:
				m.sysLogScroll -= 3
				if m.sysLogScroll < 0 {
					m.sysLogScroll = 0
				}
				m.sysLogAtBottom = false
			default: // tabStream
				if m.streamCursor > 0 {
					m.streamCursor--
					m.rebuildStreamLineBuffer()
					m.ensureCursorVisible()
					m.streamAtBottom = false
				} else if m.streamCursor == -1 {
					// No cursor — scroll up by 3 lines.
					m.scrollTop -= 3
					if m.scrollTop < 0 {
						m.scrollTop = 0
					}
					m.streamAtBottom = false
				}
			}

		case "enter", " ":
			if m.activeTab == tabStream && m.streamCursor != -1 {
				if m.expandedEntries[m.streamCursor] {
					delete(m.expandedEntries, m.streamCursor)
				} else {
					m.expandedEntries[m.streamCursor] = true
				}
				m.rebuildStreamLineBuffer()
				m.ensureCursorVisible()
			}

		case "l", "right":
			if m.activeTab == tabStream && m.streamCursor != -1 {
				if !m.expandedEntries[m.streamCursor] {
					m.expandedEntries[m.streamCursor] = true
					m.rebuildStreamLineBuffer()
					m.ensureCursorVisible()
				}
			}

		case "h", "left":
			if m.activeTab == tabStream && m.streamCursor != -1 {
				if m.expandedEntries[m.streamCursor] {
					delete(m.expandedEntries, m.streamCursor)
					m.rebuildStreamLineBuffer()
					m.ensureCursorVisible()
				} else {
					// Collapse deactivates cursor.
					m.streamCursor = -1
					m.rebuildStreamLineBuffer()
				}
			}

		case "g":
			switch m.activeTab {
			case tabAgentLogs:
				m.agentLogScroll = 0
				m.agentLogAtBottom = false
			case tabSysLogs:
				m.sysLogScroll = 0
				m.sysLogAtBottom = false
			default:
				m.scrollTop = 0
				m.streamCursor = -1
				m.expandedEntries = make(map[int]bool)
				m.rebuildStreamLineBuffer()
			}

		case "G":
			switch m.activeTab {
			case tabAgentLogs:
				m.scrollAgentLogToBottom()
				m.agentLogAtBottom = true
			case tabSysLogs:
				m.scrollSysLogToBottom()
				m.sysLogAtBottom = true
			default:
				m.scrollToBottom()
				m.streamAtBottom = true
			}

		case "ctrl+d":
			switch m.activeTab {
			case tabAgentLogs:
				m.agentLogScroll += m.visibleLines() / 2
				m.clampAgentLogScroll()
				m.agentLogAtBottom = m.isAgentLogAtBottom()
			case tabSysLogs:
				m.sysLogScroll += m.visibleLines() / 2
				m.clampSysLogScroll()
				m.sysLogAtBottom = m.isSysLogAtBottom()
			default:
				m.scrollTop += m.visibleLines() / 2
				m.clampScroll()
				m.streamAtBottom = m.isStreamAtBottom()
			}

		case "ctrl+u":
			switch m.activeTab {
			case tabAgentLogs:
				m.agentLogScroll -= m.visibleLines() / 2
				if m.agentLogScroll < 0 {
					m.agentLogScroll = 0
				}
				m.agentLogAtBottom = false
			case tabSysLogs:
				m.sysLogScroll -= m.visibleLines() / 2
				if m.sysLogScroll < 0 {
					m.sysLogScroll = 0
				}
				m.sysLogAtBottom = false
			default:
				m.scrollTop -= m.visibleLines() / 2
				if m.scrollTop < 0 {
					m.scrollTop = 0
				}
				m.streamAtBottom = false
			}
		}
		return m, nil

	case hudTickMsg:
		m.poll()
		return m, hudTick()
	}

	return m, nil
}

// poll reads fresh state from windowstate files and tails the log file.
func (m *HudModel) poll() {
	// Read window state.
	if m.windowID != "" {
		ws, _ := windowstate.Read(m.windowID)
		m.state = ws.State
		if m.state == "" {
			m.state = "idle"
		}
		if ws.Done {
			m.done = true
		}

		// Resolve agent name from window on first poll.
		if m.agentName == "" {
			m.agentName = tmux.ResolveAgentByWindowID(m.windowID)
		}
		// Always retry log path — late-bound windows (predating hook-session-start
		// or where the hook didn't fire) will have it set by the monitor asynchronously,
		// so we must keep checking.
		if m.logPath == "" && ws.LogPath != "" {
			m.logPath = ws.LogPath
			m.logPollTick = 0
		}
		if m.logPath == "" {
			m.logPollTick++
			// After ~5 seconds of no log path, fall back to a filesystem scan.
			if m.logPollTick >= 10 && m.agentName != "" {
				if ss, err := sessions.FindSessionsForAgent(m.agentName, 1); err == nil && len(ss) > 0 {
					m.logPath = ss[0].LogFile
				}
			}
		}
	}

	// Tail the log file.
	if m.logPath != "" {
		result, err := TailLog(m.logPath, m.logOffset)
		if err == nil {
			m.logOffset = result.NewOffset
			m.logEntries = append(m.logEntries, result.Entries...)
			// Trim ring buffer.
			if len(m.logEntries) > maxLogEntries {
				trim := len(m.logEntries) - maxLogEntries
				m.logEntries = m.logEntries[trim:]
				// Adjust cursor index.
				if m.streamCursor >= 0 {
					m.streamCursor -= trim
					if m.streamCursor < 0 {
						m.streamCursor = 0
					}
				}
				// Adjust expandedEntries map (shift keys by trim).
				newExpanded := make(map[int]bool)
				for k, v := range m.expandedEntries {
					if k >= trim {
						newExpanded[k-trim] = v
					}
				}
				m.expandedEntries = newExpanded
			}
			// Rebuild line buffer after entries changed.
			m.rebuildStreamLineBuffer()
			// Auto-scroll to bottom only when pinned to bottom.
			if len(result.Entries) > 0 {
				if m.streamAtBottom {
					m.scrollToBottom()
					// Keep cursor pinned to last entry when at bottom.
					if m.streamCursor != -1 && len(m.logEntries) > 0 {
						m.streamCursor = len(m.logEntries) - 1
						m.rebuildStreamLineBuffer()
					}
				} else {
					m.clampScroll()
				}
			}
		}
	}

	// Always resolve the global system log path (instance-aware).
	if m.sysLogPath == "" {
		if p, err := agentctlLogPath(); err == nil {
			m.sysLogPath = p
		}
	}

	// Resolve the agent-specific log path once (cached in agentctlLogPath).
	// Prefer the session-specific log (.agentctl/logs/sessions/<id>.log)
	// derived from @agentctl_session_id; fall back to the global agentctl.log
	// once the agent name is known (indicating hooks either ran or won't run).
	if m.agentctlLogPath == "" {
		// Try to read the session ID pinned by hook-session-start via windowstate.
		if m.windowID != "" && m.agentctlSessionID == "" {
			ws, _ := windowstate.Read(m.windowID)
			if ws.SessionID != "" {
				m.agentctlSessionID = ws.SessionID
			}
		}
		if m.agentctlSessionID != "" {
			// Session-specific log path.
			sessionLogDir := paths.SessionLogDir()
			if sessionLogDir == "" {
				m.agentctlLogErr = "paths not initialized"
			} else {
				m.agentctlLogPath = filepath.Join(sessionLogDir, m.agentctlSessionID+".log")
				m.agentctlLogIsGlobal = false
				m.agentctlLogErr = ""
			}
		} else if m.agentName != "" {
			// agentName is resolved but no session ID — pre-hook window.
			// Fall back to the global (instance-aware) agentctl.log.
			p, err := agentctlLogPath()
			if err != nil {
				m.agentctlLogErr = "could not resolve log path: " + err.Error()
			} else {
				m.agentctlLogPath = p
				m.agentctlLogIsGlobal = true
				m.agentctlLogErr = ""
			}
		}
	}

	// Tail the agent-specific log for the Agent Logs tab.
	// Only tail if the path is resolved AND it is NOT the global fallback.
	if m.agentctlLogPath != "" && !m.agentctlLogIsGlobal {
		newLines, newOffset, _ := TailAgentctlLog(m.agentctlLogPath, m.agentLogOffset)
		if len(newLines) > 0 {
			m.agentLogOffset = newOffset
			m.agentLogLines = append(m.agentLogLines, newLines...)
			// Cap the buffer.
			if len(m.agentLogLines) > maxLogLines {
				trim := len(m.agentLogLines) - maxLogLines
				m.agentLogLines = m.agentLogLines[trim:]
				if m.agentLogScroll >= trim {
					m.agentLogScroll -= trim
				} else {
					m.agentLogScroll = 0
				}
			}
			// Auto-scroll if pinned to bottom.
			if m.agentLogAtBottom {
				m.scrollAgentLogToBottom()
			} else {
				m.clampAgentLogScroll()
			}
		}
	}

	// Tail the global system log for the System Logs tab.
	if m.sysLogPath != "" {
		newLines, newOffset, _ := TailAgentctlLog(m.sysLogPath, m.sysLogOffset)
		if len(newLines) > 0 {
			m.sysLogOffset = newOffset
			m.sysLogLines = append(m.sysLogLines, newLines...)
			// Cap the buffer.
			if len(m.sysLogLines) > maxLogLines {
				trim := len(m.sysLogLines) - maxLogLines
				m.sysLogLines = m.sysLogLines[trim:]
				if m.sysLogScroll >= trim {
					m.sysLogScroll -= trim
				} else {
					m.sysLogScroll = 0
				}
			}
			// Auto-scroll if pinned to bottom.
			if m.sysLogAtBottom {
				m.scrollSysLogToBottom()
			} else {
				m.clampSysLogScroll()
			}
		}
	}
}

// visibleLines returns the number of log lines that fit in the viewport.
func (m *HudModel) visibleLines() int {
	// Header: 1, tab bar: 1, footer: 1 (hints only) + 2 extra = 5 chrome lines.
	lines := m.height - 5
	if lines < 1 {
		lines = 1
	}
	return lines
}

// rebuildStreamLineBuffer re-renders the full stream line buffer from logEntries,
// accounting for cursor selection and expansion state.
func (m *HudModel) rebuildStreamLineBuffer() {
	w := m.width
	if w < 20 {
		w = 20
	}
	logWidth := w - 2
	if logWidth < 10 {
		logWidth = 10
	}

	m.streamLineBuffer = m.streamLineBuffer[:0]
	m.streamEntryStart = make([]int, len(m.logEntries))

	for i, e := range m.logEntries {
		m.streamEntryStart[i] = len(m.streamLineBuffer)
		selected := m.streamCursor == i
		line := m.renderEntry(e, logWidth, selected)
		m.streamLineBuffer = append(m.streamLineBuffer, " "+line)
		if m.expandedEntries[i] {
			detailLines := renderExpandedDetail(e, logWidth)
			for _, dl := range detailLines {
				m.streamLineBuffer = append(m.streamLineBuffer, " "+dl)
			}
		}
	}
}

// lastVisibleEntry returns the index of the last logEntry whose first line
// is within the current viewport (scrollTop .. scrollTop+visible-1).
func (m *HudModel) lastVisibleEntry() int {
	if len(m.logEntries) == 0 {
		return -1
	}
	bottomLine := m.scrollTop + m.visibleLines() - 1
	last := 0
	for i, startLine := range m.streamEntryStart {
		if startLine <= bottomLine {
			last = i
		}
	}
	return last
}

// ensureCursorVisible scrolls the viewport so the cursor entry is visible.
func (m *HudModel) ensureCursorVisible() {
	if m.streamCursor < 0 || m.streamCursor >= len(m.streamEntryStart) {
		return
	}
	cursorLine := m.streamEntryStart[m.streamCursor]
	visible := m.visibleLines()
	if cursorLine < m.scrollTop {
		m.scrollTop = cursorLine
	}
	if cursorLine >= m.scrollTop+visible {
		m.scrollTop = cursorLine - visible + 1
	}
	// Clamp.
	totalLines := len(m.streamLineBuffer)
	max := totalLines - visible
	if max < 0 {
		max = 0
	}
	if m.scrollTop > max {
		m.scrollTop = max
	}
	if m.scrollTop < 0 {
		m.scrollTop = 0
	}
}

// clampScroll ensures scrollTop stays within valid bounds (line-based).
func (m *HudModel) clampScroll() {
	max := len(m.streamLineBuffer) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	if m.scrollTop > max {
		m.scrollTop = max
	}
	if m.scrollTop < 0 {
		m.scrollTop = 0
	}
}

// scrollToBottom pins the scroll to the last line.
func (m *HudModel) scrollToBottom() {
	max := len(m.streamLineBuffer) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	m.scrollTop = max
}

// isStreamAtBottom returns true if scrollTop is at the last possible position.
func (m *HudModel) isStreamAtBottom() bool {
	max := len(m.streamLineBuffer) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	return m.scrollTop >= max
}

// clampAgentLogScroll ensures agentLogScroll stays within valid bounds.
func (m *HudModel) clampAgentLogScroll() {
	max := len(m.agentLogLines) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	if m.agentLogScroll > max {
		m.agentLogScroll = max
	}
	if m.agentLogScroll < 0 {
		m.agentLogScroll = 0
	}
}

// isAgentLogAtBottom returns true if agentLogScroll is at the last possible position.
func (m *HudModel) isAgentLogAtBottom() bool {
	max := len(m.agentLogLines) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	return m.agentLogScroll >= max
}

// scrollAgentLogToBottom pins agentLogScroll to the last line of the Agent Logs tab.
func (m *HudModel) scrollAgentLogToBottom() {
	max := len(m.agentLogLines) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	m.agentLogScroll = max
}

// clampSysLogScroll ensures sysLogScroll stays within valid bounds.
func (m *HudModel) clampSysLogScroll() {
	max := len(m.sysLogLines) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	if m.sysLogScroll > max {
		m.sysLogScroll = max
	}
	if m.sysLogScroll < 0 {
		m.sysLogScroll = 0
	}
}

// isSysLogAtBottom returns true if sysLogScroll is at the last possible position.
func (m *HudModel) isSysLogAtBottom() bool {
	max := len(m.sysLogLines) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	return m.sysLogScroll >= max
}

// scrollSysLogToBottom pins sysLogScroll to the last line of the System Logs tab.
func (m *HudModel) scrollSysLogToBottom() {
	max := len(m.sysLogLines) - m.visibleLines()
	if max < 0 {
		max = 0
	}
	m.sysLogScroll = max
}

// View renders the complete HUD.
func (m HudModel) View() tea.View {
	w := m.width
	if w < 20 {
		w = 20
	}

	// Mini mode: only render footer when height is very small.
	if m.height <= 5 {
		v := tea.NewView(m.renderMiniFooter(w))
		v.AltScreen = true
		return v
	}

	var sb strings.Builder

	// ── Header bar ──────────────────────────────────────────────────────────
	sb.WriteString(m.renderHeader(w))
	sb.WriteByte('\n')

	// ── Tab bar ─────────────────────────────────────────────────────────────
	sb.WriteString(m.renderTabBar(w))
	sb.WriteByte('\n')

	// ── Viewport ─────────────────────────────────────────────────────────────
	visible := m.visibleLines()
	rendered := 0

	switch m.activeTab {
	case tabAgentLogs:
		// Agent Logs tab: render per-agent session log lines.
		if len(m.agentLogLines) == 0 {
			hint := m.emptyAgentLogsHint()
			sb.WriteString(hudEmptyStateStyle.Render(" " + hint))
			sb.WriteByte('\n')
			rendered++
		} else {
			end := m.agentLogScroll + visible
			if end > len(m.agentLogLines) {
				end = len(m.agentLogLines)
			}
			for i := m.agentLogScroll; i < end; i++ {
				sb.WriteString(m.agentLogLines[i])
				sb.WriteByte('\n')
				rendered++
			}
		}

	case tabSysLogs:
		// System Logs tab: render global agentctl.log lines.
		if len(m.sysLogLines) == 0 {
			hint := m.emptySysLogsHint()
			sb.WriteString(hudEmptyStateStyle.Render(" " + hint))
			sb.WriteByte('\n')
			rendered++
		} else {
			end := m.sysLogScroll + visible
			if end > len(m.sysLogLines) {
				end = len(m.sysLogLines)
			}
			for i := m.sysLogScroll; i < end; i++ {
				sb.WriteString(m.sysLogLines[i])
				sb.WriteByte('\n')
				rendered++
			}
		}

	default: // tabStream
		// Stream tab: render from pre-built line buffer.
		if len(m.logEntries) == 0 {
			hint := m.emptyStreamHint()
			sb.WriteString(hudEmptyStateStyle.Render(" " + hint))
			sb.WriteByte('\n')
			rendered++
		} else {
			end := m.scrollTop + visible
			if end > len(m.streamLineBuffer) {
				end = len(m.streamLineBuffer)
			}
			for i := m.scrollTop; i < end; i++ {
				sb.WriteString(m.streamLineBuffer[i])
				sb.WriteByte('\n')
				rendered++
			}
		}
	}

	// Pad remaining lines.
	for rendered < visible {
		sb.WriteByte('\n')
		rendered++
	}

	// ── Footer bar ──────────────────────────────────────────────────────────
	sb.WriteString(m.renderFooter(w))

	v := tea.NewView(sb.String())
	v.AltScreen = true
	return v
}

// renderHeader builds the top status bar.
func (m HudModel) renderHeader(w int) string {
	name := m.agentName
	if name == "" {
		name = m.windowID
	}

	stateBadge := m.stateLabel()

	// Build left portion: name + state badge.
	left := hudHeaderNameStyle.Render(" "+name+" ") +
		hudHeaderSepStyle.Render(" ─ ") +
		stateBadge

	// Fill to the right (metrics moved to footer).
	leftW := lipgloss.Width(left)
	fill := w - leftW
	if fill < 0 {
		fill = 0
	}
	return left + hudHeaderFillStyle.Render(strings.Repeat("─", fill))
}

// renderTabBar builds the tab bar line shown below the header.
func (m HudModel) renderTabBar(w int) string {
	var stream, agentLogs, sysLogs string
	switch m.activeTab {
	case tabStream:
		stream = hudTabActiveStyle.Render("[Stream]")
		agentLogs = hudTabInactiveStyle.Render(" Agent Logs ")
		sysLogs = hudTabInactiveStyle.Render(" System Logs ")
	case tabAgentLogs:
		stream = hudTabInactiveStyle.Render(" Stream ")
		agentLogs = hudTabActiveStyle.Render("[Agent Logs]")
		sysLogs = hudTabInactiveStyle.Render(" System Logs ")
	case tabSysLogs:
		stream = hudTabInactiveStyle.Render(" Stream ")
		agentLogs = hudTabInactiveStyle.Render(" Agent Logs ")
		sysLogs = hudTabActiveStyle.Render("[System Logs]")
	}
	tabs := " " + stream + " " + agentLogs + " " + sysLogs
	tabsW := lipgloss.Width(tabs)
	fill := w - tabsW
	if fill < 0 {
		fill = 0
	}
	return tabs + hudHeaderFillStyle.Render(strings.Repeat(" ", fill))
}

// stateLabel returns a styled state badge.
func (m HudModel) stateLabel() string {
	s := m.state
	if m.done {
		s = "done"
	}
	switch s {
	case "working":
		return hudStateWorkingStyle.Render(" ● Working ")
	case "blocked":
		return hudStateBlockedStyle.Render(" ⏸ Blocked ")
	case "done":
		return hudStateDoneStyle.Render(" ✓ Done ")
	default: // idle, empty
		return hudStateIdleStyle.Render(" ○ Idle ")
	}
}

// renderEntry formats a single log entry for display.
// selected = true renders the entry with a highlight and a hollow arrow for tool_calls.
func (m HudModel) renderEntry(e LogEntry, width int, selected bool) string {
	ts := hudTimeStyle.Render(e.Timestamp.Format("15:04:05"))

	summary := e.Summary
	if selected && e.Kind == "tool_call" {
		// Swap filled ▶ for hollow ▷ to indicate selection.
		summary = strings.Replace(summary, "▶ ", "▷ ", 1)
	}

	style := e.Style
	if selected {
		style = hudSelectedStyle
	}

	// Calculate available width for summary.
	tsW := lipgloss.Width(ts)
	available := width - tsW - 2 // 2 for spacing
	if available < 10 {
		available = 10
	}

	// Truncate summary if needed.
	plainSummary := summary
	if lipgloss.Width(plainSummary) > available {
		runes := []rune(plainSummary)
		for len(runes) > 0 && lipgloss.Width(string(runes)) > available-1 {
			runes = runes[:len(runes)-1]
		}
		plainSummary = string(runes) + "…"
	}
	rendered := style.Render(plainSummary)

	return ts + "  " + rendered
}

// renderExpandedDetail renders the detail block for an expanded log entry.
// Returns a slice of lines (without leading " " — caller adds that).
// Each line is pre-styled and ready to append to the stream line buffer.
func renderExpandedDetail(e LogEntry, width int) []string {
	// Indent aligns detail block with summary text (after "HH:MM:SS  ").
	const indent = "          " // 10 spaces
	const indentLen = 10

	// Box inner width = space available after indent, border char, and a space.
	boxInnerWidth := width - indentLen - 3 // "│ " (2) + 1 right margin
	if boxInnerWidth < 10 {
		boxInnerWidth = 10
	}

	var contentLines []string
	switch e.Kind {
	case "tool_call":
		if len(e.FullInput) == 0 {
			contentLines = []string{"(no input data)"}
		} else {
			contentLines = renderToolInputLines(e.ToolName, e.FullInput, boxInnerWidth)
		}
	default: // "text", "user", "system"
		text := e.FullText
		if text == "" {
			text = "(empty)"
		}
		contentLines = hudWrapText(text, boxInnerWidth)
	}

	// Cap at 20 content lines.
	const maxDetailLines = 20
	if len(contentLines) > maxDetailLines {
		contentLines = contentLines[:maxDetailLines]
		contentLines = append(contentLines, "… (truncated)")
	}

	borderH := strings.Repeat("─", boxInnerWidth+1)
	top := indent + hudDetailBorderStyle.Render("┌"+borderH)
	bottom := indent + hudDetailBorderStyle.Render("└"+borderH)

	lines := []string{top}
	for _, cl := range contentLines {
		lines = append(lines, indent+hudDetailBorderStyle.Render("│")+" "+cl)
	}
	lines = append(lines, bottom)
	return lines
}

// renderToolInputLines renders the FullInput map as key: value lines for a tool call.
func renderToolInputLines(toolName string, input map[string]any, width int) []string {
	if len(input) == 0 {
		return []string{"(no input data)"}
	}

	// Define display order per tool.
	priority := toolKeyPriority(toolName)
	rendered := make(map[string]bool)
	var lines []string

	for _, key := range priority {
		val, ok := input[key]
		if !ok {
			continue
		}
		rendered[key] = true
		lines = append(lines, renderInputKV(key, val, width)...)
	}

	// Remaining keys alphabetically.
	var rest []string
	for k := range input {
		if !rendered[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		lines = append(lines, renderInputKV(key, input[key], width)...)
	}

	return lines
}

// toolKeyPriority returns the preferred display order of input keys for a given tool.
func toolKeyPriority(toolName string) []string {
	switch toolName {
	case "Bash":
		return []string{"command", "description"}
	case "Edit":
		return []string{"file_path", "old_string", "new_string", "description"}
	case "Read":
		return []string{"file_path", "offset", "limit"}
	case "Write":
		return []string{"file_path", "content"}
	case "Grep":
		return []string{"pattern", "path", "glob", "type"}
	case "Glob":
		return []string{"pattern", "path"}
	case "Agent":
		return []string{"description", "prompt"}
	default:
		return nil
	}
}

// renderInputKV formats a single key/value pair, potentially spanning multiple lines.
func renderInputKV(key string, val any, width int) []string {
	strVal := fmt.Sprintf("%v", val)

	keyLabel := hudDetailKeyStyle.Render(key + ": ")

	switch key {
	case "file_path":
		abbrev := abbreviateHudPath(strVal)
		return []string{keyLabel + hudDetailValueStyle.Render(abbrev)}

	case "old_string", "new_string":
		return renderMultilineKV(key, strVal, width, 10)

	case "command":
		return renderMultilineKV(key, strVal, width, 20)

	case "description", "content", "prompt":
		return renderMultilineKV(key, strVal, width, 15)

	default:
		// Single-line value, truncate if needed.
		valueWidth := width - lipgloss.Width(keyLabel)
		if valueWidth < 5 {
			valueWidth = 5
		}
		v := strVal
		if len(v) > valueWidth {
			v = v[:valueWidth-1] + "…"
		}
		return []string{keyLabel + hudDetailValueStyle.Render(v)}
	}
}

// renderMultilineKV renders a key with a potentially multi-line string value.
// maxLines caps how many value lines are shown; extra lines show "… (N more lines)".
func renderMultilineKV(key, val string, width, maxLines int) []string {
	valueLines := strings.Split(val, "\n")
	total := len(valueLines)
	keyLabel := hudDetailKeyStyle.Render(key + ":")

	// Show line count in the key label if multi-line.
	if total > 1 {
		keyLabel = hudDetailKeyStyle.Render(fmt.Sprintf("%s: (%d lines)", key, total))
	}

	// Limit to maxLines.
	shown := valueLines
	truncated := 0
	if len(shown) > maxLines {
		truncated = len(shown) - maxLines
		shown = shown[:maxLines]
	}

	var lines []string
	lines = append(lines, keyLabel)
	for _, vl := range shown {
		// Indent value lines and truncate to width.
		prefix := "  "
		line := prefix + vl
		if len(line) > width {
			line = line[:width-1] + "…"
		}
		lines = append(lines, hudDetailValueStyle.Render(line))
	}
	if truncated > 0 {
		lines = append(lines, hudDetailValueStyle.Render(fmt.Sprintf("  … (%d more lines)", truncated)))
	}

	return lines
}

// hudWrapText wraps text to the given width, splitting on newlines first.
func hudWrapText(text string, width int) []string {
	if width <= 0 {
		width = 40
	}
	var result []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			result = append(result, "")
			continue
		}
		if len(paragraph) <= width {
			result = append(result, paragraph)
			continue
		}
		// Simple word wrap.
		words := strings.Fields(paragraph)
		line := ""
		for _, word := range words {
			if line == "" {
				line = word
			} else if len(line)+1+len(word) <= width {
				line += " " + word
			} else {
				result = append(result, line)
				line = word
			}
		}
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

// renderMiniFooter renders a compact view for when height <= 5.
// For height <= 2: show only agent name + state.
// For height >= 3: show agent name + state on line 1, keybind hints on line 2.
// Metrics are displayed by ChromeBarModel, not the HUD popup.
func (m HudModel) renderMiniFooter(w int) string {
	name := m.agentName
	if name == "" {
		name = m.windowID
	}
	left := hudHeaderNameStyle.Render(" "+name+" ") +
		hudHeaderSepStyle.Render(" ─ ") +
		m.stateLabel()
	leftW := lipgloss.Width(left)
	fill := w - leftW
	if fill < 0 {
		fill = 0
	}
	nameLine := left + hudHeaderFillStyle.Render(strings.Repeat("─", fill))

	if m.height <= 2 {
		return nameLine
	}

	// height >= 3: line 1 = name + state, line 2 = keybind hints, pad to height.
	hint := "Tab/Shift+Tab switch · j/k scroll · Ctrl+d/u page · g/G top/bottom · q quit "
	hintFillW := w - lipgloss.Width(hint)
	if hintFillW < 0 {
		hintFillW = 0
	}
	hintLine := hudFooterFillStyle.Render(strings.Repeat(" ", hintFillW)) +
		hudFooterHintStyle.Render(hint)

	var sb strings.Builder
	sb.WriteString(nameLine)
	sb.WriteByte('\n')
	sb.WriteString(hintLine)
	// Pad remaining lines (height - 2 lines already written).
	for i := 2; i < m.height; i++ {
		sb.WriteByte('\n')
	}
	return sb.String()
}

// renderFooter builds the single-line bottom bar: keybind hints only.
// Metrics have moved to the chrome bar (ChromeBarModel).
func (m HudModel) renderFooter(w int) string {
	var hint string
	if m.activeTab == tabStream && m.streamCursor != -1 {
		hint = "↑/↓ navigate · Enter expand · h collapse · Esc back · g/G top/bottom · q quit "
	} else {
		hint = "Tab/Shift+Tab switch · j/k scroll · Ctrl+d/u page · g/G top/bottom · q quit "
	}
	hintFillW := w - lipgloss.Width(hint)
	if hintFillW < 0 {
		hintFillW = 0
	}
	return hudFooterFillStyle.Render(strings.Repeat(" ", hintFillW)) +
		hudFooterHintStyle.Render(hint)
}

// emptyStreamHint returns a diagnostic message for the Stream tab when no
// log entries have been loaded yet.
func (m HudModel) emptyStreamHint() string {
	if m.logPath == "" {
		if m.logPollTick < 10 {
			return "No log linked yet — waiting for @agentctl_log…"
		}
		return "No session log found. Window may predate hook-session-start."
	}
	return "Waiting for activity… (" + m.logPath + ")"
}

// emptyAgentLogsHint returns a diagnostic message for the Agent Logs tab
// when no agent-specific log lines have been loaded yet.
func (m HudModel) emptyAgentLogsHint() string {
	if m.agentctlLogErr != "" {
		return "Log error: " + m.agentctlLogErr
	}
	if m.agentctlSessionID == "" {
		return "No agent-specific log — waiting for session start…"
	}
	if m.agentctlLogIsGlobal {
		return "No agent-specific log — session ID not set"
	}
	return "Waiting for activity… [session: " + m.agentctlSessionID + "]"
}

// emptySysLogsHint returns a diagnostic message for the System Logs tab
// when no global log lines have been loaded yet.
func (m HudModel) emptySysLogsHint() string {
	if m.sysLogPath == "" {
		return "System log path not yet resolved."
	}
	return "Waiting for activity… [global log]"
}

// ── HUD-specific styles ──────────────────────────────────────────────────────

var (
	hudHeaderNameStyle   lipgloss.Style
	hudHeaderSepStyle    lipgloss.Style
	hudHeaderFillStyle   lipgloss.Style
	hudStateWorkingStyle lipgloss.Style
	hudStateIdleStyle    lipgloss.Style
	hudStateBlockedStyle lipgloss.Style
	hudStateDoneStyle    lipgloss.Style
	hudFooterFillStyle   lipgloss.Style
	hudFooterHintStyle   lipgloss.Style
	hudTabActiveStyle    lipgloss.Style
	hudTabInactiveStyle  lipgloss.Style
	hudEmptyStateStyle   lipgloss.Style
	hudSelectedStyle     lipgloss.Style
	hudDetailBorderStyle lipgloss.Style
	hudDetailKeyStyle    lipgloss.Style
	hudDetailValueStyle  lipgloss.Style
)

// initHudStyles initialises HUD-specific styles from the active theme.
func initHudStyles() {
	t := theme.Current()

	hudHeaderNameStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Text))

	hudHeaderSepStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	hudHeaderFillStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))

	hudStateWorkingStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Green))

	hudStateIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	hudStateBlockedStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Yellow))

	hudStateDoneStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Accent))

	hudFooterFillStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Base))
	hudFooterHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	hudTabActiveStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Text))

	hudTabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	hudEmptyStateStyle = lipgloss.NewStyle().
		Italic(true).
		Foreground(lipgloss.Color(t.Muted))

	// Stream interactivity styles.
	hudSelectedStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Text))

	hudDetailBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))
	hudDetailKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Blue))
	hudDetailValueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
}
