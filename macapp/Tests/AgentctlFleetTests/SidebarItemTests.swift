import XCTest

@testable import AgentctlFleet

final class SidebarItemTests: XCTestCase {

  private func agent(windowID: String, name: String, state: String = "working") -> AgentSnapshot {
    AgentSnapshot(name: name, windowID: windowID, state: state)
  }

  private func group(session: String, project: String, rows: [AgentSnapshot]) -> SessionGroup {
    SessionGroup(session: session, project: project, rollup: .working, rows: rows, stale: false)
  }

  // MARK: - Stable identity across snapshots

  func testGroupAndAgentIdentityIsStableAcrossReconcile() {
    let tree1 = SidebarTree().reconciled(
      with: FleetSnapshot(sessions: [
        group(
          session: "agentctl-foo", project: "foo",
          rows: [agent(windowID: "@1", name: "go-engineer")])
      ]))

    let groupNode1 = tree1.groups[0]
    let agentNode1 = groupNode1.children[0]

    // A second reconcile against a snapshot with the same identities but
    // different content (rename + state change) must reuse the same node
    // instances, not allocate new ones.
    let tree2 = tree1.reconciled(
      with: FleetSnapshot(sessions: [
        group(
          session: "agentctl-foo", project: "foo",
          rows: [agent(windowID: "@1", name: "go-engineer-renamed", state: "blocked")])
      ]))

    XCTAssertTrue(
      tree2.groups[0] === groupNode1, "group node identity must survive an unrelated content change"
    )
    XCTAssertTrue(
      tree2.groups[0].children[0] === agentNode1,
      "agent node identity must survive a rename/state change")
    XCTAssertEqual(tree2.groups[0].children[0].agent?.name, "go-engineer-renamed")
    XCTAssertEqual(tree2.groups[0].children[0].agent?.state, "blocked")
  }

  func testNewGroupsAndAgentsGetNewNodes() {
    let tree1 = SidebarTree().reconciled(
      with: FleetSnapshot(sessions: [
        group(session: "agentctl-foo", project: "foo", rows: [agent(windowID: "@1", name: "a")])
      ]))

    let tree2 = tree1.reconciled(
      with: FleetSnapshot(sessions: [
        group(session: "agentctl-foo", project: "foo", rows: [agent(windowID: "@1", name: "a")]),
        group(session: "agentctl-bar", project: "bar", rows: [agent(windowID: "@2", name: "b")]),
      ]))

    XCTAssertEqual(tree2.groups.count, 2)
    XCTAssertTrue(tree2.groups[0] === tree1.groups[0])
    XCTAssertEqual(tree2.groups[1].group?.session, "agentctl-bar")
  }

  // MARK: - Selected-agent-disappears detection

  func testContainsIsTrueWhileAgentPresent() {
    let tree = SidebarTree().reconciled(
      with: FleetSnapshot(sessions: [
        group(session: "agentctl-foo", project: "foo", rows: [agent(windowID: "@1", name: "a")])
      ]))
    XCTAssertTrue(tree.contains(.agent(windowID: "@1")))
  }

  func testSelectedAgentDisappearingIsDetected() {
    let tree1 = SidebarTree().reconciled(
      with: FleetSnapshot(sessions: [
        group(session: "agentctl-foo", project: "foo", rows: [agent(windowID: "@1", name: "a")])
      ]))
    XCTAssertTrue(tree1.contains(.agent(windowID: "@1")))

    // The window closes — it drops out of the next snapshot entirely.
    let tree2 = tree1.reconciled(
      with: FleetSnapshot(sessions: [
        group(session: "agentctl-foo", project: "foo", rows: [])
      ]))
    XCTAssertFalse(tree2.contains(.agent(windowID: "@1")))
  }

  func testGroupDisappearingIsDetected() {
    let tree1 = SidebarTree().reconciled(
      with: FleetSnapshot(sessions: [
        group(session: "agentctl-foo", project: "foo", rows: [agent(windowID: "@1", name: "a")])
      ]))

    // The whole session ends (registry entry gone).
    let tree2 = tree1.reconciled(with: FleetSnapshot(sessions: []))
    XCTAssertFalse(tree2.contains(.group(session: "agentctl-foo")))
    XCTAssertFalse(tree2.contains(.agent(windowID: "@1")))
  }

  // MARK: - node(for:)

  func testNodeForFindsBothLevels() {
    let tree = SidebarTree().reconciled(
      with: FleetSnapshot(sessions: [
        group(session: "agentctl-foo", project: "foo", rows: [agent(windowID: "@1", name: "a")])
      ]))
    XCTAssertNotNil(tree.node(for: .group(session: "agentctl-foo")))
    XCTAssertNotNil(tree.node(for: .agent(windowID: "@1")))
    XCTAssertNil(tree.node(for: .agent(windowID: "@999")))
  }

  // MARK: - Glyphs (precedence mirrors FleetStore.rowSortRank/rollupOf)

  func testAgentGlyphPrecedence() {
    XCTAssertEqual(
      SidebarGlyphs.agentGlyph(for: agent(windowID: "@1", name: "a", state: "blocked")), "⏸")
    XCTAssertEqual(
      SidebarGlyphs.agentGlyph(
        for: AgentSnapshot(name: "a", windowID: "@1", state: "idle", done: true)), "✓")
    XCTAssertEqual(
      SidebarGlyphs.agentGlyph(for: agent(windowID: "@1", name: "a", state: "working")), "●")
    XCTAssertEqual(
      SidebarGlyphs.agentGlyph(for: agent(windowID: "@1", name: "a", state: "idle")), "○")
  }

  func testRollupGlyphs() {
    XCTAssertEqual(SidebarGlyphs.rollupGlyph(for: .blocked), "⏸")
    XCTAssertEqual(SidebarGlyphs.rollupGlyph(for: .working), "●")
    XCTAssertEqual(SidebarGlyphs.rollupGlyph(for: .done), "✓")
    XCTAssertEqual(SidebarGlyphs.rollupGlyph(for: .idle), "○")
  }
}
