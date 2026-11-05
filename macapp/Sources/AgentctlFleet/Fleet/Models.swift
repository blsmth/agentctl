import Foundation

/// Mirrors `registry.Entry` (Go): one live agentctl session, discovered from
/// `~/.config/agentctl/registry/<session-name>.json`.
///
/// Decoding is lenient — unknown fields are ignored (the default for
/// `Codable`) and every field defaults sensibly if absent, since the Go
/// writer's JSON shape can gain fields over time.
struct RegistryEntry: Codable, Equatable, Sendable {
  var sessionName: String
  var projectDir: String
  var runtimeDir: String
  var host: String
  var pid: Int
  var startedAt: Int64
  var lastSeen: Int64

  enum CodingKeys: String, CodingKey {
    case sessionName = "session_name"
    case projectDir = "project_dir"
    case runtimeDir = "runtime_dir"
    case host
    case pid
    case startedAt = "started_at"
    case lastSeen = "last_seen"
  }

  init(
    sessionName: String, projectDir: String = "", runtimeDir: String = "", host: String = "local",
    pid: Int = 0, startedAt: Int64 = 0, lastSeen: Int64 = 0
  ) {
    self.sessionName = sessionName
    self.projectDir = projectDir
    self.runtimeDir = runtimeDir
    self.host = host
    self.pid = pid
    self.startedAt = startedAt
    self.lastSeen = lastSeen
  }

  init(from decoder: Decoder) throws {
    let c = try decoder.container(keyedBy: CodingKeys.self)
    sessionName = try c.decodeIfPresent(String.self, forKey: .sessionName) ?? ""
    projectDir = try c.decodeIfPresent(String.self, forKey: .projectDir) ?? ""
    runtimeDir = try c.decodeIfPresent(String.self, forKey: .runtimeDir) ?? ""
    host = try c.decodeIfPresent(String.self, forKey: .host) ?? "local"
    pid = try c.decodeIfPresent(Int.self, forKey: .pid) ?? 0
    startedAt = try c.decodeIfPresent(Int64.self, forKey: .startedAt) ?? 0
    lastSeen = try c.decodeIfPresent(Int64.self, forKey: .lastSeen) ?? 0
  }
}

/// Mirrors `state.AgentState` (Go): one agent window's snapshot, a row inside
/// a project's `state.json`. Go writes almost every field with `omitempty`,
/// so absence is the norm, not an error — every field here defaults rather
/// than failing decode.
struct AgentSnapshot: Codable, Equatable, Sendable {
  var name: String
  var agentName: String
  var sessionID: String
  var workDir: String
  var windowID: String
  var windowIndex: Int
  var state: String
  var contextPct: Int
  var worktreePath: String
  var done: Bool
  var modelName: String
  var turnCount: Int
  var ticket: Int
  var headerPane: String
  var footerPane: String
  var lastToolDetail: String
  var lastText: String
  var mailboxPending: Int
  var mainPane: String

  enum CodingKeys: String, CodingKey {
    case name
    case agentName = "agent_name"
    case sessionID = "session_id"
    case workDir = "work_dir"
    case windowID = "window_id"
    case windowIndex = "window_index"
    case state
    case contextPct = "context_pct"
    case worktreePath = "worktree_path"
    case done
    case modelName = "model_name"
    case turnCount = "turn_count"
    case ticket
    case headerPane = "header_pane"
    case footerPane = "footer_pane"
    case lastToolDetail = "last_tool_detail"
    case lastText = "last_text"
    case mailboxPending = "mailbox_pending"
    case mainPane = "main_pane"
  }

  init(
    name: String = "", agentName: String = "", sessionID: String = "", workDir: String = "",
    windowID: String = "", windowIndex: Int = 0, state: String = "", contextPct: Int = 0,
    worktreePath: String = "", done: Bool = false, modelName: String = "", turnCount: Int = 0,
    ticket: Int = 0, headerPane: String = "", footerPane: String = "", lastToolDetail: String = "",
    lastText: String = "", mailboxPending: Int = 0, mainPane: String = ""
  ) {
    self.name = name
    self.agentName = agentName
    self.sessionID = sessionID
    self.workDir = workDir
    self.windowID = windowID
    self.windowIndex = windowIndex
    self.state = state
    self.contextPct = contextPct
    self.worktreePath = worktreePath
    self.done = done
    self.modelName = modelName
    self.turnCount = turnCount
    self.ticket = ticket
    self.headerPane = headerPane
    self.footerPane = footerPane
    self.lastToolDetail = lastToolDetail
    self.lastText = lastText
    self.mailboxPending = mailboxPending
    self.mainPane = mainPane
  }

  init(from decoder: Decoder) throws {
    let c = try decoder.container(keyedBy: CodingKeys.self)
    name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
    agentName = try c.decodeIfPresent(String.self, forKey: .agentName) ?? ""
    sessionID = try c.decodeIfPresent(String.self, forKey: .sessionID) ?? ""
    workDir = try c.decodeIfPresent(String.self, forKey: .workDir) ?? ""
    windowID = try c.decodeIfPresent(String.self, forKey: .windowID) ?? ""
    windowIndex = try c.decodeIfPresent(Int.self, forKey: .windowIndex) ?? 0
    state = try c.decodeIfPresent(String.self, forKey: .state) ?? ""
    contextPct = try c.decodeIfPresent(Int.self, forKey: .contextPct) ?? 0
    worktreePath = try c.decodeIfPresent(String.self, forKey: .worktreePath) ?? ""
    done = try c.decodeIfPresent(Bool.self, forKey: .done) ?? false
    modelName = try c.decodeIfPresent(String.self, forKey: .modelName) ?? ""
    turnCount = try c.decodeIfPresent(Int.self, forKey: .turnCount) ?? 0
    ticket = try c.decodeIfPresent(Int.self, forKey: .ticket) ?? 0
    headerPane = try c.decodeIfPresent(String.self, forKey: .headerPane) ?? ""
    footerPane = try c.decodeIfPresent(String.self, forKey: .footerPane) ?? ""
    lastToolDetail = try c.decodeIfPresent(String.self, forKey: .lastToolDetail) ?? ""
    lastText = try c.decodeIfPresent(String.self, forKey: .lastText) ?? ""
    mailboxPending = try c.decodeIfPresent(Int.self, forKey: .mailboxPending) ?? 0
    mainPane = try c.decodeIfPresent(String.self, forKey: .mainPane) ?? ""
  }
}

/// Mirrors `state.State` (Go): the full `state.json` snapshot written by one
/// project's monitor.
struct StateSnapshot: Codable, Equatable, Sendable {
  var agents: [AgentSnapshot]
  var timestamp: Date
  var activeWindowID: String

  enum CodingKeys: String, CodingKey {
    case agents
    case timestamp
    case activeWindowID = "active_window_id"
  }

  init(agents: [AgentSnapshot] = [], timestamp: Date = .distantPast, activeWindowID: String = "") {
    self.agents = agents
    self.timestamp = timestamp
    self.activeWindowID = activeWindowID
  }

  init(from decoder: Decoder) throws {
    let c = try decoder.container(keyedBy: CodingKeys.self)
    agents = try c.decodeIfPresent([AgentSnapshot].self, forKey: .agents) ?? []
    activeWindowID = try c.decodeIfPresent(String.self, forKey: .activeWindowID) ?? ""
    if let raw = try c.decodeIfPresent(String.self, forKey: .timestamp),
      let date = parseGoTimestamp(raw)
    {
      timestamp = date
    } else {
      timestamp = .distantPast
    }
  }

  /// Whether this snapshot is older than `maxAge` — mirrors
  /// `state.Snapshot.IsStale` (Go). A snapshot that could never be parsed
  /// (`timestamp == .distantPast`) is always stale.
  func isStale(maxAge: TimeInterval, now: Date = Date()) -> Bool {
    now.timeIntervalSince(timestamp) > maxAge
  }
}

/// Parses a Go `time.Time` JSON string (RFC3339 with up to nanosecond
/// fractional precision) into a `Date`. `ISO8601DateFormatter` only supports
/// up to millisecond precision, so a longer fractional part is truncated
/// before retrying — this loses sub-millisecond precision, which is
/// irrelevant for a 5-second staleness check.
func parseGoTimestamp(_ s: String) -> Date? {
  if let date = iso8601FractionalFormatter.date(from: s) {
    return date
  }
  if let date = iso8601BasicFormatter.date(from: s) {
    return date
  }
  guard let dotIndex = s.firstIndex(of: ".") else { return nil }
  var digitsEnd = s.index(after: dotIndex)
  while digitsEnd < s.endIndex, s[digitsEnd].isNumber {
    digitsEnd = s.index(after: digitsEnd)
  }
  let truncatedDigits = s[s.index(after: dotIndex)..<digitsEnd].prefix(3)
  let normalized = s.replacingCharacters(
    in: s.index(after: dotIndex)..<digitsEnd, with: truncatedDigits)
  return iso8601FractionalFormatter.date(from: normalized)
}

// ISO8601DateFormatter isn't marked Sendable, but it's safe to share
// across threads here: both formatters are configured once at first access
// and only ever used for read-only `.date(from:)` parsing afterward.
nonisolated(unsafe) private let iso8601FractionalFormatter: ISO8601DateFormatter = {
  let f = ISO8601DateFormatter()
  f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
  return f
}()

nonisolated(unsafe) private let iso8601BasicFormatter: ISO8601DateFormatter = {
  let f = ISO8601DateFormatter()
  f.formatOptions = [.withInternetDateTime]
  return f
}()

/// Mirrors the subset of `windowstate.WindowState` (Go) the fleet needs:
/// `.agentctl/windows/<windowID>.json`, read only to identify which pane in
/// a window's layout is the chrome bar (so the remaining pane can be
/// identified as the agent pane).
struct WindowHookState: Codable, Equatable, Sendable {
  var chromeBarPane: String

  enum CodingKeys: String, CodingKey {
    case chromeBarPane = "chrome_bar_pane"
  }

  init(chromeBarPane: String = "") {
    self.chromeBarPane = chromeBarPane
  }

  init(from decoder: Decoder) throws {
    let c = try decoder.container(keyedBy: CodingKeys.self)
    chromeBarPane = try c.decodeIfPresent(String.self, forKey: .chromeBarPane) ?? ""
  }
}

/// Identifies the agent's content pane within a window's layout.
///
/// `mainPane` (from `state.AgentState.main_pane`, written authoritatively by
/// the Go side's `tmux.findMainPane`) wins outright when it names a pane
/// that's actually present in `panes` -- no guessing needed. A stale
/// `mainPane` naming a pane that no longer exists (e.g. after a window was
/// torn down and rebuilt) must not win over the heuristic, so it's checked
/// against the parsed layout before being trusted.
///
/// Otherwise, fall back to the pre-existing heuristic: some windows have no
/// chrome pane at all (a single leaf layout); some have a chrome bar as a
/// second pane. Preference order: if there's only one pane, it's the agent
/// pane; otherwise prefer the pane that doesn't match `chromeBarPane`; if
/// that's unknown or doesn't match anything in the layout, fall back to the
/// first pane in visual order (agent content is conventionally pane index
/// 0).
func identifyAgentPane(panes: [PaneLayout], chromeBarPane: String?, mainPane: String? = nil)
  -> String?
{
  guard !panes.isEmpty else { return nil }
  if let mainPane, !mainPane.isEmpty, panes.contains(where: { $0.id == mainPane }) {
    return mainPane
  }
  if panes.count == 1 {
    return panes[0].id
  }
  if let chromeBarPane, !chromeBarPane.isEmpty {
    if let agent = panes.first(where: { $0.id != chromeBarPane }) {
      return agent.id
    }
  }
  return panes[0].id
}
