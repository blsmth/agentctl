import Foundation

/// One decoded event from a tmux control-mode (`tmux -C attach`) stdout stream.
///
/// Notifications (`%output`, `%layout-change`, ...) arrive out-of-band and,
/// per the verified protocol behaviour, never appear inside a `%begin`/`%end`
/// reply block. `.reply` is emitted once a block closes with `%end` or
/// `%error`.
enum ControlEvent: Equatable {
  case output(pane: String, bytes: [UInt8])
  case layoutChange(window: String, layout: String, visibleLayout: String, flags: String)
  case windowAdd(window: String)
  case windowClose(window: String)
  case windowRenamed(window: String, name: String)
  case sessionChanged(session: String, name: String)
  case pause(pane: String)
  case unpause(pane: String)
  case exit(reason: String?)
  case reply(commandNumber: Int, lines: [String], isError: Bool)
  /// Not part of the tmux control-mode protocol — synthesized by
  /// `ControlModeGateway` the moment an attach succeeds and the read loop
  /// begins (including every reconnect). A reconnected client is a brand
  /// new tmux client: all `refresh-client -A` gating state from before the
  /// drop is gone, and `%output` still has no replay. Consumers must treat
  /// this identically to a fresh first attach — re-apply the full gating
  /// set and re-run the bootstrap for whatever pane was selected.
  case attached
}

/// A line-oriented state machine over a tmux control-mode client's stdout.
///
/// Feed it one line at a time (no trailing newline) via `consume(line:)`.
/// It has no knowledge of the transport (pipe, `Process`, fixture file) —
/// that separation is what makes it testable against recorded protocol
/// samples with no live tmux.
final class ControlModeParser {
  /// A `%begin`/`%end` block currently being buffered.
  private struct PendingBlock {
    let commandNumber: Int
    var lines: [String] = []
  }

  private var pending: PendingBlock?

  /// Feeds one line of control-mode output and returns the event it
  /// produced, if any. Buffered reply lines produce no event until the
  /// matching `%end`/`%error` arrives.
  func consume(line: String) -> ControlEvent? {
    if let event = Self.parseNotification(line) {
      // Defensive: notifications are documented to never appear inside
      // a block, but if tmux ever surprises us, emit rather than
      // silently swallowing it into the reply buffer.
      return event
    }

    if let block = Self.parseBlockBoundary(line) {
      return handleBoundary(block)
    }

    if pending != nil {
      pending?.lines.append(line)
      return nil
    }

    // Stray line outside any block and not a recognized notification.
    // Ignore rather than crash — tmux versions add notifications we
    // don't know about yet.
    return nil
  }

  private enum BlockBoundary {
    case begin(commandNumber: Int)
    case end(commandNumber: Int, isError: Bool)
  }

  private func handleBoundary(_ boundary: BlockBoundary) -> ControlEvent? {
    switch boundary {
    case .begin(let commandNumber):
      pending = PendingBlock(commandNumber: commandNumber)
      return nil
    case .end(let commandNumber, let isError):
      guard let block = pending, block.commandNumber == commandNumber else {
        // Mismatched or orphaned %end/%error — defensive no-op.
        pending = nil
        return nil
      }
      pending = nil
      return .reply(commandNumber: commandNumber, lines: block.lines, isError: isError)
    }
  }

  private static func parseBlockBoundary(_ line: String) -> BlockBoundary? {
    let fields = line.split(separator: " ", omittingEmptySubsequences: true)
    guard fields.count >= 3 else { return nil }
    switch fields[0] {
    case "%begin":
      guard let num = Int(fields[2]) else { return nil }
      return .begin(commandNumber: num)
    case "%end":
      guard let num = Int(fields[2]) else { return nil }
      return .end(commandNumber: num, isError: false)
    case "%error":
      guard let num = Int(fields[2]) else { return nil }
      return .end(commandNumber: num, isError: true)
    default:
      return nil
    }
  }

  private static func parseNotification(_ line: String) -> ControlEvent? {
    guard line.hasPrefix("%") else { return nil }
    let fields = line.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
    guard let keyword = fields.first else { return nil }
    let rest = fields.count > 1 ? fields[1] : Substring("")

    switch keyword {
    case "%output":
      return parseOutput(rest)
    case "%layout-change":
      return parseLayoutChange(rest)
    case "%window-add":
      return .windowAdd(window: String(rest).trimmingCharacters(in: .whitespaces))
    case "%window-close":
      return .windowClose(window: String(rest).trimmingCharacters(in: .whitespaces))
    case "%window-renamed":
      return parseWindowRenamed(rest)
    case "%session-changed":
      return parseSessionChanged(rest)
    case "%pause":
      return .pause(pane: String(rest).trimmingCharacters(in: .whitespaces))
    case "%continue":
      return .unpause(pane: String(rest).trimmingCharacters(in: .whitespaces))
    case "%exit":
      let reason = String(rest).trimmingCharacters(in: .whitespaces)
      return .exit(reason: reason.isEmpty ? nil : reason)
    default:
      // %begin/%end/%error are handled by parseBlockBoundary; anything
      // else (e.g. %sessions-changed, or a future notification type)
      // is unknown — ignore rather than crash.
      return nil
    }
  }

  private static func parseOutput(_ rest: Substring) -> ControlEvent? {
    let fields = rest.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
    guard let pane = fields.first else { return nil }
    let escaped = fields.count > 1 ? fields[1] : Substring("")
    return .output(pane: String(pane), bytes: decodeEscapedOutput(escaped))
  }

  private static func parseLayoutChange(_ rest: Substring) -> ControlEvent? {
    let fields = rest.split(separator: " ", omittingEmptySubsequences: true)
    guard fields.count >= 4 else { return nil }
    return .layoutChange(
      window: String(fields[0]),
      layout: String(fields[1]),
      visibleLayout: String(fields[2]),
      flags: String(fields[3])
    )
  }

  private static func parseWindowRenamed(_ rest: Substring) -> ControlEvent? {
    let fields = rest.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
    guard let window = fields.first else { return nil }
    let name = fields.count > 1 ? String(fields[1]) : ""
    return .windowRenamed(window: String(window), name: name)
  }

  private static func parseSessionChanged(_ rest: Substring) -> ControlEvent? {
    let fields = rest.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
    guard let session = fields.first else { return nil }
    let name = fields.count > 1 ? String(fields[1]) : ""
    return .sessionChanged(session: String(session), name: name)
  }
}
