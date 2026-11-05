import Foundation
import os

/// Errors surfaced by `ControlModeGateway`.
enum ControlModeError: Error, Equatable {
  /// No `tmux` binary was found on any of the searched paths.
  case tmuxNotFound
  /// The tmux control-mode client process failed to launch.
  case launchFailed(String)
  /// A command's reply came back as `%error`.
  case commandFailed(lines: [String])
  /// The gateway was stopped (or the pending command's connection dropped)
  /// before a reply arrived.
  case disconnected
}

/// Locates the `tmux` binary without relying on inherited `PATH` — a GUI app
/// launched from Finder/LaunchServices gets a minimal one that typically
/// excludes Homebrew.
enum TmuxLocator {
  static let defaultSearchPaths = [
    "/opt/homebrew/bin/tmux",
    "/usr/local/bin/tmux",
    "/usr/bin/tmux",
  ]

  /// Returns the first candidate path that exists, per `fileExists`.
  /// Injectable for testing without touching the real filesystem.
  static func resolve(
    searchPaths: [String] = defaultSearchPaths,
    fileExists: (String) -> Bool = { FileManager.default.isExecutableFile(atPath: $0) }
  ) -> String? {
    searchPaths.first(where: fileExists)
  }
}

/// Pure exponential backoff schedule: 1s, 2s, 4s, ... capped at 30s.
enum Backoff {
  static let initial: TimeInterval = 1
  static let cap: TimeInterval = 30

  /// Returns the delay to use *after* `current` has already been waited
  /// once — i.e. the next value in the sequence, capped at `cap`.
  static func next(after current: TimeInterval, cap: TimeInterval = Self.cap) -> TimeInterval {
    min(current * 2, cap)
  }
}

/// One control-mode client attached to a single agentctl (tmux) session.
///
/// tmux control mode multiplexes every pane's `%output` in the attached
/// session onto one stdout stream, but does **not** cross sessions — so the
/// fleet runs one `ControlModeGateway` per live agentctl session (about ten
/// today).
///
/// This type never mutates the fleet beyond what its callers explicitly
/// request via `send`. It never issues `select-window`, `resize-window`, or
/// any other command on its own — Phase 1 has no callers driving those at
/// all.
actor ControlModeGateway {
  private enum Constants {
    /// `-f ignore-size`: an attaching control client normally becomes
    /// the "latest" client under tmux's default `window-size latest`,
    /// reflowing windows a human has attached to. This flag prevents
    /// that entirely — verified against tmux 3.6a. Mandatory in Phase 1.
    static let attachFlags = ["-C", "attach", "-f", "ignore-size"]
  }

  /// Events this gateway has observed, other than command replies (which
  /// are delivered to the awaiting `send` call instead). Safe to iterate
  /// from any thread/actor context — the underlying `%output` bytes should
  /// be forwarded straight to a terminal view's `feed(byteArray:)` with no
  /// additional main-thread hop.
  nonisolated let events: AsyncStream<ControlEvent>
  private nonisolated let eventsContinuation: AsyncStream<ControlEvent>.Continuation

  private let sessionName: String
  private let tmuxPath: String
  private let logger: Logger

  private var process: Process?
  private var stdin: FileHandle?
  private let parser = ControlModeParser()

  /// FIFO of continuations waiting on a `%begin`/`%end` reply, in the
  /// order their commands were written to stdin. tmux processes control
  /// commands and replies to them strictly in order, so send-order FIFO
  /// correlation is sufficient without tracking tmux's own command
  /// numbers. `nil` entries correspond to fire-and-forget `send(_:)`
  /// calls whose reply is discarded.
  private var pendingReplies: [CheckedContinuation<[String], Error>?] = []

  private var runLoopTask: Task<Void, Never>?
  private var stopped = true

  /// - Parameters:
  ///   - sessionName: the tmux session to attach to.
  ///   - tmuxPath: resolved by `TmuxLocator` if not overridden (tests
  ///     inject a fake path).
  init(sessionName: String, tmuxPath: String? = nil) throws {
    self.sessionName = sessionName
    guard let resolved = tmuxPath ?? TmuxLocator.resolve() else {
      throw ControlModeError.tmuxNotFound
    }
    self.tmuxPath = resolved
    self.logger = Logger(subsystem: "com.agentctl.fleet", category: "gateway.\(sessionName)")

    var continuation: AsyncStream<ControlEvent>.Continuation!
    self.events = AsyncStream { continuation = $0 }
    self.eventsContinuation = continuation
  }

  /// Starts (or restarts) the attach-and-reconnect loop. Safe to call
  /// again after `stop()`.
  func start() {
    guard stopped else { return }
    stopped = false
    runLoopTask?.cancel()
    runLoopTask = Task { [weak self] in
      await self?.runLoop()
    }
  }

  /// Stops the reconnect loop and tears down the current attachment, if
  /// any. A session confirmed gone from the registry should call this —
  /// otherwise the gateway retries forever.
  func stop() {
    stopped = true
    runLoopTask?.cancel()
    runLoopTask = nil
    teardown(reason: "stopped")
  }

  /// Sends a command without waiting for its reply.
  func send(_ command: String) {
    pendingReplies.append(nil)
    write(command)
  }

  /// Sends a command and awaits its `%end`/`%error` reply, returning the
  /// buffered output lines. Throws `.commandFailed` on `%error`, or
  /// `.disconnected` if the connection drops before a reply arrives.
  func send(_ command: String) async throws -> [String] {
    try await withCheckedThrowingContinuation { continuation in
      pendingReplies.append(continuation)
      write(command)
    }
  }

  private func write(_ command: String) {
    guard let stdin else {
      failAllPending(.disconnected)
      return
    }
    let line = command + "\n"
    do {
      try stdin.write(contentsOf: Data(line.utf8))
    } catch {
      logger.error("write failed: \(error.localizedDescription, privacy: .public)")
      failAllPending(.disconnected)
    }
  }

  private func failAllPending(_ error: ControlModeError) {
    let pending = pendingReplies
    pendingReplies.removeAll()
    for continuation in pending {
      continuation?.resume(throwing: error)
    }
  }

  // MARK: - Attach / reconnect loop

  private func runLoop() async {
    var delay = Backoff.initial
    while !stopped && !Task.isCancelled {
      do {
        try await attachOnce()
        delay = Backoff.initial
      } catch {
        logger.error(
          "attach to \(self.sessionName, privacy: .public) failed: \(String(describing: error), privacy: .public)"
        )
      }
      guard !stopped, !Task.isCancelled else { return }
      logger.info("reconnecting to \(self.sessionName, privacy: .public) in \(delay)s")
      try? await Task.sleep(for: .seconds(delay))
      delay = Backoff.next(after: delay)
    }
  }

  /// Spawns the control client and suspends until it disconnects (process
  /// exit, `%exit` notification, or a read error).
  private func attachOnce() async throws {
    let stdoutPipe = Pipe()
    let stdinPipe = Pipe()
    let proc = Process()
    proc.executableURL = URL(fileURLWithPath: tmuxPath)
    proc.arguments = Constants.attachFlags + ["-t", sessionName]
    proc.standardOutput = stdoutPipe
    proc.standardInput = stdinPipe
    proc.standardError = FileHandle.nullDevice

    self.process = proc
    self.stdin = stdinPipe.fileHandleForWriting

    // proc.run() can fail synchronously (binary missing, exec permission
    // denied, ...) before any handler is wired up — check that up front
    // so the continuation is resumed exactly once, on exactly one path.
    do {
      try proc.run()
    } catch {
      throw ControlModeError.launchFailed(error.localizedDescription)
    }

    // The attach succeeded and the read loop is about to begin: this is
    // also true on every reconnect, which is exactly when consumers need
    // to know their gating state and bootstrap are stale (see
    // `ControlEvent.attached`).
    eventsContinuation.yield(.attached)

    try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
      let box = ExitBox(continuation: continuation)
      proc.terminationHandler = { [weak self] p in
        self?.logger.info("process exited: status \(p.terminationStatus)")
        box.resolve()
        Task { [weak self] in await self?.handleDisconnect() }
      }

      let readHandle = stdoutPipe.fileHandleForReading
      Task { [weak self] in
        do {
          for try await line in readHandle.bytes.lines {
            await self?.handleLine(line)
          }
        } catch {
          self?.logger.error("read loop error: \(error.localizedDescription, privacy: .public)")
        }
        box.resolve()
        await self?.handleDisconnect()
      }
    }
  }

  private func handleDisconnect() {
    teardown(reason: "disconnected")
  }

  private func teardown(reason: String) {
    logger.info(
      "tearing down attach to \(self.sessionName, privacy: .public): \(reason, privacy: .public)")
    if let process, process.isRunning {
      process.terminate()
    }
    process = nil
    try? stdin?.close()
    stdin = nil
    failAllPending(.disconnected)
  }

  private func handleLine(_ line: String) {
    guard let event = parser.consume(line: line) else { return }
    switch event {
    case .reply(_, let lines, let isError):
      resolveNextPending(lines: lines, isError: isError)
    case .exit:
      eventsContinuation.yield(event)
      teardown(reason: "%exit")
    default:
      eventsContinuation.yield(event)
    }
  }

  private func resolveNextPending(lines: [String], isError: Bool) {
    guard !pendingReplies.isEmpty else { return }
    let continuation = pendingReplies.removeFirst()
    if isError {
      continuation?.resume(throwing: ControlModeError.commandFailed(lines: lines))
    } else {
      continuation?.resume(returning: lines)
    }
  }
}

/// Coordinates "whichever of {process termination, read-loop end} happens
/// first resolves the attach continuation exactly once". Plain class guarded
/// by a lock rather than an actor: it's called from a `Process`
/// termination-handler thread and a `Task`, both of which need a
/// synchronous, non-async resolve.
private final class ExitBox: @unchecked Sendable {
  private let lock = NSLock()
  private var resolved = false
  private let continuation: CheckedContinuation<Void, Error>

  init(continuation: CheckedContinuation<Void, Error>) {
    self.continuation = continuation
  }

  func resolve() {
    lock.lock()
    defer { lock.unlock() }
    guard !resolved else { return }
    resolved = true
    continuation.resume()
  }
}
