import XCTest

@testable import AgentctlFleet

final class ParserTests: XCTestCase {
  func testOutputNotification() {
    let parser = ControlModeParser()
    let event = parser.consume(line: "%output %0 echo TYPED_INTO_PANE\\015")
    XCTAssertEqual(
      event,
      .output(pane: "%0", bytes: Array("echo TYPED_INTO_PANE".utf8) + [0x0D])
    )
  }

  func testLayoutChangeNotification() {
    let parser = ControlModeParser()
    let event = parser.consume(
      line: "%layout-change @0 ac9d,200x50,0,0,0 ac9d,200x50,0,0,0 *"
    )
    XCTAssertEqual(
      event,
      .layoutChange(
        window: "@0", layout: "ac9d,200x50,0,0,0", visibleLayout: "ac9d,200x50,0,0,0", flags: "*"
      )
    )
  }

  func testWindowAddCloseRenamed() {
    let parser = ControlModeParser()
    XCTAssertEqual(parser.consume(line: "%window-add @5"), .windowAdd(window: "@5"))
    XCTAssertEqual(parser.consume(line: "%window-close @5"), .windowClose(window: "@5"))
    XCTAssertEqual(
      parser.consume(line: "%window-renamed @5 my window"),
      .windowRenamed(window: "@5", name: "my window")
    )
  }

  func testSessionChanged() {
    let parser = ControlModeParser()
    XCTAssertEqual(
      parser.consume(line: "%session-changed $3 agentctl-myproject"),
      .sessionChanged(session: "$3", name: "agentctl-myproject")
    )
  }

  func testPauseAndContinue() {
    let parser = ControlModeParser()
    XCTAssertEqual(parser.consume(line: "%pause %2"), .pause(pane: "%2"))
    XCTAssertEqual(parser.consume(line: "%continue %2"), .unpause(pane: "%2"))
  }

  func testExitWithAndWithoutReason() {
    let parser = ControlModeParser()
    XCTAssertEqual(parser.consume(line: "%exit"), .exit(reason: nil))
    XCTAssertEqual(
      parser.consume(line: "%exit server exited unexpectedly"),
      .exit(reason: "server exited unexpectedly")
    )
  }

  func testUnknownNotificationIsIgnoredNotCrashed() {
    let parser = ControlModeParser()
    XCTAssertNil(parser.consume(line: "%sessions-changed"))
    XCTAssertNil(parser.consume(line: "%some-future-notification foo bar"))
  }

  func testReplyBlockBuffersUntilMatchingEnd() {
    let parser = ControlModeParser()
    XCTAssertNil(parser.consume(line: "%begin 1788205849 298 1"))
    XCTAssertNil(parser.consume(line: "%0 @0"))
    XCTAssertNil(parser.consume(line: "%1 @1"))
    let event = parser.consume(line: "%end 1788205849 298 1")
    XCTAssertEqual(event, .reply(commandNumber: 298, lines: ["%0 @0", "%1 @1"], isError: false))
  }

  func testErrorBlockIsFlaggedAsError() {
    let parser = ControlModeParser()
    XCTAssertNil(parser.consume(line: "%begin 100 1 1"))
    XCTAssertNil(parser.consume(line: "unknown command"))
    let event = parser.consume(line: "%error 100 1 1")
    XCTAssertEqual(event, .reply(commandNumber: 1, lines: ["unknown command"], isError: true))
  }

  func testEmptyReplyBlock() {
    let parser = ControlModeParser()
    XCTAssertNil(parser.consume(line: "%begin 1 42 1"))
    let event = parser.consume(line: "%end 1 42 1")
    XCTAssertEqual(event, .reply(commandNumber: 42, lines: [], isError: false))
  }

  func testNotificationInsideBlockIsEmittedDefensively() {
    // The protocol never actually does this, but the parser must not
    // silently swallow a notification into the reply buffer if it did.
    let parser = ControlModeParser()
    XCTAssertNil(parser.consume(line: "%begin 1 1 1"))
    let event = parser.consume(line: "%output %0 surprise")
    XCTAssertEqual(event, .output(pane: "%0", bytes: Array("surprise".utf8)))
    // The block is still open and %end still closes it normally.
    let end = parser.consume(line: "%end 1 1 1")
    XCTAssertEqual(end, .reply(commandNumber: 1, lines: [], isError: false))
  }

  func testMismatchedEndIsIgnoredDefensively() {
    let parser = ControlModeParser()
    XCTAssertNil(parser.consume(line: "%begin 1 5 1"))
    XCTAssertNil(parser.consume(line: "%end 1 99 1"))  // wrong command number
  }

  func testStrayLineOutsideBlockIsIgnored() {
    let parser = ControlModeParser()
    XCTAssertNil(parser.consume(line: "this is not a control-mode line"))
  }

  // MARK: - .attached

  func testAttachedIsNotPartOfTheWireProtocol() {
    // `.attached` is synthesized by `ControlModeGateway`, never sent by
    // tmux itself — the parser must not treat a line that happens to
    // start with "attached" as anything but a stray, ignored line.
    let parser = ControlModeParser()
    XCTAssertNil(parser.consume(line: "attached"))
    XCTAssertNil(parser.consume(line: "%attached"))
  }

  func testAttachedIsEquatable() {
    XCTAssertEqual(ControlEvent.attached, ControlEvent.attached)
    XCTAssertNotEqual(ControlEvent.attached, ControlEvent.exit(reason: nil))
  }
}
