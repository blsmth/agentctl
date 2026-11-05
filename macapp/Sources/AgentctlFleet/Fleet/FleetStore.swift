import Combine
import Foundation

/// One session's worth of agents, ordered and rolled up for display — mirrors
/// `herd.Group` (Go). `stale` means the session's `state.json` is missing,
/// unreadable, or older than `StateWatcher.snapshotStaleAfter`; `rows` keeps
/// the last-known-good rows in that case rather than going empty.
struct SessionGroup: Equatable, Sendable {
  var session: String
  var project: String
  var rollup: RollupStatus
  var rows: [AgentSnapshot]
  var stale: Bool
}

/// Group-level attention-priority rollup — mirrors `herd.RollupStatus` (Go):
/// Blocked > Working > Done > Idle. This is a *different* precedence than
/// the within-group row order (`FleetStore.rowSortRank`) — see
/// `FleetStore.rollupOf` for why.
enum RollupStatus: Int, Comparable, Sendable {
  case blocked = 0
  case working
  case done
  case idle

  static func < (lhs: RollupStatus, rhs: RollupStatus) -> Bool {
    lhs.rawValue < rhs.rawValue
  }
}

/// The whole fleet, ordered so sessions needing attention float to the top.
struct FleetSnapshot: Equatable, Sendable {
  var sessions: [SessionGroup] = []
}

/// Aggregates the registry and every live session's `state.json` into one
/// ordered `FleetSnapshot`, kept current via `RegistryWatcher` and one
/// `StateWatcher` per live session.
///
/// Never mutates any of the files it watches (invariant #1 in the spec):
/// this is a read-only view over the same on-disk state the herd TUI reads.
@MainActor
final class FleetStore: ObservableObject {
  @Published private(set) var snapshot = FleetSnapshot()

  private var registryWatcher: RegistryWatcher?
  private var stateWatchers: [String: StateWatcher] = [:]  // keyed by session name
  private var latestEntries: [String: RegistryEntry] = [:]
  // Last-known-good snapshot per session, keyed by session name.
  private var latestSnapshots: [String: StateSnapshot] = [:]

  /// Starts watching the registry (and, transitively, every live
  /// session's runtime directory).
  func start() {
    guard registryWatcher == nil else { return }
    let watcher = RegistryWatcher { [weak self] entries in
      Task { @MainActor in self?.handleRegistryUpdate(entries) }
    }
    watcher.start()
    registryWatcher = watcher
  }

  func stop() {
    registryWatcher?.stop()
    registryWatcher = nil
    for watcher in stateWatchers.values { watcher.stop() }
    stateWatchers.removeAll()
  }

  private func handleRegistryUpdate(_ entries: [RegistryEntry]) {
    let liveSessions = Set(entries.map(\.sessionName))
    latestEntries = Dictionary(uniqueKeysWithValues: entries.map { ($0.sessionName, $0) })

    // A session gone from the registry is genuinely gone (not just
    // momentarily wedged) — stop retrying it and drop its cached rows.
    let goneSessions = stateWatchers.keys.filter { !liveSessions.contains($0) }
    for session in goneSessions {
      stateWatchers[session]?.stop()
      stateWatchers.removeValue(forKey: session)
      latestSnapshots.removeValue(forKey: session)
    }

    for entry in entries where stateWatchers[entry.sessionName] == nil {
      let sessionName = entry.sessionName
      let runtimeDir = URL(fileURLWithPath: entry.runtimeDir, isDirectory: true)
      let watcher = StateWatcher(runtimeDir: runtimeDir) { [weak self] snap in
        Task { @MainActor in self?.handleStateUpdate(session: sessionName, snapshot: snap) }
      }
      watcher.start()
      stateWatchers[sessionName] = watcher
    }

    rebuild()
  }

  private func handleStateUpdate(session: String, snapshot: StateSnapshot?) {
    // A transient decode failure (nil) keeps the last-known-good
    // snapshot in place rather than blanking the session — see the
    // type doc on `SessionGroup.stale`.
    if let snapshot {
      latestSnapshots[session] = snapshot
    }
    rebuild()
  }

  private func rebuild() {
    var groups: [SessionGroup] = []
    for (session, entry) in latestEntries {
      let project = Self.projectDisplayName(entry)
      guard let snap = latestSnapshots[session] else {
        groups.append(
          SessionGroup(session: session, project: project, rollup: .idle, rows: [], stale: true)
        )
        continue
      }
      let rows = Self.sortRows(snap.agents)
      groups.append(
        SessionGroup(
          session: session,
          project: project,
          rollup: Self.rollupRows(rows),
          rows: rows,
          stale: snap.isStale(maxAge: StateWatcher.snapshotStaleAfter)
        )
      )
    }
    snapshot = FleetSnapshot(sessions: Self.orderGroups(groups))
  }

  /// A human-friendly project name for a registry entry — mirrors
  /// `projectDisplayName` (Go): the project directory's basename, falling
  /// back to the session name for an empty or bare-component path.
  nonisolated static func projectDisplayName(_ entry: RegistryEntry) -> String {
    let name = (entry.projectDir as NSString).lastPathComponent
    if name.isEmpty || name == "." || name == "/" {
      return entry.sessionName
    }
    return name
  }

  // MARK: - Pure ordering (mirrors internal/herd/herd.go — keep in sync)

  /// Within-group sort priority — mirrors `rowSortRank` (Go): blocked
  /// first, then done (unseen), then working, then idle. Lower sorts
  /// first.
  nonisolated static func rowSortRank(_ row: AgentSnapshot) -> Int {
    if row.state == "blocked" { return 0 }
    if row.done { return 1 }
    if row.state == "working" { return 2 }
    return 3
  }

  /// Orders rows within a group: blocked, done, working, idle — ties
  /// broken by `windowIndex` so the order matches tmux's display order
  /// and doesn't jitter between polls as states change. Mirrors
  /// `SortRows` (Go); stable with respect to the input order.
  nonisolated static func sortRows(_ rows: [AgentSnapshot]) -> [AgentSnapshot] {
    rows.enumerated()
      .sorted { lhs, rhs in
        let lRank = rowSortRank(lhs.element)
        let rRank = rowSortRank(rhs.element)
        if lRank != rRank { return lRank < rRank }
        if lhs.element.windowIndex != rhs.element.windowIndex {
          return lhs.element.windowIndex < rhs.element.windowIndex
        }
        return lhs.offset < rhs.offset  // stable tie-break
      }
      .map(\.element)
  }

  /// One row's contribution to the group-level rollup — mirrors
  /// `rollupOf` (Go). Note the precedence differs from `rowSortRank`:
  /// working beats done here, deliberately — see the design doc's "Group
  /// rollup" section.
  nonisolated static func rollupOf(_ row: AgentSnapshot) -> RollupStatus {
    if row.state == "blocked" { return .blocked }
    if row.state == "working" { return .working }
    if row.done { return .done }
    return .idle
  }

  /// Rolls a set of rows up to one group-level status — mirrors
  /// `RollupRows` (Go): blocked if any row is blocked, else working if
  /// any is working, else done if any is done (unseen), else idle. An
  /// empty slice rolls up to idle.
  nonisolated static func rollupRows(_ rows: [AgentSnapshot]) -> RollupStatus {
    var best = RollupStatus.idle
    for row in rows {
      let contribution = rollupOf(row)
      if contribution < best {
        best = contribution
        if best == .blocked { break }
      }
    }
    return best
  }

  /// Orders groups so the ones needing attention float to the top —
  /// mirrors `OrderGroups` (Go): by rollup (blocked > working > done >
  /// idle), then session name for stability when rollups tie.
  nonisolated static func orderGroups(_ groups: [SessionGroup]) -> [SessionGroup] {
    groups.sorted { lhs, rhs in
      if lhs.rollup != rhs.rollup { return lhs.rollup < rhs.rollup }
      return lhs.session < rhs.session
    }
  }
}
