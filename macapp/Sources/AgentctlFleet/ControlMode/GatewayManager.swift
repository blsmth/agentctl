import Foundation
import os

/// Owns one `ControlModeGateway` per live agentctl (tmux) session, keyed by
/// session name, and keeps that set in exact lockstep with the registry via
/// `sync(with:)` — drive this from `FleetStore.$snapshot`.
///
/// Never mutates the fleet itself: this only starts/stops gateway
/// *subprocesses* (control-mode attach clients), which is the read/observe
/// path every invariant in the epic spec explicitly allows.
@MainActor
final class GatewayManager {
  /// Builds one gateway for `sessionName`. Injectable so tests can make
  /// specific sessions fail to construct (mirrors `ControlModeGateway.init`
  /// throwing `.tmuxNotFound`) without depending on which `tmux` paths
  /// happen to exist on the machine running the tests.
  typealias GatewayFactory = (String) throws -> ControlModeGateway

  private(set) var gateways: [String: ControlModeGateway] = [:]
  private let makeGateway: GatewayFactory
  private let logger = Logger(subsystem: "com.agentctl.fleet", category: "gateway-manager")

  /// Invoked synchronously, on the main actor, right after a new gateway is
  /// added to `gateways` (before its `start()` has necessarily completed) —
  /// lets a listener (e.g. `TerminalPaneController`) begin consuming its
  /// `events` stream from the moment the gateway exists.
  var onGatewayAdded: ((String, ControlModeGateway) -> Void)?
  /// Invoked synchronously, on the main actor, right after a gateway is
  /// removed from `gateways` (its `stop()` is fired off separately and does
  /// not need to complete first).
  var onGatewayRemoved: ((String) -> Void)?

  init(makeGateway: @escaping GatewayFactory = { try ControlModeGateway(sessionName: $0) }) {
    self.makeGateway = makeGateway
  }

  /// Starts a gateway for every session in `sessions` that doesn't already
  /// have one, and stops + drops any gateway whose session is no longer in
  /// `sessions`. Idempotent — calling twice with the same list is a no-op
  /// the second time.
  ///
  /// A throwing `makeGateway` must not abort the sync loop or crash the
  /// app: it's logged and that session simply gets no live terminal until
  /// its gateway can be constructed on a later sync (e.g. once `tmux`
  /// becomes resolvable).
  func sync(with sessions: [String]) {
    let live = Set(sessions)

    for (session, gateway) in gateways where !live.contains(session) {
      gateways.removeValue(forKey: session)
      onGatewayRemoved?(session)
      Task { await gateway.stop() }
    }

    for session in sessions where gateways[session] == nil {
      do {
        let gateway = try makeGateway(session)
        gateways[session] = gateway
        onGatewayAdded?(session, gateway)
        Task { await gateway.start() }
      } catch {
        logger.error(
          "failed to create gateway for session \(session, privacy: .public): \(String(describing: error), privacy: .public)"
        )
      }
    }
  }

  func gateway(for session: String) -> ControlModeGateway? {
    gateways[session]
  }

  /// Stops and drops every gateway — used at app shutdown.
  func stopAll() {
    for (session, gateway) in gateways {
      onGatewayRemoved?(session)
      Task { await gateway.stop() }
    }
    gateways.removeAll()
  }
}
