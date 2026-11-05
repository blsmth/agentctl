import Foundation

/// Turns raw bytes (from `TerminalViewDelegate.send`, or any other input
/// source) into `send-keys -H` commands for a tmux pane.
///
/// This deliberately does not attempt iTerm2-style classification into
/// literal text vs. control sequences: `send-keys -H <hex bytes>` is a
/// verified general byte-injection primitive (round-tripped ASCII, ESC
/// sequences, UTF-8, and control bytes against a live raw-mode pane), it
/// sidesteps tmux 3.5+'s `modifyOtherKeys` hazard entirely (which silently
/// turns `send-keys 0x0d` into the literal text `"0x0d"`), and it avoids any
/// quoting bugs in the `-l` literal-string path. The only cost is ~3 characters
/// of wire overhead per byte, irrelevant at human typing rates.
enum KeyEncoder {
  /// Maximum length, in characters, of a single emitted `send-keys`
  /// command. Chunking long input (e.g. a paste) avoids the "long tmux
  /// command" hazard documented by other control-mode clients (iTerm2).
  static let maxCommandLength = 1000

  /// Builds one or more `send-keys -t <paneID> -H <hex...>` commands that,
  /// sent in order, inject `bytes` into `paneID` verbatim. Returns an empty
  /// array for empty input.
  static func encodeKeys(paneID: String, bytes: ArraySlice<UInt8>) -> [String] {
    guard !bytes.isEmpty else { return [] }

    let prefix = "send-keys -t \(paneID) -H"
    // Each byte contributes " XX" (3 chars) to the command line.
    let bytesPerChunk = max(1, (maxCommandLength - prefix.count) / 3)

    var commands: [String] = []
    var chunk: [UInt8] = []
    chunk.reserveCapacity(bytesPerChunk)

    for byte in bytes {
      chunk.append(byte)
      if chunk.count == bytesPerChunk {
        commands.append(command(prefix: prefix, bytes: chunk))
        chunk.removeAll(keepingCapacity: true)
      }
    }
    if !chunk.isEmpty {
      commands.append(command(prefix: prefix, bytes: chunk))
    }
    return commands
  }

  private static func command(prefix: String, bytes: [UInt8]) -> String {
    let hex = bytes.map { String(format: "%02x", $0) }.joined(separator: " ")
    return "\(prefix) \(hex)"
  }
}
