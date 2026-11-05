import CoreGraphics

/// Pure point-size math for fitting a monospace terminal font to a pane's
/// actual column count inside a fixed-width view — no `NSView`/`NSFont`
/// dependency, so it's directly testable.
///
/// The fleet's panes reach 222 columns; this deliberately picks small text
/// over ever resizing a live agent's pane (owning geometry is Phase 3 only).
enum FontFitter {
  enum Constants {
    /// Never render smaller than this — below it the text is illegible, so
    /// the view should scroll horizontally instead of shrinking further.
    static let minimumPointSize: CGFloat = 6
    /// A generous ceiling so a narrow pane in a wide view doesn't blow up
    /// to a comically large point size.
    static let maximumPointSize: CGFloat = 24
    /// Advance width of one monospace glyph, in points, per point of font
    /// size — measured for SF Mono/Menlo (~7.6pt advance at 13pt ≈ 0.585,
    /// rounded up slightly for headroom). A ratio rather than a measured
    /// `NSFont` advance keeps this pure and testable.
    static let advanceRatio: CGFloat = 0.6
  }

  /// Returns the point size such that `cols` monospace characters span
  /// `viewWidth`, clamped to `[minimumPointSize, maximumPointSize]`.
  ///
  /// `cols <= 0` or `viewWidth <= 0` (a not-yet-laid-out view, or a pane
  /// whose geometry hasn't been resolved yet) returns `minimumPointSize`
  /// rather than dividing by zero or going negative.
  static func pointSize(
    forColumns cols: Int,
    viewWidth: CGFloat,
    advanceRatio: CGFloat = Constants.advanceRatio,
    minimumPointSize: CGFloat = Constants.minimumPointSize,
    maximumPointSize: CGFloat = Constants.maximumPointSize
  ) -> CGFloat {
    guard cols > 0, viewWidth > 0, advanceRatio > 0 else { return minimumPointSize }
    let raw = viewWidth / (CGFloat(cols) * advanceRatio)
    return min(max(raw, minimumPointSize), maximumPointSize)
  }
}
