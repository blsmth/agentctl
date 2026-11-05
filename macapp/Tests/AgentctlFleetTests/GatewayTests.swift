import XCTest

@testable import AgentctlFleet

final class GatewayTests: XCTestCase {

  // MARK: - TmuxLocator

  func testResolvesFirstExistingCandidate() {
    let existing: Set<String> = ["/usr/local/bin/tmux"]
    let resolved = TmuxLocator.resolve(
      searchPaths: ["/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"],
      fileExists: { existing.contains($0) }
    )
    XCTAssertEqual(resolved, "/usr/local/bin/tmux")
  }

  func testPrefersEarlierCandidateWhenBothExist() {
    let existing: Set<String> = ["/opt/homebrew/bin/tmux", "/usr/bin/tmux"]
    let resolved = TmuxLocator.resolve(
      searchPaths: ["/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"],
      fileExists: { existing.contains($0) }
    )
    XCTAssertEqual(resolved, "/opt/homebrew/bin/tmux")
  }

  func testReturnsNilWhenNoCandidateExists() {
    let resolved = TmuxLocator.resolve(
      searchPaths: ["/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"],
      fileExists: { _ in false }
    )
    XCTAssertNil(resolved)
  }

  func testGatewayInitSucceedsWithExplicitPath() throws {
    let gateway = try ControlModeGateway(sessionName: "agentctl-test", tmuxPath: "/usr/bin/tmux")
    _ = gateway  // constructing it is the assertion; no live attach happens.
  }

  // MARK: - Backoff

  func testBackoffDoublesUntilCap() {
    var delay = Backoff.initial
    XCTAssertEqual(delay, 1)
    delay = Backoff.next(after: delay)
    XCTAssertEqual(delay, 2)
    delay = Backoff.next(after: delay)
    XCTAssertEqual(delay, 4)
    delay = Backoff.next(after: delay)
    XCTAssertEqual(delay, 8)
    delay = Backoff.next(after: delay)
    XCTAssertEqual(delay, 16)
    delay = Backoff.next(after: delay)
    XCTAssertEqual(delay, 30, "should cap at 30s")
    delay = Backoff.next(after: delay)
    XCTAssertEqual(delay, 30, "should stay capped")
  }

  func testBackoffCustomCap() {
    XCTAssertEqual(Backoff.next(after: 40, cap: 60), 60)
    XCTAssertEqual(Backoff.next(after: 100, cap: 60), 60)
  }
}
