import AppKit
import Combine
import os

/// Phase 2: sidebar + one live interactive terminal. Builds the main
/// window, starts the fleet watchers, and keeps `GatewayManager`'s live
/// gateways in lockstep with the registry via `FleetStore.$snapshot`.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
  private let logger = Logger(subsystem: "com.agentctl.fleet", category: "app")
  private var fleetStore: FleetStore?
  private var gatewayManager: GatewayManager?
  private var mainWindowController: MainWindowController?
  private var sessionsCancellable: AnyCancellable?

  func applicationDidFinishLaunching(_ notification: Notification) {
    logger.info("AgentctlFleet launching (Phase 2: sidebar + one live terminal)")

    buildMainMenu()

    let store = FleetStore()
    let gateways = GatewayManager()
    sessionsCancellable = store.$snapshot
      .map { snapshot in snapshot.sessions.map(\.session) }
      .removeDuplicates()
      .sink { sessions in gateways.sync(with: sessions) }

    let controller = MainWindowController(fleetStore: store, gatewayManager: gateways)
    controller.showWindow(nil)
    controller.window?.makeKeyAndOrderFront(nil)
    NSApp.activate(ignoringOtherApps: true)

    store.start()

    fleetStore = store
    gatewayManager = gateways
    mainWindowController = controller
  }

  func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    // Phase 3 makes this a menu-bar app that must outlive any window it
    // opens — keep returning false even though Phase 2 always has one.
    false
  }

  func applicationWillTerminate(_ notification: Notification) {
    gatewayManager?.stopAll()
    fleetStore?.stop()
  }

  /// A plain `swift build` executable has no menu bar at all otherwise —
  /// without this, ⌘Q doesn't work and there's no way to Copy/Paste in the
  /// terminal view via the menu.
  private func buildMainMenu() {
    let mainMenu = NSMenu()

    let appMenuItem = NSMenuItem()
    let appMenu = NSMenu()
    let appName = ProcessInfo.processInfo.processName
    appMenu.addItem(
      withTitle: "Quit \(appName)", action: #selector(NSApplication.terminate(_:)),
      keyEquivalent: "q")
    appMenuItem.submenu = appMenu
    mainMenu.addItem(appMenuItem)

    let editMenuItem = NSMenuItem()
    let editMenu = NSMenu(title: "Edit")
    editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
    editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
    editMenuItem.submenu = editMenu
    mainMenu.addItem(editMenuItem)

    NSApp.mainMenu = mainMenu
  }
}
