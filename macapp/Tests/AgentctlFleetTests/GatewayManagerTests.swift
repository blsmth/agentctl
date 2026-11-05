import XCTest

@testable import AgentctlFleet

@MainActor
final class GatewayManagerTests: XCTestCase {

  /// A `GatewayManager.GatewayFactory` that records every session it was
  /// asked to build a gateway for, succeeding for everything except the
  /// sessions named in `failingSessions`.
  private func makeFactory(failingSessions: Set<String> = []) -> (
    factory: GatewayManager.GatewayFactory, calls: Box<[String]>
  ) {
    let calls = Box<[String]>([])
    let factory: GatewayManager.GatewayFactory = { session in
      calls.value.append(session)
      if failingSessions.contains(session) {
        throw ControlModeError.tmuxNotFound
      }
      return try ControlModeGateway(sessionName: session, tmuxPath: "/usr/bin/tmux")
    }
    return (factory, calls)
  }

  func testSyncStartsGatewaysForNewSessions() {
    let (factory, _) = makeFactory()
    let manager = GatewayManager(makeGateway: factory)

    manager.sync(with: ["agentctl-foo", "agentctl-bar"])

    XCTAssertEqual(Set(manager.gateways.keys), ["agentctl-foo", "agentctl-bar"])
    XCTAssertNotNil(manager.gateway(for: "agentctl-foo"))
    XCTAssertNotNil(manager.gateway(for: "agentctl-bar"))
  }

  func testSyncStopsAndDropsGoneSessions() {
    let (factory, _) = makeFactory()
    let manager = GatewayManager(makeGateway: factory)

    manager.sync(with: ["agentctl-foo", "agentctl-bar"])
    manager.sync(with: ["agentctl-foo"])

    XCTAssertEqual(Set(manager.gateways.keys), ["agentctl-foo"])
    XCTAssertNil(manager.gateway(for: "agentctl-bar"))
  }

  func testSyncIsIdempotentForUnchangedList() {
    let (factory, calls) = makeFactory()
    let manager = GatewayManager(makeGateway: factory)

    manager.sync(with: ["agentctl-foo"])
    manager.sync(with: ["agentctl-foo"])

    XCTAssertEqual(
      calls.value, ["agentctl-foo"], "the second sync must not rebuild an already-live gateway")
    XCTAssertEqual(Set(manager.gateways.keys), ["agentctl-foo"])
  }

  func testSyncSurvivesAThrowingInitWithoutAbortingTheLoop() {
    let (factory, _) = makeFactory(failingSessions: ["agentctl-bad"])
    let manager = GatewayManager(makeGateway: factory)

    manager.sync(with: ["agentctl-bad", "agentctl-good"])

    XCTAssertNil(manager.gateway(for: "agentctl-bad"), "a throwing init must not get a gateway")
    XCTAssertNotNil(
      manager.gateway(for: "agentctl-good"),
      "a throwing init for one session must not abort the sync loop for the rest")
  }

  func testOnGatewayAddedAndRemovedFire() {
    let (factory, _) = makeFactory()
    let manager = GatewayManager(makeGateway: factory)

    let added = Box<[String]>([])
    let removed = Box<[String]>([])
    manager.onGatewayAdded = { session, _ in added.value.append(session) }
    manager.onGatewayRemoved = { session in removed.value.append(session) }

    manager.sync(with: ["agentctl-foo"])
    manager.sync(with: [])

    XCTAssertEqual(added.value, ["agentctl-foo"])
    XCTAssertEqual(removed.value, ["agentctl-foo"])
  }
}

/// A minimal reference-typed box so the `@Sendable`-checked closures above
/// can accumulate calls without capturing a `var` directly.
private final class Box<T> {
  var value: T
  init(_ value: T) { self.value = value }
}
