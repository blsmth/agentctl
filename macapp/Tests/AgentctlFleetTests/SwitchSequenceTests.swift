import XCTest

@testable import AgentctlFleet

final class SwitchSequenceTests: XCTestCase {

  // MARK: - First selection (no old pane)

  func testFirstSelectionHasNoGateOffStep() {
    let steps = SwitchSequence.steps(oldPane: nil, newPane: (session: "agentctl-foo", pane: "%1"))

    XCTAssertEqual(
      steps,
      [
        SwitchStep(
          session: "agentctl-foo", command: "refresh-client -A \"%1:on\"", awaitsReply: false),
        SwitchStep(
          session: "agentctl-foo", command: "capture-pane -peqJ -t %1 -S -2000", awaitsReply: true),
      ])
  }

  // MARK: - Same-session switch

  func testSameSessionSwitchOrdersOldOffThenNewOnThenCapture() {
    let steps = SwitchSequence.steps(
      oldPane: (session: "agentctl-foo", pane: "%1"), newPane: (session: "agentctl-foo", pane: "%2")
    )

    XCTAssertEqual(
      steps,
      [
        SwitchStep(
          session: "agentctl-foo", command: "refresh-client -A \"%1:off\"", awaitsReply: false),
        SwitchStep(
          session: "agentctl-foo", command: "refresh-client -A \"%2:on\"", awaitsReply: false),
        SwitchStep(
          session: "agentctl-foo", command: "capture-pane -peqJ -t %2 -S -2000", awaitsReply: true),
      ])
  }

  // MARK: - Cross-session switch

  func testCrossSessionSwitchTargetsEachStepAtItsOwnSession() {
    let steps = SwitchSequence.steps(
      oldPane: (session: "agentctl-foo", pane: "%1"), newPane: (session: "agentctl-bar", pane: "%9")
    )

    XCTAssertEqual(
      steps,
      [
        SwitchStep(
          session: "agentctl-foo", command: "refresh-client -A \"%1:off\"", awaitsReply: false),
        SwitchStep(
          session: "agentctl-bar", command: "refresh-client -A \"%9:on\"", awaitsReply: false),
        SwitchStep(
          session: "agentctl-bar", command: "capture-pane -peqJ -t %9 -S -2000", awaitsReply: true),
      ])
  }

  // MARK: - Re-attach: same shape as first selection, not a special case

  func testReattachUsesTheFirstSelectionShape() {
    // A reconnected client has no prior gating state to undo — the
    // caller passes `oldPane: nil` for a re-attach exactly as it would
    // for a first selection, so this must produce an identical sequence.
    let firstSelection = SwitchSequence.steps(
      oldPane: nil, newPane: (session: "agentctl-foo", pane: "%1"))
    let reattach = SwitchSequence.steps(
      oldPane: nil, newPane: (session: "agentctl-foo", pane: "%1"))
    XCTAssertEqual(firstSelection, reattach)
  }

  // MARK: - Quoting and capture depth

  func testPaneIDIsQuotedInBothRefreshClientCalls() {
    let steps = SwitchSequence.steps(
      oldPane: (session: "agentctl-foo", pane: "%1"), newPane: (session: "agentctl-foo", pane: "%2")
    )
    let refreshClientCommands = steps.map(\.command).filter { $0.hasPrefix("refresh-client") }
    XCTAssertEqual(
      refreshClientCommands, ["refresh-client -A \"%1:off\"", "refresh-client -A \"%2:on\""])
  }

  func testCapturePaneAlwaysHasScrollbackDepth() {
    let steps = SwitchSequence.steps(oldPane: nil, newPane: (session: "agentctl-foo", pane: "%1"))
    let captureCommand = steps.last!.command
    XCTAssertTrue(
      captureCommand.contains("-S -2000"),
      "capture must request the full bootstrap scrollback depth")
    XCTAssertTrue(captureCommand.contains("-peqJ"))
  }

  func testEmptyOldPaneIsTreatedAsNoOldPane() {
    // Defensive: an empty pane string must not produce a bogus
    // `refresh-client -A "":off"` command.
    let steps = SwitchSequence.steps(
      oldPane: (session: "agentctl-foo", pane: ""), newPane: (session: "agentctl-foo", pane: "%1"))
    XCTAssertEqual(
      steps.count, 2, "an empty old pane must be treated the same as no old pane at all")
  }
}

// MARK: - Buffered output (the step 4 -> 5 race)

final class PaneOutputBufferTests: XCTestCase {

  func testOutputArrivingDuringBootstrapIsBufferedInArrivalOrder() {
    var buffer = PaneOutputBuffer()
    buffer.beginBootstrap()

    buffer.recordIfBootstrapping([1, 2])
    buffer.recordIfBootstrapping([3])

    XCTAssertEqual(buffer.state, .bootstrapping)
    let drained = buffer.endBootstrapAndDrain()
    XCTAssertEqual(
      drained, [[1, 2], [3]], "buffered output must flush in the order it arrived, not dropped")
  }

  func testDrainingSwitchesToStreamingAndClearsTheBuffer() {
    var buffer = PaneOutputBuffer()
    buffer.beginBootstrap()
    buffer.recordIfBootstrapping([9])
    _ = buffer.endBootstrapAndDrain()

    XCTAssertEqual(buffer.state, .streaming)
    // Nothing recorded while streaming, so a second drain is empty rather
    // than replaying the same bytes twice.
    XCTAssertEqual(buffer.endBootstrapAndDrain(), [])
  }

  func testOutputWhileStreamingIsNotBuffered() {
    var buffer = PaneOutputBuffer()
    XCTAssertEqual(buffer.state, .streaming, "a fresh buffer starts in the streaming state")
    buffer.recordIfBootstrapping([1])
    XCTAssertEqual(
      buffer.buffered, [], "recordIfBootstrapping must no-op outside the bootstrapping state")
  }

  func testStartingANewBootstrapDiscardsAnyPriorLeftoverBuffer() {
    var buffer = PaneOutputBuffer()
    buffer.beginBootstrap()
    buffer.recordIfBootstrapping([1])
    // A second switch starts before the first's bootstrap ever drained —
    // its stale buffered bytes must not leak into the new bootstrap.
    buffer.beginBootstrap()
    XCTAssertEqual(buffer.buffered, [])
  }
}
