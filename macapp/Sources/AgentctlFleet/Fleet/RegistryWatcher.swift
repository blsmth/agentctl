import Foundation

/// Watches `~/.config/agentctl/registry/*.json` and reports the set of live
/// (non-stale) sessions whenever the directory changes.
///
/// Never writes to the registry — this is a read-only view. Staleness
/// mirrors `registry.StaleAfter` (Go): an entry whose `last_seen` is more
/// than 30s old is dropped from the reported set (not deleted from disk;
/// that's the monitor's job via `registry.Prune`, and this app must never
/// mutate what it's observing).
final class RegistryWatcher {
  /// Mirrors `registry.StaleAfter` (Go).
  static let staleAfter: TimeInterval = 30

  private let directory: URL
  private let onUpdate: ([RegistryEntry]) -> Void
  private var watcher: DirectoryWatcher?

  init(
    directory: URL = RegistryWatcher.defaultDirectory, onUpdate: @escaping ([RegistryEntry]) -> Void
  ) {
    self.directory = directory
    self.onUpdate = onUpdate
  }

  static var defaultDirectory: URL {
    FileManager.default.homeDirectoryForCurrentUser
      .appendingPathComponent(".config/agentctl/registry", isDirectory: true)
  }

  /// Loads once immediately, then begins watching for further changes.
  func start() {
    reload()
    let live = DirectoryWatcher(path: directory.path) { [weak self] in self?.reload() }
    live.start()
    watcher = live
  }

  func stop() {
    watcher?.stop()
    watcher = nil
  }

  func reload() {
    onUpdate(Self.loadEntries(from: directory))
  }

  /// Reads and decodes every `*.json` file in `directory`, skipping any
  /// that's unreadable or corrupt (a half-written or torn file must not
  /// blank the whole fleet view — mirrors `registry.List`), and filters
  /// out entries whose heartbeat has gone stale. Pure aside from the
  /// filesystem read, so it's directly testable against a temp directory.
  static func loadEntries(
    from directory: URL,
    now: Date = Date(),
    staleAfter: TimeInterval = RegistryWatcher.staleAfter
  ) -> [RegistryEntry] {
    let fm = FileManager.default
    guard let files = try? fm.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
    else {
      return []
    }

    let decoder = JSONDecoder()
    var entries: [RegistryEntry] = []
    for file in files where file.pathExtension == "json" {
      guard let data = try? Data(contentsOf: file),
        let entry = try? decoder.decode(RegistryEntry.self, from: data)
      else { continue }
      let age = now.timeIntervalSince1970 - Double(entry.lastSeen)
      guard age <= staleAfter else { continue }
      entries.append(entry)
    }
    return entries.sorted { $0.sessionName < $1.sessionName }
  }
}
