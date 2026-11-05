import XCTest

@testable import AgentctlFleet

final class FleetStoreTests: XCTestCase {

  // MARK: - RegistryWatcher.loadEntries

  func testLoadEntriesSkipsStaleAndCorruptFiles() throws {
    let dir = try makeTempDir()
    defer { try? FileManager.default.removeItem(at: dir) }

    let now = Date()
    try writeRegistryEntry(
      in: dir, name: "fresh",
      entry: RegistryEntry(
        sessionName: "agentctl-fresh", lastSeen: Int64(now.timeIntervalSince1970))
    )
    try writeRegistryEntry(
      in: dir, name: "stale",
      entry: RegistryEntry(
        sessionName: "agentctl-stale", lastSeen: Int64(now.timeIntervalSince1970 - 60)
      )
    )
    try Data("not valid json".utf8).write(to: dir.appendingPathComponent("corrupt.json"))

    let entries = RegistryWatcher.loadEntries(from: dir, now: now, staleAfter: 30)
    XCTAssertEqual(entries.map(\.sessionName), ["agentctl-fresh"])
  }

  func testLoadEntriesReturnsEmptyForMissingDirectory() {
    let missing = URL(fileURLWithPath: "/tmp/agentctl-fleet-tests-does-not-exist-\(UUID())")
    XCTAssertEqual(RegistryWatcher.loadEntries(from: missing), [])
  }

  func testLoadEntriesSortsBySessionName() throws {
    let dir = try makeTempDir()
    defer { try? FileManager.default.removeItem(at: dir) }
    let now = Date()
    try writeRegistryEntry(
      in: dir, name: "b",
      entry: RegistryEntry(
        sessionName: "agentctl-bravo", lastSeen: Int64(now.timeIntervalSince1970))
    )
    try writeRegistryEntry(
      in: dir, name: "a",
      entry: RegistryEntry(
        sessionName: "agentctl-alpha", lastSeen: Int64(now.timeIntervalSince1970))
    )
    let entries = RegistryWatcher.loadEntries(from: dir, now: now)
    XCTAssertEqual(entries.map(\.sessionName), ["agentctl-alpha", "agentctl-bravo"])
  }

  // MARK: - StateWatcher.loadSnapshot

  func testLoadSnapshotReadsStateJSON() throws {
    let dir = try makeTempDir()
    defer { try? FileManager.default.removeItem(at: dir) }
    let json = """
      {"agents": [{"name": "a", "state": "working"}], "timestamp": "\(iso8601Now())"}
      """
    try Data(json.utf8).write(to: dir.appendingPathComponent("state.json"))

    let snapshot = StateWatcher.loadSnapshot(runtimeDir: dir)
    XCTAssertEqual(snapshot?.agents.count, 1)
    XCTAssertEqual(snapshot?.agents.first?.name, "a")
  }

  func testLoadSnapshotReturnsNilWhenMissing() {
    let missing = URL(fileURLWithPath: "/tmp/agentctl-fleet-tests-missing-\(UUID())")
    XCTAssertNil(StateWatcher.loadSnapshot(runtimeDir: missing))
  }

  func testLoadSnapshotReturnsNilOnCorruptJSON() throws {
    let dir = try makeTempDir()
    defer { try? FileManager.default.removeItem(at: dir) }
    try Data("{not json".utf8).write(to: dir.appendingPathComponent("state.json"))
    XCTAssertNil(StateWatcher.loadSnapshot(runtimeDir: dir))
  }

  // MARK: - Pure ordering (mirrors internal/herd/herd_test.go's cases)

  func testRowSortRankOrdering() {
    let blocked = AgentSnapshot(state: "blocked")
    let doneUnseen = AgentSnapshot(state: "idle", done: true)
    let working = AgentSnapshot(state: "working")
    let idle = AgentSnapshot(state: "idle")

    XCTAssertLessThan(FleetStore.rowSortRank(blocked), FleetStore.rowSortRank(doneUnseen))
    XCTAssertLessThan(FleetStore.rowSortRank(doneUnseen), FleetStore.rowSortRank(working))
    XCTAssertLessThan(FleetStore.rowSortRank(working), FleetStore.rowSortRank(idle))
  }

  func testSortRowsOrdersBlockedDoneWorkingIdleThenByWindowIndex() {
    let rows = [
      AgentSnapshot(name: "idle-2", windowIndex: 2, state: "idle"),
      AgentSnapshot(name: "working-1", windowIndex: 1, state: "working"),
      AgentSnapshot(name: "blocked-3", windowIndex: 3, state: "blocked"),
      AgentSnapshot(name: "done-0", windowIndex: 0, state: "idle", done: true),
      AgentSnapshot(name: "idle-0", windowIndex: 0, state: "idle"),
    ]
    let sorted = FleetStore.sortRows(rows)
    XCTAssertEqual(
      sorted.map(\.name),
      ["blocked-3", "done-0", "working-1", "idle-0", "idle-2"]
    )
  }

  func testRollupOfPrecedenceDiffersFromRowSortRank() {
    // A row that is both "working" and Done=true rolls up to Working
    // (rollupOf checks state=="working" before Done), even though it
    // sorts *after* a blocked-but-not-working done row in rowSortRank
    // terms. This asymmetry is deliberate — see herd.go's rollupOf doc.
    let workingAndDone = AgentSnapshot(state: "working", done: true)
    XCTAssertEqual(FleetStore.rollupOf(workingAndDone), .working)

    let doneOnly = AgentSnapshot(state: "idle", done: true)
    XCTAssertEqual(FleetStore.rollupOf(doneOnly), .done)

    let blocked = AgentSnapshot(state: "blocked")
    XCTAssertEqual(FleetStore.rollupOf(blocked), .blocked)

    let idle = AgentSnapshot(state: "idle")
    XCTAssertEqual(FleetStore.rollupOf(idle), .idle)
  }

  func testRollupRowsPicksMostUrgent() {
    XCTAssertEqual(FleetStore.rollupRows([]), .idle)
    XCTAssertEqual(
      FleetStore.rollupRows([AgentSnapshot(state: "idle"), AgentSnapshot(state: "working")]),
      .working
    )
    XCTAssertEqual(
      FleetStore.rollupRows([
        AgentSnapshot(state: "working"), AgentSnapshot(state: "blocked"),
      ]),
      .blocked
    )
    XCTAssertEqual(
      FleetStore.rollupRows([AgentSnapshot(state: "idle", done: true)]),
      .done
    )
  }

  func testOrderGroupsSortsByRollupThenSessionName() {
    let groups = [
      SessionGroup(
        session: "agentctl-zeta", project: "zeta", rollup: .idle, rows: [], stale: false),
      SessionGroup(
        session: "agentctl-alpha", project: "alpha", rollup: .blocked, rows: [], stale: false),
      SessionGroup(
        session: "agentctl-beta", project: "beta", rollup: .working, rows: [], stale: false),
      SessionGroup(
        session: "agentctl-gamma", project: "gamma", rollup: .blocked, rows: [], stale: false),
    ]
    let ordered = FleetStore.orderGroups(groups)
    XCTAssertEqual(
      ordered.map(\.session),
      ["agentctl-alpha", "agentctl-gamma", "agentctl-beta", "agentctl-zeta"]
    )
  }

  func testProjectDisplayNameFallsBackToSessionName() {
    XCTAssertEqual(
      FleetStore.projectDisplayName(
        RegistryEntry(sessionName: "agentctl-x", projectDir: "/repo/myapp")),
      "myapp"
    )
    XCTAssertEqual(
      FleetStore.projectDisplayName(RegistryEntry(sessionName: "agentctl-x", projectDir: "")),
      "agentctl-x"
    )
    XCTAssertEqual(
      FleetStore.projectDisplayName(RegistryEntry(sessionName: "agentctl-x", projectDir: "/")),
      "agentctl-x"
    )
  }

  // MARK: - Helpers

  private func makeTempDir() throws -> URL {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    return dir
  }

  private func writeRegistryEntry(in dir: URL, name: String, entry: RegistryEntry) throws {
    let data = try JSONEncoder().encode(entry)
    try data.write(to: dir.appendingPathComponent("\(name).json"))
  }

  private func iso8601Now() -> String {
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return formatter.string(from: Date())
  }
}
