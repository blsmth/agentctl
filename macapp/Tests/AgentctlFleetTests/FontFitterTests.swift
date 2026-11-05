import XCTest

@testable import AgentctlFleet

final class FontFitterTests: XCTestCase {

  func testTypicalPaneFitsWithinRange() {
    // 80 columns in an 800pt-wide view: 800 / (80 * 0.6) ≈ 16.7pt.
    let size = FontFitter.pointSize(forColumns: 80, viewWidth: 800)
    XCTAssertEqual(size, 800 / (80 * 0.6), accuracy: 0.001)
  }

  func testWideColumnPaneStillClampsToMinimum() {
    // A 222-column pane (the fleet's widest observed real pane) in a
    // laptop-sized 900pt view: 900 / (222 * 0.6) ≈ 6.76pt — above the
    // floor, but small, which is the accepted tradeoff for not reflowing
    // the pane.
    let size = FontFitter.pointSize(forColumns: 222, viewWidth: 900)
    XCTAssertEqual(size, 900 / (222 * 0.6), accuracy: 0.001)
    XCTAssertGreaterThanOrEqual(size, FontFitter.Constants.minimumPointSize)
  }

  func testExtremelyWidePaneClampsToMinimumFloor() {
    // 222 columns crammed into a narrow 200pt view would compute well
    // below the illegible floor — must clamp rather than shrink further.
    let size = FontFitter.pointSize(forColumns: 222, viewWidth: 200)
    XCTAssertEqual(size, FontFitter.Constants.minimumPointSize)
  }

  func testZeroWidthReturnsMinimum() {
    XCTAssertEqual(
      FontFitter.pointSize(forColumns: 80, viewWidth: 0), FontFitter.Constants.minimumPointSize)
  }

  func testNegativeWidthReturnsMinimum() {
    XCTAssertEqual(
      FontFitter.pointSize(forColumns: 80, viewWidth: -100), FontFitter.Constants.minimumPointSize)
  }

  func testZeroColumnsReturnsMinimum() {
    XCTAssertEqual(
      FontFitter.pointSize(forColumns: 0, viewWidth: 800), FontFitter.Constants.minimumPointSize)
  }

  func testNegativeColumnsReturnsMinimum() {
    XCTAssertEqual(
      FontFitter.pointSize(forColumns: -5, viewWidth: 800), FontFitter.Constants.minimumPointSize)
  }

  func testNarrowPaneClampsToMaximum() {
    // A tiny 10-column pane in a huge view would compute a comically
    // large point size — must clamp to the ceiling instead.
    let size = FontFitter.pointSize(forColumns: 10, viewWidth: 5000)
    XCTAssertEqual(size, FontFitter.Constants.maximumPointSize)
  }
}
