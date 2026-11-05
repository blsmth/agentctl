import Foundation

/// One pane's geometry, parsed from a tmux layout string.
struct PaneLayout: Equatable {
  let id: String
  let cols: Int
  let rows: Int
  let x: Int
  let y: Int
}

/// Parses a tmux window layout string into its panes, in visual order (the
/// order tmux itself uses — the first pane is index 0).
///
/// Grammar (from tmux's own `layout-custom.c`, cross-checked against real
/// layouts captured on this machine):
///
///     layout    := checksum "," node
///     node      := dims "," (pane-id | "[" node ("," node)* "]" | "{" node ("," node)* "}")
///     dims      := cols "x" rows "," x "," y
///
/// A leaf node is `WxH,x,y,<pane-id>`; a split node is
/// `WxH,x,y[<child>,<child>,...]` (vertical) or `WxH,x,y{...}` (horizontal).
///
/// Malformed input returns an empty array rather than crashing — a layout
/// string is untrusted external input from a tmux version we may not have
/// tested against.
func parseLayout(_ s: String) -> [PaneLayout] {
  // Strip the leading 4-hex checksum and its comma.
  guard let firstComma = s.firstIndex(of: ",") else { return [] }
  let rest = s[s.index(after: firstComma)...]

  var scanner = LayoutScanner(rest)
  guard let node = scanner.parseNode() else { return [] }

  var panes: [PaneLayout] = []
  flatten(node, into: &panes)
  return panes
}

/// The parsed tree shape, before flattening into visual order.
private indirect enum LayoutNode {
  case leaf(PaneLayout)
  case split([LayoutNode])
}

private func flatten(_ node: LayoutNode, into panes: inout [PaneLayout]) {
  switch node {
  case .leaf(let pane):
    panes.append(pane)
  case .split(let children):
    for child in children {
      flatten(child, into: &panes)
    }
  }
}

/// A minimal recursive-descent scanner over the layout grammar. Operates on
/// a `Substring` cursor rather than regex — the grammar is recursive
/// (splits nest splits) which regex can't express cleanly.
private struct LayoutScanner {
  private var chars: [Character]
  private var index = 0

  init(_ s: Substring) {
    self.chars = Array(s)
  }

  private var current: Character? {
    index < chars.count ? chars[index] : nil
  }

  private mutating func advance() {
    index += 1
  }

  /// Consumes characters matching `predicate` and returns them as a string.
  private mutating func take(while predicate: (Character) -> Bool) -> String {
    var result = ""
    while let c = current, predicate(c) {
      result.append(c)
      advance()
    }
    return result
  }

  private mutating func expect(_ c: Character) -> Bool {
    guard current == c else { return false }
    advance()
    return true
  }

  /// Parses `WxH,x,y` followed by either a pane ID, a `[...]` split, or a
  /// `{...}` split.
  mutating func parseNode() -> LayoutNode? {
    guard let cols = parseInt(), expect("x"), let rows = parseInt(),
      expect(","), let x = parseInt(),
      expect(","), let y = parseInt()
    else { return nil }

    switch current {
    case "[":
      advance()
      guard let children = parseChildren(closing: "]") else { return nil }
      return .split(children)
    case "{":
      advance()
      guard let children = parseChildren(closing: "}") else { return nil }
      return .split(children)
    case ",":
      advance()
      let id = take { $0 != "," && $0 != "]" && $0 != "}" }
      guard !id.isEmpty else { return nil }
      // Layout strings encode pane IDs as bare digits ("509"); every
      // other tmux control-mode surface (e.g. `%output %509 ...`)
      // carries the "%" prefix, so normalize here for consistency.
      return .leaf(PaneLayout(id: "%" + id, cols: cols, rows: rows, x: x, y: y))
    default:
      return nil
    }
  }

  private mutating func parseChildren(closing: Character) -> [LayoutNode]? {
    var children: [LayoutNode] = []
    while true {
      guard let child = parseNode() else { return nil }
      children.append(child)
      if current == "," {
        advance()
        continue
      }
      if expect(closing) {
        return children
      }
      return nil
    }
  }

  private mutating func parseInt() -> Int? {
    let digits = take { $0.isNumber }
    return Int(digits)
  }
}
