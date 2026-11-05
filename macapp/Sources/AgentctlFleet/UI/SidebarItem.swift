import Foundation

/// A sidebar row's stable identity — session name for a group, `windowID`
/// for an agent. `windowID` is stable for a window's lifetime; `name` is
/// not (a hook can rename it), so identity must never key off `name`.
enum SidebarNodeID: Hashable {
  case group(session: String)
  case agent(windowID: String)
}

/// One sidebar row. Reference-typed and reused across snapshot updates
/// (see `SidebarTree.reconciled(with:)`) specifically so `NSOutlineView` can
/// key its internal expanded-items/selection bookkeeping off the *same*
/// object instance for a logical row that persists between updates —
/// `state.json` republishes on every write, and a naive `reloadData()`
/// against freshly-allocated items would collapse every group and drop the
/// selection on each tick.
final class SidebarNode: NSObject {
  let id: SidebarNodeID
  private(set) var group: SessionGroup?
  private(set) var agent: AgentSnapshot?
  private(set) var children: [SidebarNode]

  init(group: SessionGroup, children: [SidebarNode]) {
    self.id = .group(session: group.session)
    self.group = group
    self.agent = nil
    self.children = children
    super.init()
  }

  init(agent: AgentSnapshot) {
    self.id = .agent(windowID: agent.windowID)
    self.group = nil
    self.agent = agent
    self.children = []
    super.init()
  }

  fileprivate func update(group: SessionGroup, children: [SidebarNode]) {
    self.group = group
    self.children = children
  }

  fileprivate func update(agent: AgentSnapshot) {
    self.agent = agent
  }
}

/// The sidebar's full row tree, rebuilt from a `FleetSnapshot` on every
/// update while reusing `SidebarNode` instances for rows whose identity is
/// unchanged — see `SidebarNode`'s doc comment for why that matters.
struct SidebarTree {
  private(set) var groups: [SidebarNode] = []

  /// Builds a new tree from `snapshot`, reusing this tree's node instances
  /// wherever the logical row is still present. `FleetSnapshot.sessions`
  /// and each `rows` array arrive pre-sorted (attention-first) — this
  /// preserves that order rather than re-deriving it.
  func reconciled(with snapshot: FleetSnapshot) -> SidebarTree {
    var existingGroups: [String: SidebarNode] = [:]
    for node in groups {
      if case .group(let session) = node.id { existingGroups[session] = node }
    }

    let newGroups: [SidebarNode] = snapshot.sessions.map { group in
      var existingAgents: [String: SidebarNode] = [:]
      if let existingGroupNode = existingGroups[group.session] {
        for child in existingGroupNode.children {
          if case .agent(let windowID) = child.id { existingAgents[windowID] = child }
        }
      }

      let children: [SidebarNode] = group.rows.map { agent in
        if let existing = existingAgents[agent.windowID] {
          existing.update(agent: agent)
          return existing
        }
        return SidebarNode(agent: agent)
      }

      if let existingGroupNode = existingGroups[group.session] {
        existingGroupNode.update(group: group, children: children)
        return existingGroupNode
      }
      return SidebarNode(group: group, children: children)
    }

    return SidebarTree(groups: newGroups)
  }

  /// The node with `id`, searching both levels — nil if it's absent from
  /// this tree (e.g. the window it names has closed).
  func node(for id: SidebarNodeID) -> SidebarNode? {
    for group in groups {
      if group.id == id { return group }
      for child in group.children where child.id == id { return child }
    }
    return nil
  }

  /// Whether a row with `id` is still present — used to detect "the
  /// selected agent disappeared" without the caller re-walking the tree.
  func contains(_ id: SidebarNodeID) -> Bool {
    node(for: id) != nil
  }
}

/// Row glyphs — mirrors the precedence already encoded in
/// `FleetStore.rowSortRank` / `rollupOf`; deliberately not re-derived here.
enum SidebarGlyphs {
  /// Working `●`, blocked `⏸`, done (unseen) `✓`, idle `○`.
  static func agentGlyph(for row: AgentSnapshot) -> String {
    if row.state == "blocked" { return "⏸" }
    if row.done { return "✓" }
    if row.state == "working" { return "●" }
    return "○"
  }

  /// Same glyph set, for a group's rolled-up `RollupStatus`.
  static func rollupGlyph(for rollup: RollupStatus) -> String {
    switch rollup {
    case .blocked: return "⏸"
    case .working: return "●"
    case .done: return "✓"
    case .idle: return "○"
    }
  }
}
