import AppKit
import SwiftTerm
import os

/// One command in a pane switch/re-gate sequence, plus which session's
/// gateway it targets and whether its reply must be awaited.
struct SwitchStep: Equatable {
  let session: String
  let command: String
  let awaitsReply: Bool
}

/// Pure builder for the ordered tmux commands one pane switch issues —
/// spec §4.1. Kept free of any gateway/AppKit dependency so the exact
/// ordered command list is directly assertable in tests, without a live
/// tmux or `NSView`.
enum SwitchSequence {
  /// Scrollback depth fed as bootstrap content on every switch.
  static let defaultCaptureLines = 2000

  /// old-pane-off (if there was one) → new-pane-on → capture, in that
  /// exact order. `oldPane == nil` covers both first-selection and
  /// re-attach — a reconnected client has no prior gating state to undo,
  /// so it's the same shape as a first selection, not a special case.
  static func steps(
    oldPane: (session: String, pane: String)?,
    newPane: (session: String, pane: String),
    captureLines: Int = defaultCaptureLines
  ) -> [SwitchStep] {
    var steps: [SwitchStep] = []
    if let oldPane, !oldPane.pane.isEmpty {
      steps.append(
        SwitchStep(
          session: oldPane.session, command: gateCommand(pane: oldPane.pane, on: false),
          awaitsReply: false))
    }
    steps.append(
      SwitchStep(
        session: newPane.session, command: gateCommand(pane: newPane.pane, on: true),
        awaitsReply: false))
    steps.append(
      SwitchStep(
        session: newPane.session, command: captureCommand(pane: newPane.pane, lines: captureLines),
        awaitsReply: true))
    return steps
  }

  /// `refresh-client -A "<pane>:on|off"` — the pane ID **must** be quoted;
  /// unquoted, tmux throws a parse error (verified against tmux 3.6a).
  static func gateCommand(pane: String, on: Bool) -> String {
    "refresh-client -A \"\(pane):\(on ? "on" : "off")\""
  }

  /// `capture-pane -peqJ -t <pane> -S -<lines>`: print, include escape
  /// sequences, quiet, rejoin wrapped lines, from `lines` back.
  static func captureCommand(pane: String, lines: Int) -> String {
    "capture-pane -peqJ -t \(pane) -S -\(lines)"
  }
}

/// Whether output for the pane currently being switched to is being fed
/// straight to the view or held back — see `PaneOutputBuffer`.
enum PaneStreamState: Equatable {
  case bootstrapping
  case streaming
}

/// Buffers `%output` bytes for the pane a switch is bootstrapping, so the
/// race between spec §4.1's step 4 (`capture-pane`) and step 5 (resume
/// streaming) can't drop or misorder output that arrives while the capture
/// is still in flight. Pure and AppKit/gateway-free, so the exact flush
/// behavior is directly testable.
struct PaneOutputBuffer: Equatable {
  private(set) var state: PaneStreamState = .streaming
  private(set) var buffered: [[UInt8]] = []

  /// Starts (or restarts) bootstrapping, discarding anything left over
  /// from a previous switch — called the moment a switch's gate-on step
  /// is sent, before the `capture-pane` reply arrives.
  mutating func beginBootstrap() {
    state = .bootstrapping
    buffered = []
  }

  /// Records `bytes` while bootstrapping; a no-op once streaming, since
  /// callers feed directly to the view in that state instead.
  mutating func recordIfBootstrapping(_ bytes: [UInt8]) {
    guard state == .bootstrapping else { return }
    buffered.append(bytes)
  }

  /// Ends the bootstrap and returns whatever was buffered, in arrival
  /// order, to be fed to the view *after* the capture content — never
  /// dropped, never interleaved ahead of it. Switches to `.streaming` so
  /// subsequent output is fed directly rather than buffered.
  mutating func endBootstrapAndDrain() -> [[UInt8]] {
    state = .streaming
    let drained = buffered
    buffered = []
    return drained
  }
}

/// Owns the single `TerminalView` for the whole app — SwiftTerm #658 forces
/// exactly one instance (6 streaming agent TUIs sustain 75-82% of one core
/// at idle with no pause-rendering API) — and drives every pane switch,
/// output gate, and keystroke through it.
///
/// Never mutates the fleet beyond `refresh-client -A` (client-local output
/// gating), `capture-pane` (read-only), `send-keys -H`, and read-only
/// `list-windows`. No `resize-window`, `select-window`, `kill-window`, or
/// `set-option` — see the epic spec's invariants.
@MainActor
final class TerminalPaneController: NSObject {
  let view: TerminalView

  private let gatewayManager: GatewayManager
  private let logger = Logger(subsystem: "com.agentctl.fleet", category: "terminal-pane")

  /// The pane currently shown, if any.
  private var selection: (session: String, pane: String)?
  private var outputBuffer = PaneOutputBuffer()

  /// Panes already told their gate state, per session — reset to empty on
  /// `.attached`, since a reconnected client starts with no gating state
  /// at all (every pane defaults back to "on").
  private var knownPanes: [String: Set<String>] = [:]
  /// Cached `%layout-change`/`list-windows` results: session -> window ID
  /// -> panes in visual order, so most pane lookups need no round trip.
  private var layoutsBySessionWindow: [String: [String: [PaneLayout]]] = [:]
  /// The selected pane's column count, so the view can be refit if its
  /// frame changes (e.g. the sidebar is resized).
  private var selectedPaneCols: Int?

  private var eventTasks: [String: Task<Void, Never>] = [:]

  private static let listWindowsCommand = "list-windows -F \"#{window_id} #{window_layout}\""

  init(gatewayManager: GatewayManager) {
    self.gatewayManager = gatewayManager
    self.view = TerminalView(frame: .zero, font: nil)
    super.init()

    view.terminalDelegate = self
    view.postsFrameChangedNotifications = true
    NotificationCenter.default.addObserver(
      self, selector: #selector(viewFrameDidChange), name: NSView.frameDidChangeNotification,
      object: view)

    gatewayManager.onGatewayAdded = { [weak self] session, gateway in
      self?.attachGateway(gateway, session: session)
    }
    gatewayManager.onGatewayRemoved = { [weak self] session in
      self?.detachGateway(session: session)
    }
    for (session, gateway) in gatewayManager.gateways {
      attachGateway(gateway, session: session)
    }

    clearSelection(reason: "Select an agent to view its terminal.")
  }

  // MARK: - Selection

  /// Switches the single `TerminalView` to `agent`'s pane in `session`.
  func select(agent: AgentSnapshot, session: String) async {
    guard let pane = await resolvePane(for: agent, session: session) else {
      logger.error(
        "could not resolve a pane for window \(agent.windowID, privacy: .public) in \(session, privacy: .public)"
      )
      return
    }
    selectedPaneCols =
      layoutsBySessionWindow[session]?[agent.windowID]?.first(where: { $0.id == pane })?.cols
    performSwitch(from: selection, to: session, newPane: pane)
  }

  /// Clears the view to an explicit placeholder state — used both before
  /// any selection is made and when the selected agent disappears from
  /// the fleet snapshot (its window closed). Never auto-selects a
  /// neighbour; the caller decides whether to re-select anything.
  func clearSelection(reason: String) {
    selection = nil
    selectedPaneCols = nil
    _ = outputBuffer.endBootstrapAndDrain()
    view.getTerminal().resetToInitialState()
    view.feed(text: "\r\n  \(reason)\r\n")
  }

  private func performSwitch(
    from previous: (session: String, pane: String)?, to newSession: String, newPane: String
  ) {
    selection = (newSession, newPane)
    outputBuffer.beginBootstrap()

    let steps = SwitchSequence.steps(
      oldPane: previous, newPane: (session: newSession, pane: newPane))

    Task { [weak self] in
      guard let self else { return }
      var capturedLines: [String] = []
      for step in steps {
        guard let gateway = self.gatewayManager.gateway(for: step.session) else { continue }
        if step.awaitsReply {
          do {
            capturedLines = try await gateway.send(step.command)
          } catch {
            self.logger.error(
              "capture-pane failed for \(newPane, privacy: .public): \(String(describing: error), privacy: .public)"
            )
          }
        } else {
          await Self.fireAndForget(step.command, on: gateway)
        }
      }
      self.applyCapture(capturedLines, forSelection: (newSession, newPane))
    }
  }

  private func applyCapture(_ lines: [String], forSelection target: (session: String, pane: String))
  {
    // A newer switch superseded this one while the capture was in
    // flight — discard rather than clobbering whatever's now selected.
    guard let selection, selection.session == target.session, selection.pane == target.pane else {
      return
    }

    view.getTerminal().resetToInitialState()
    if !lines.isEmpty {
      view.feed(text: lines.joined(separator: "\r\n") + "\r\n")
    }
    for bytes in outputBuffer.endBootstrapAndDrain() {
      view.feed(byteArray: bytes[...])
    }

    if let cols = selectedPaneCols {
      refitFont(cols: cols)
    }
  }

  // MARK: - Gateway lifecycle

  private func attachGateway(_ gateway: ControlModeGateway, session: String) {
    eventTasks[session]?.cancel()
    eventTasks[session] = Task { [weak self] in
      for await event in gateway.events {
        self?.handle(event, session: session)
      }
    }
  }

  private func detachGateway(session: String) {
    eventTasks[session]?.cancel()
    eventTasks.removeValue(forKey: session)
    knownPanes.removeValue(forKey: session)
    layoutsBySessionWindow.removeValue(forKey: session)
  }

  private func handle(_ event: ControlEvent, session: String) {
    switch event {
    case .output(let pane, let bytes):
      handleOutput(session: session, pane: pane, bytes: bytes)
    case .layoutChange(let window, let layout, _, _):
      let panes = parseLayout(layout)
      layoutsBySessionWindow[session, default: [:]][window] = panes
      Task { [weak self] in await self?.ensureGated(paneIDs: panes.map(\.id), session: session) }
    case .attached:
      handleAttached(session: session)
    case .exit:
      // The gateway's own reconnect loop handles retrying; there's
      // nothing to do here beyond letting the view go stale until
      // `.attached` fires again.
      break
    case .windowAdd, .windowClose, .windowRenamed, .sessionChanged, .pause, .unpause, .reply:
      break
    }
  }

  private func handleOutput(session: String, pane: String, bytes: [UInt8]) {
    guard let selection, selection.session == session, selection.pane == pane else { return }
    switch outputBuffer.state {
    case .bootstrapping:
      outputBuffer.recordIfBootstrapping(bytes)
    case .streaming:
      view.feed(byteArray: bytes[...])
    }
  }

  private func handleAttached(session: String) {
    // A reconnected control client has no gating state at all — every
    // pane defaults back to "on" until told otherwise.
    knownPanes[session] = []
    Task { [weak self] in await self?.discoverAndGateSession(session) }

    // Same code path as a normal switch, not a near-copy of it: a
    // reconnected client has no prior gating state, exactly like a first
    // selection (`oldPane == nil`).
    if let selection, selection.session == session {
      performSwitch(from: nil, to: session, newPane: selection.pane)
    }
  }

  // MARK: - Pane discovery & gating

  private func resolvePane(for agent: AgentSnapshot, session: String) async -> String? {
    if let panes = layoutsBySessionWindow[session]?[agent.windowID] {
      return identifyAgentPane(panes: panes, chromeBarPane: nil, mainPane: agent.mainPane)
    }
    guard let gateway = gatewayManager.gateway(for: session) else { return nil }
    guard let lines = try? await gateway.send(Self.listWindowsCommand) else { return nil }
    cacheLayouts(from: lines, session: session)
    guard let panes = layoutsBySessionWindow[session]?[agent.windowID] else { return nil }
    return identifyAgentPane(panes: panes, chromeBarPane: nil, mainPane: agent.mainPane)
  }

  private func discoverAndGateSession(_ session: String) async {
    guard let gateway = gatewayManager.gateway(for: session) else { return }
    guard let lines = try? await gateway.send(Self.listWindowsCommand) else { return }
    cacheLayouts(from: lines, session: session)
    let allPanes = (layoutsBySessionWindow[session] ?? [:]).values.flatMap { $0.map(\.id) }
    await ensureGated(paneIDs: allPanes, session: session)
  }

  /// Sends an explicit gate command for every pane in `paneIDs` not
  /// already known for `session` — every pane defaults to "on" the moment
  /// a client attaches, so anything but the current selection must be
  /// told to turn off explicitly, including chrome-bar panes: the app
  /// draws its own chrome natively in Phase 3, so those bytes should
  /// never cross the pipe at all.
  private func ensureGated(paneIDs: [String], session: String) async {
    guard let gateway = gatewayManager.gateway(for: session) else { return }
    var known = knownPanes[session] ?? []
    for pane in paneIDs where !known.contains(pane) {
      known.insert(pane)
      let desiredOn = selection?.session == session && selection?.pane == pane
      await Self.fireAndForget(SwitchSequence.gateCommand(pane: pane, on: desiredOn), on: gateway)
    }
    knownPanes[session] = known
  }

  private func cacheLayouts(from lines: [String], session: String) {
    for line in lines {
      guard let spaceIndex = line.firstIndex(of: " ") else { continue }
      let window = String(line[line.startIndex..<spaceIndex])
      let layout = String(line[line.index(after: spaceIndex)...])
      layoutsBySessionWindow[session, default: [:]][window] = parseLayout(layout)
    }
  }

  // MARK: - Font fit

  @objc private func viewFrameDidChange() {
    guard let cols = selectedPaneCols else { return }
    refitFont(cols: cols)
  }

  private func refitFont(cols: Int) {
    let width = view.bounds.width
    guard width > 0 else { return }
    let pointSize = FontFitter.pointSize(forColumns: cols, viewWidth: width)
    let family = view.font.fontName
    view.font =
      NSFont(name: family, size: pointSize)
      ?? .monospacedSystemFont(ofSize: pointSize, weight: .regular)
  }

  // MARK: - Input

  private func sendKeys(_ bytes: [UInt8]) {
    guard let selection else { return }
    let commands = KeyEncoder.encodeKeys(paneID: selection.pane, bytes: bytes[...])
    guard !commands.isEmpty, let gateway = gatewayManager.gateway(for: selection.session) else {
      return
    }
    Task {
      for command in commands {
        await Self.fireAndForget(command, on: gateway)
      }
    }
  }

  /// Disambiguates `ControlModeGateway`'s two overloads of `send` at the
  /// call site: Swift's overload resolution doesn't filter by `throws`/
  /// `async` effects the way it filters by parameter types, so a bare
  /// `await gateway.send(x)` whose result is discarded silently resolves
  /// to the *throwing* reply-awaiting overload (and then requires `try`)
  /// rather than the true fire-and-forget one. An explicit `Void`-typed
  /// binding forces the non-throwing overload instead.
  private static func fireAndForget(_ command: String, on gateway: ControlModeGateway) async {
    let _: Void = await gateway.send(command)
  }
}

// MARK: - TerminalViewDelegate

// `TerminalViewDelegate` isn't `@MainActor` in SwiftTerm 1.20.0, but this
// controller is — every requirement is `nonisolated` and forwards onto the
// main actor, rather than making the delegate methods themselves
// MainActor-isolated (which would fail to satisfy the protocol).
extension TerminalPaneController: TerminalViewDelegate {
  nonisolated func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
    // Geometry ownership is Phase 3 only — deliberately a no-op. Do not
    // send `refresh-client -C`.
  }

  nonisolated func setTerminalTitle(source: TerminalView, title: String) {}

  nonisolated func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}

  /// `TerminalViewDelegate.send` — user input exits here. Always routed
  /// through `KeyEncoder`'s `-H` hex path: tmux 3.5+ turns a bare
  /// `send-keys 0xNN` into the literal text `"0xNN"` once a pane has
  /// `modifyOtherKeys` on, and Claude Code's TUI enables it.
  nonisolated func send(source: TerminalView, data: ArraySlice<UInt8>) {
    let bytes = Array(data)
    Task { @MainActor in
      self.sendKeys(bytes)
    }
  }

  nonisolated func scrolled(source: TerminalView, position: Double) {}

  nonisolated func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
}
