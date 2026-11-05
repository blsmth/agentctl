import Foundation

/// Watches one project's `<runtime_dir>/state.json` and reports the decoded
/// snapshot whenever the containing directory changes.
///
/// One instance per live registry entry (~9 today). Read-only: this never
/// writes `state.json` or anything else in the runtime directory.
final class StateWatcher {
  /// Mirrors `herd.SnapshotStaleAfter` (Go).
  static let snapshotStaleAfter: TimeInterval = 5

  private let runtimeDir: URL
  private let onUpdate: (StateSnapshot?) -> Void
  private var watcher: DirectoryWatcher?

  init(runtimeDir: URL, onUpdate: @escaping (StateSnapshot?) -> Void) {
    self.runtimeDir = runtimeDir
    self.onUpdate = onUpdate
  }

  /// Loads once immediately, then begins watching for further changes.
  func start() {
    reload()
    let live = DirectoryWatcher(path: runtimeDir.path) { [weak self] in self?.reload() }
    live.start()
    watcher = live
  }

  func stop() {
    watcher?.stop()
    watcher = nil
  }

  func reload() {
    onUpdate(Self.loadSnapshot(runtimeDir: runtimeDir))
  }

  /// Reads and decodes `<runtimeDir>/state.json`. Returns nil if the file
  /// is missing, unreadable, or fails to parse — the caller (`FleetStore`)
  /// is responsible for keeping the last-known-good snapshot rather than
  /// blanking a session on a transient nil, mirroring the herd hub's
  /// "a briefly-wedged monitor shouldn't make a whole project's agents
  /// vanish" behavior.
  static func loadSnapshot(runtimeDir: URL) -> StateSnapshot? {
    let path = runtimeDir.appendingPathComponent("state.json")
    guard let data = try? Data(contentsOf: path) else { return nil }
    return try? JSONDecoder().decode(StateSnapshot.self, from: data)
  }
}
