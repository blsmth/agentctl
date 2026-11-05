import Foundation

/// Watches a directory for changes and invokes a callback on the main queue.
///
/// Both the registry directory and each project's runtime directory are
/// written to exclusively via tmp-then-rename (never an in-place edit), and
/// a rename that lands a new file into a directory updates *that
/// directory's* mtime — so watching the containing directory, rather than
/// the individual file, reliably observes every update even though the
/// file's own inode changes on every write.
///
/// This wraps `DispatchSourceFileSystemObject` rather than the lower-level
/// FSEvents C API: for a handful of directories (~10 registry entries, ~9
/// runtime dirs) a `DispatchSource` per directory is simpler to reason about
/// and just as correct, at the cost of one open file descriptor per watched
/// directory — negligible at this scale.
final class DirectoryWatcher {
  private let path: String
  private let onChange: () -> Void
  private var source: DispatchSourceFileSystemObject?
  private var descriptor: CInt = -1

  init(path: String, onChange: @escaping () -> Void) {
    self.path = path
    self.onChange = onChange
  }

  /// Begins watching. Safe to call once; a directory that doesn't exist
  /// yet simply isn't watched (callers should still do an initial load —
  /// the directory may be created later, but Phase 1's fleet directories
  /// are expected to exist before the app launches).
  func start() {
    guard source == nil else { return }
    let fd = open(path, O_EVTONLY)
    guard fd >= 0 else { return }
    descriptor = fd

    let src = DispatchSource.makeFileSystemObjectSource(
      fileDescriptor: fd,
      eventMask: [.write, .rename, .delete, .extend, .attrib],
      queue: .main
    )
    src.setEventHandler { [weak self] in
      self?.onChange()
    }
    src.setCancelHandler { [weak self] in
      guard let self, self.descriptor >= 0 else { return }
      close(self.descriptor)
      self.descriptor = -1
    }
    src.resume()
    source = src
  }

  func stop() {
    source?.cancel()
    source = nil
  }

  deinit {
    stop()
  }
}
