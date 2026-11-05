import XCTest

@testable import AgentctlFleet

final class OutputDecoderTests: XCTestCase {
  func testTableDrivenCases() {
    let cases: [(name: String, input: String, expected: [UInt8])] = [
      ("plain ascii", "hello", Array("hello".utf8)),
      ("cr lf", "a\\015\\012b", [UInt8(ascii: "a"), 0x0D, 0x0A, UInt8(ascii: "b")]),
      ("escaped backslash", "\\134", [0x5C]),
      ("escape sequence", "\\033[1m", [0x1B] + Array("[1m".utf8)),
      ("empty", "", []),
      ("lone backslash", "\\", Array("\\".utf8)),
      ("invalid two-digit octal", "\\09", Array("\\09".utf8)),
    ]

    for testCase in cases {
      let decoded = decodeEscapedOutput(Substring(testCase.input))
      XCTAssertEqual(decoded, testCase.expected, "case: \(testCase.name)")
    }
  }

  func testUTF8PassesThroughUnescaped() {
    let decoded = decodeEscapedOutput("caf\u{00e9}")
    XCTAssertEqual(decoded, Array("caf\u{00e9}".utf8))
  }

  func testTrailingLoneBackslashDoesNotCrash() {
    let decoded = decodeEscapedOutput("abc\\")
    XCTAssertEqual(decoded, Array("abc\\".utf8))
  }

  func testMixedRealSample() {
    // Captured sample: "echo TYPED_INTO_PANE\015" followed by "\012".
    let decoded = decodeEscapedOutput("echo TYPED_INTO_PANE\\015")
    XCTAssertEqual(decoded, Array("echo TYPED_INTO_PANE".utf8) + [0x0D])
  }
}
