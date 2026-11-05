import XCTest

@testable import AgentctlFleet

final class ModelsTests: XCTestCase {

  // MARK: - RegistryEntry decoding

  func testRegistryEntryDecodesExactGoShape() throws {
    let json = """
      {
        "session_name": "agentctl-myproject",
        "project_dir": "/Users/alice/src/myproject",
        "runtime_dir": "/Users/alice/src/myproject/.agentctl",
        "host": "local",
        "pid": 4242,
        "started_at": 1750000000,
        "last_seen": 1750000030
      }
      """
    let entry = try JSONDecoder().decode(RegistryEntry.self, from: Data(json.utf8))
    XCTAssertEqual(entry.sessionName, "agentctl-myproject")
    XCTAssertEqual(entry.projectDir, "/Users/alice/src/myproject")
    XCTAssertEqual(entry.runtimeDir, "/Users/alice/src/myproject/.agentctl")
    XCTAssertEqual(entry.host, "local")
    XCTAssertEqual(entry.pid, 4242)
    XCTAssertEqual(entry.startedAt, 1_750_000_000)
    XCTAssertEqual(entry.lastSeen, 1_750_000_030)
  }

  func testRegistryEntryDecodeIsLenientToMissingFields() throws {
    let json = #"{"session_name": "agentctl-x"}"#
    let entry = try JSONDecoder().decode(RegistryEntry.self, from: Data(json.utf8))
    XCTAssertEqual(entry.sessionName, "agentctl-x")
    XCTAssertEqual(entry.projectDir, "")
    XCTAssertEqual(entry.pid, 0)
  }

  // MARK: - AgentSnapshot decoding

  func testAgentSnapshotDecodesGoOmitemptyShape() throws {
    // Go's omitempty means most fields are simply absent when zero —
    // this fixture only sets a few, matching a freshly-spawned agent.
    let json = """
      {"name": "go-engineer", "state": "working", "window_id": "@3"}
      """
    let agent = try JSONDecoder().decode(AgentSnapshot.self, from: Data(json.utf8))
    XCTAssertEqual(agent.name, "go-engineer")
    XCTAssertEqual(agent.state, "working")
    XCTAssertEqual(agent.windowID, "@3")
    XCTAssertEqual(agent.done, false)
    XCTAssertEqual(agent.contextPct, 0)
    XCTAssertEqual(agent.ticket, 0)
  }

  func testAgentSnapshotDecodesFullShape() throws {
    let json = """
      {
        "name": "go-engineer~2",
        "agent_name": "go-engineer",
        "window_id": "@7",
        "window_index": 3,
        "state": "blocked",
        "done": true,
        "context_pct": 42,
        "model_name": "claude-sonnet-4-20250514",
        "turn_count": 12,
        "ticket": 1012,
        "last_tool_detail": "Editing monitor.go",
        "last_text": "Working on the fix now.",
        "work_dir": "/repo",
        "worktree_path": "/repo/.worktrees/agent-go-engineer-issue-1012",
        "mailbox_pending": 2,
        "header_pane": "%5",
        "footer_pane": "%6",
        "main_pane": "%12"
      }
      """
    let agent = try JSONDecoder().decode(AgentSnapshot.self, from: Data(json.utf8))
    XCTAssertEqual(agent.name, "go-engineer~2")
    XCTAssertEqual(agent.agentName, "go-engineer")
    XCTAssertEqual(agent.windowIndex, 3)
    XCTAssertEqual(agent.state, "blocked")
    XCTAssertTrue(agent.done)
    XCTAssertEqual(agent.contextPct, 42)
    XCTAssertEqual(agent.ticket, 1012)
    XCTAssertEqual(agent.mailboxPending, 2)
    XCTAssertEqual(agent.headerPane, "%5")
    XCTAssertEqual(agent.footerPane, "%6")
    XCTAssertEqual(agent.mainPane, "%12")
  }

  func testAgentSnapshotDecodeIsLenientToMissingMainPane() throws {
    let json = #"{"name": "go-engineer", "state": "working"}"#
    let agent = try JSONDecoder().decode(AgentSnapshot.self, from: Data(json.utf8))
    XCTAssertEqual(agent.mainPane, "")
  }

  // MARK: - StateSnapshot decoding + timestamp parsing

  func testStateSnapshotDecodesRFC3339NanoTimestamp() throws {
    let json = """
      {
        "agents": [{"name": "a", "state": "idle"}],
        "timestamp": "2026-08-31T20:17:35.123456789Z",
        "active_window_id": "@1"
      }
      """
    let snapshot = try JSONDecoder().decode(StateSnapshot.self, from: Data(json.utf8))
    XCTAssertEqual(snapshot.agents.count, 1)
    XCTAssertEqual(snapshot.activeWindowID, "@1")
    XCTAssertNotEqual(snapshot.timestamp, .distantPast)
  }

  func testStateSnapshotMissingTimestampDefaultsToDistantPastAndIsStale() throws {
    let json = #"{"agents": []}"#
    let snapshot = try JSONDecoder().decode(StateSnapshot.self, from: Data(json.utf8))
    XCTAssertEqual(snapshot.timestamp, .distantPast)
    XCTAssertTrue(snapshot.isStale(maxAge: 5))
  }

  func testStateSnapshotFreshIsNotStale() throws {
    let snapshot = StateSnapshot(agents: [], timestamp: Date(), activeWindowID: "")
    XCTAssertFalse(snapshot.isStale(maxAge: 5))
  }

  func testStateSnapshotOldIsStale() throws {
    let snapshot = StateSnapshot(
      agents: [], timestamp: Date().addingTimeInterval(-10), activeWindowID: "")
    XCTAssertTrue(snapshot.isStale(maxAge: 5))
  }

  func testParseGoTimestampVariants() {
    XCTAssertNotNil(parseGoTimestamp("2026-08-31T20:17:35Z"))
    XCTAssertNotNil(parseGoTimestamp("2026-08-31T20:17:35.123Z"))
    XCTAssertNotNil(parseGoTimestamp("2026-08-31T20:17:35.123456789Z"))
    XCTAssertNotNil(parseGoTimestamp("2026-08-31T20:17:35.1Z"))
    XCTAssertNil(parseGoTimestamp("not a timestamp"))
    XCTAssertNil(parseGoTimestamp(""))
  }

  // MARK: - identifyAgentPane

  func testIdentifyAgentPaneSinglePaneNoChrome() {
    let panes = [PaneLayout(id: "%553", cols: 214, rows: 76, x: 0, y: 0)]
    XCTAssertEqual(identifyAgentPane(panes: panes, chromeBarPane: nil), "%553")
  }

  func testIdentifyAgentPanePrefersNonChromePane() {
    let panes = [
      PaneLayout(id: "%509", cols: 214, rows: 71, x: 0, y: 0),
      PaneLayout(id: "%510", cols: 214, rows: 4, x: 0, y: 72),
    ]
    XCTAssertEqual(identifyAgentPane(panes: panes, chromeBarPane: "%510"), "%509")
  }

  func testIdentifyAgentPaneFallsBackToFirstWhenChromeUnknown() {
    let panes = [
      PaneLayout(id: "%1", cols: 100, rows: 50, x: 0, y: 0),
      PaneLayout(id: "%2", cols: 100, rows: 4, x: 0, y: 50),
    ]
    XCTAssertEqual(identifyAgentPane(panes: panes, chromeBarPane: nil), "%1")
    XCTAssertEqual(identifyAgentPane(panes: panes, chromeBarPane: "%999"), "%1")
  }

  func testIdentifyAgentPaneEmptyLayoutReturnsNil() {
    XCTAssertNil(identifyAgentPane(panes: [], chromeBarPane: nil))
  }

  func testIdentifyAgentPanePrefersAuthoritativeMainPaneWhenInLayout() {
    // A 3+ pane window is exactly the case the heuristic can't handle --
    // main_pane must win outright when it names a real pane, without even
    // consulting chromeBarPane.
    let panes = [
      PaneLayout(id: "%1", cols: 100, rows: 50, x: 0, y: 0),
      PaneLayout(id: "%2", cols: 100, rows: 20, x: 0, y: 50),
      PaneLayout(id: "%3", cols: 100, rows: 4, x: 0, y: 70),
    ]
    XCTAssertEqual(
      identifyAgentPane(panes: panes, chromeBarPane: "%3", mainPane: "%2"), "%2")
  }

  func testIdentifyAgentPaneFallsBackToHeuristicWhenMainPaneNotInLayout() {
    // A stale main_pane naming a pane that no longer exists must not win
    // over the heuristic.
    let panes = [
      PaneLayout(id: "%509", cols: 214, rows: 71, x: 0, y: 0),
      PaneLayout(id: "%510", cols: 214, rows: 4, x: 0, y: 72),
    ]
    XCTAssertEqual(
      identifyAgentPane(panes: panes, chromeBarPane: "%510", mainPane: "%999"), "%509")
  }

  func testIdentifyAgentPaneNilOrEmptyMainPanePreservesExistingBehavior() {
    let panes = [
      PaneLayout(id: "%509", cols: 214, rows: 71, x: 0, y: 0),
      PaneLayout(id: "%510", cols: 214, rows: 4, x: 0, y: 72),
    ]
    XCTAssertEqual(
      identifyAgentPane(panes: panes, chromeBarPane: "%510", mainPane: nil), "%509")
    XCTAssertEqual(
      identifyAgentPane(panes: panes, chromeBarPane: "%510", mainPane: ""), "%509")
  }

  // MARK: - WindowHookState decoding

  func testWindowHookStateDecodesChromeBarPane() throws {
    let json = #"{"chrome_bar_pane": "%510", "state": "working"}"#
    let hook = try JSONDecoder().decode(WindowHookState.self, from: Data(json.utf8))
    XCTAssertEqual(hook.chromeBarPane, "%510")
  }

  func testWindowHookStateMissingChromeBarPaneDefaultsEmpty() throws {
    let json = #"{"state": "working"}"#
    let hook = try JSONDecoder().decode(WindowHookState.self, from: Data(json.utf8))
    XCTAssertEqual(hook.chromeBarPane, "")
  }
}
