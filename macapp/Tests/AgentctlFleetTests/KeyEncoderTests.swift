import XCTest

@testable import AgentctlFleet

final class KeyEncoderTests: XCTestCase {
  func testEmptyInputProducesNoCommands() {
    XCTAssertEqual(KeyEncoder.encodeKeys(paneID: "%0", bytes: ArraySlice([])), [])
  }

  func testSingleASCIIByte() {
    let commands = KeyEncoder.encodeKeys(paneID: "%0", bytes: ArraySlice([0x41]))
    XCTAssertEqual(commands, ["send-keys -t %0 -H 41"])
  }

  func testControlByteUsesHexNotLiteral() {
    // 0x03 is ^C — must go through -H, never the literal `-l` path.
    let commands = KeyEncoder.encodeKeys(paneID: "%3", bytes: ArraySlice([0x03]))
    XCTAssertEqual(commands, ["send-keys -t %3 -H 03"])
  }

  func testEscapeSequence() {
    let commands = KeyEncoder.encodeKeys(paneID: "%0", bytes: ArraySlice([0x1B, 0x5B, 0x41]))
    XCTAssertEqual(commands, ["send-keys -t %0 -H 1b 5b 41"])
  }

  func testUTF8MultibyteSequence() {
    let bytes = Array("✓".utf8)  // e2 9c 93
    XCTAssertEqual(bytes, [0xE2, 0x9C, 0x93])
    let commands = KeyEncoder.encodeKeys(paneID: "%0", bytes: ArraySlice(bytes))
    XCTAssertEqual(commands, ["send-keys -t %0 -H e2 9c 93"])
  }

  func testLongPasteIsChunkedUnderMaxCommandLength() {
    let bytes = ArraySlice([UInt8](repeating: 0x61, count: 600))
    let commands = KeyEncoder.encodeKeys(paneID: "%0", bytes: bytes)

    XCTAssertGreaterThan(
      commands.count, 1, "600 bytes at 3 chars each should not fit in one command")
    for command in commands {
      XCTAssertLessThanOrEqual(command.count, KeyEncoder.maxCommandLength)
    }

    // Reassemble the hex bytes across every chunk and confirm nothing
    // was dropped or reordered.
    let prefix = "send-keys -t %0 -H "
    let reassembled =
      commands
      .flatMap { command -> [UInt8] in
        let hex = command.dropFirst(prefix.count)
        return hex.split(separator: " ").map { UInt8($0, radix: 16)! }
      }
    XCTAssertEqual(reassembled, Array(bytes))
  }
}
