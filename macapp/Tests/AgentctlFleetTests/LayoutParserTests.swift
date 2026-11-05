import XCTest

@testable import AgentctlFleet

final class LayoutParserTests: XCTestCase {
  func testSinglePaneNoChrome() {
    let panes = parseLayout("3306,214x76,0,0,553")
    XCTAssertEqual(panes, [PaneLayout(id: "%553", cols: 214, rows: 76, x: 0, y: 0)])
  }

  func testVerticalSplitAgentAndChrome() {
    let panes = parseLayout("d0a9,214x76,0,0[214x71,0,0,509,214x4,0,72,510]")
    XCTAssertEqual(
      panes,
      [
        PaneLayout(id: "%509", cols: 214, rows: 71, x: 0, y: 0),
        PaneLayout(id: "%510", cols: 214, rows: 4, x: 0, y: 72),
      ]
    )
  }

  func testThrowawayServerSample() {
    let panes = parseLayout("ac9d,200x50,0,0,0")
    XCTAssertEqual(panes, [PaneLayout(id: "%0", cols: 200, rows: 50, x: 0, y: 0)])
  }

  func testHorizontalSplit() {
    // Two panes side by side: a 100x50 pane at (0,0) and a 99x50 pane at
    // (101,0) (1-column separator), inside a horizontal `{...}` split.
    let panes = parseLayout("abcd,200x50,0,0{100x50,0,0,1,99x50,101,0,2}")
    XCTAssertEqual(
      panes,
      [
        PaneLayout(id: "%1", cols: 100, rows: 50, x: 0, y: 0),
        PaneLayout(id: "%2", cols: 99, rows: 50, x: 101, y: 0),
      ]
    )
  }

  func testNestedSplitVisualOrder() {
    // A vertical split whose second child is itself a horizontal split —
    // exercises recursive flattening and visual (depth-first) ordering.
    let layout = "ffff,100x50,0,0[100x25,0,0,1,100x24,0,26{50x24,0,26,2,49x24,51,26,3}]"
    let panes = parseLayout(layout)
    XCTAssertEqual(panes.map(\.id), ["%1", "%2", "%3"])
  }

  func testMalformedLayoutReturnsEmptyNotCrash() {
    XCTAssertEqual(parseLayout(""), [])
    XCTAssertEqual(parseLayout("not-a-layout"), [])
    XCTAssertEqual(parseLayout("d0a9,214x76,0,0["), [])
    XCTAssertEqual(parseLayout("d0a9,abcx76,0,0,1"), [])
  }
}
