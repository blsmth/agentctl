import Foundation

/// Decodes a tmux control-mode `%output` payload into raw bytes.
///
/// tmux escapes every byte `< 0x20` and backslash itself as a three-digit
/// octal sequence (e.g. `\015` for CR, `\012` for LF, `\134` for `\`).
/// Everything else passes through as the UTF-8 bytes of the character.
///
/// Malformed input (a trailing lone backslash, or a backslash followed by
/// fewer than three octal digits) must never crash — the bytes are emitted
/// literally instead, since a live stream can be truncated mid-line under
/// adversarial buffering even though our line-oriented reader shouldn't
/// normally hand this function a partial escape.
func decodeEscapedOutput(_ s: Substring) -> [UInt8] {
  var bytes: [UInt8] = []
  let chars = Array(s)
  var i = 0

  while i < chars.count {
    let c = chars[i]
    guard c == "\\" else {
      bytes.append(contentsOf: String(c).utf8)
      i += 1
      continue
    }

    let octalStart = i + 1
    let octalEnd = octalStart + 3
    if octalEnd <= chars.count,
      let value = octalValue(chars[octalStart..<octalEnd])
    {
      bytes.append(value)
      i = octalEnd
      continue
    }

    // Not a valid three-digit octal escape — emit the backslash
    // literally and continue from the next character.
    bytes.append(contentsOf: String(c).utf8)
    i += 1
  }

  return bytes
}

/// Parses exactly three ASCII octal digits into a byte value, or returns nil
/// if any of them isn't an octal digit.
private func octalValue(_ digits: ArraySlice<Character>) -> UInt8? {
  var value: UInt16 = 0
  for d in digits {
    guard let digit = d.wholeNumberValue, digit >= 0, digit <= 7 else { return nil }
    value = value * 8 + UInt16(digit)
  }
  guard value <= 0xFF else { return nil }
  return UInt8(value)
}
