import AppKit

/// Root window: an `NSSplitViewController` with the fleet sidebar on the
/// left and the single live `TerminalView` on the right.
@MainActor
final class MainWindowController: NSWindowController {
  private let terminalPane: TerminalPaneController
  private let sidebar: SidebarViewController

  init(fleetStore: FleetStore, gatewayManager: GatewayManager) {
    let terminalPane = TerminalPaneController(gatewayManager: gatewayManager)
    let sidebar = SidebarViewController(fleetStore: fleetStore)
    self.terminalPane = terminalPane
    self.sidebar = sidebar

    let splitViewController = NSSplitViewController()

    let sidebarItem = NSSplitViewItem(sidebarWithViewController: sidebar)
    sidebarItem.minimumThickness = 220
    sidebarItem.maximumThickness = 420
    splitViewController.addSplitViewItem(sidebarItem)

    let terminalViewController = NSViewController()
    terminalViewController.view = terminalPane.view
    let terminalItem = NSSplitViewItem(viewController: terminalViewController)
    splitViewController.addSplitViewItem(terminalItem)

    let window = NSWindow(contentViewController: splitViewController)
    window.setContentSize(NSSize(width: 1100, height: 700))
    window.title = "Agentctl Fleet"
    window.center()

    super.init(window: window)

    sidebar.onSelectAgent = { [weak terminalPane] agent, session in
      Task { await terminalPane?.select(agent: agent, session: session) }
    }
    sidebar.onSelectionGone = { [weak terminalPane] in
      terminalPane?.clearSelection(reason: "This agent's window has closed.")
    }
  }

  @available(*, unavailable)
  required init?(coder: NSCoder) {
    fatalError("init(coder:) is not supported")
  }
}
