import AppKit
import Combine

/// `NSOutlineView` sidebar — session groups nested with their agents —
/// bound to `FleetStore.$snapshot`. Source-list styled per macOS
/// convention.
///
/// `snapshot` republishes on every `state.json` write, which is frequent;
/// see `SidebarTree`/`SidebarNode` for how row identity survives that so
/// expansion state and selection aren't dropped on every tick.
@MainActor
final class SidebarViewController: NSViewController {
  /// Invoked when the user selects an agent row (never for a group row).
  var onSelectAgent: ((AgentSnapshot, _ session: String) -> Void)?
  /// Invoked when the previously selected agent disappears from the
  /// snapshot (its window closed). The sidebar never auto-selects a
  /// neighbour — that decision belongs to whoever owns the terminal pane.
  var onSelectionGone: (() -> Void)?

  private let fleetStore: FleetStore
  private let outlineView = NSOutlineView()
  private var tree = SidebarTree()
  private var selectedID: SidebarNodeID?
  private var hasExpandedInitialGroups = false
  private var cancellable: AnyCancellable?

  init(fleetStore: FleetStore) {
    self.fleetStore = fleetStore
    super.init(nibName: nil, bundle: nil)
  }

  @available(*, unavailable)
  required init?(coder: NSCoder) {
    fatalError("init(coder:) is not supported")
  }

  override func loadView() {
    let scrollView = NSScrollView()
    scrollView.hasVerticalScroller = true
    scrollView.drawsBackground = false

    outlineView.headerView = nil
    outlineView.style = .sourceList
    outlineView.dataSource = self
    outlineView.delegate = self
    outlineView.floatsGroupRows = false

    let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("fleet"))
    column.title = "Fleet"
    outlineView.addTableColumn(column)
    outlineView.outlineTableColumn = column

    scrollView.documentView = outlineView
    view = scrollView
  }

  override func viewDidLoad() {
    super.viewDidLoad()
    cancellable = fleetStore.$snapshot
      .receive(on: DispatchQueue.main)
      .sink { [weak self] snapshot in
        self?.apply(snapshot)
      }
  }

  private func apply(_ snapshot: FleetSnapshot) {
    tree = tree.reconciled(with: snapshot)
    outlineView.reloadData()

    if !hasExpandedInitialGroups {
      hasExpandedInitialGroups = true
      for group in tree.groups {
        outlineView.expandItem(group)
      }
    }

    guard let selectedID else { return }
    if tree.contains(selectedID) {
      reselect(selectedID)
    } else if case .agent = selectedID {
      self.selectedID = nil
      outlineView.deselectAll(nil)
      onSelectionGone?()
    }
  }

  private func reselect(_ id: SidebarNodeID) {
    guard let node = tree.node(for: id) else { return }
    let row = outlineView.row(forItem: node)
    guard row >= 0 else { return }
    outlineView.selectRowIndexes(IndexSet(integer: row), byExtendingSelection: false)
  }

  /// The session name owning `node` (an agent node) — found by walking the
  /// small group list rather than storing a back-pointer, since the fleet
  /// tops out around ten groups.
  private func session(owning node: SidebarNode) -> String? {
    for group in tree.groups where group.children.contains(where: { $0 === node }) {
      guard case .group(let session) = group.id else { return nil }
      return session
    }
    return nil
  }
}

// MARK: - NSOutlineViewDataSource

extension SidebarViewController: NSOutlineViewDataSource {
  func outlineView(_ outlineView: NSOutlineView, numberOfChildrenOfItem item: Any?) -> Int {
    guard let item else { return tree.groups.count }
    guard let node = item as? SidebarNode else { return 0 }
    return node.children.count
  }

  func outlineView(_ outlineView: NSOutlineView, child index: Int, ofItem item: Any?) -> Any {
    guard let item else { return tree.groups[index] }
    guard let node = item as? SidebarNode else {
      preconditionFailure("outline view item was not a SidebarNode")
    }
    return node.children[index]
  }

  func outlineView(_ outlineView: NSOutlineView, isItemExpandable item: Any) -> Bool {
    guard let node = item as? SidebarNode else { return false }
    return !node.children.isEmpty
  }
}

// MARK: - NSOutlineViewDelegate

extension SidebarViewController: NSOutlineViewDelegate {
  func outlineView(_ outlineView: NSOutlineView, isGroupItem item: Any) -> Bool {
    (item as? SidebarNode)?.group != nil
  }

  func outlineView(_ outlineView: NSOutlineView, shouldSelectItem item: Any) -> Bool {
    (item as? SidebarNode)?.agent != nil
  }

  func outlineView(_ outlineView: NSOutlineView, viewFor tableColumn: NSTableColumn?, item: Any)
    -> NSView?
  {
    guard let node = item as? SidebarNode else { return nil }
    if let group = node.group {
      let cell = SidebarGroupCellView()
      cell.configure(group: group)
      return cell
    }
    if let agent = node.agent {
      let cell = SidebarAgentCellView()
      cell.configure(agent: agent)
      return cell
    }
    return nil
  }

  func outlineViewSelectionDidChange(_ notification: Notification) {
    let row = outlineView.selectedRow
    guard row >= 0, let node = outlineView.item(atRow: row) as? SidebarNode, let agent = node.agent,
      let session = session(owning: node)
    else { return }
    selectedID = node.id
    onSelectAgent?(agent, session)
  }
}

// MARK: - Row cells

private final class SidebarGroupCellView: NSTableCellView {
  private let glyphLabel = NSTextField(labelWithString: "")
  private let titleLabel = NSTextField(labelWithString: "")
  private let subtitleLabel = NSTextField(labelWithString: "")

  override init(frame frameRect: NSRect) {
    super.init(frame: frameRect)
    setUp()
  }

  @available(*, unavailable)
  required init?(coder: NSCoder) {
    fatalError("init(coder:) is not supported")
  }

  private func setUp() {
    titleLabel.font = .systemFont(ofSize: NSFont.systemFontSize, weight: .semibold)
    titleLabel.lineBreakMode = .byTruncatingTail
    subtitleLabel.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
    subtitleLabel.textColor = .secondaryLabelColor
    subtitleLabel.lineBreakMode = .byTruncatingTail

    let textStack = NSStackView(views: [titleLabel, subtitleLabel])
    textStack.orientation = .vertical
    textStack.alignment = .leading
    textStack.spacing = 1

    let row = NSStackView(views: [glyphLabel, textStack])
    row.orientation = .horizontal
    row.alignment = .centerY
    row.spacing = 6
    row.translatesAutoresizingMaskIntoConstraints = false

    addSubview(row)
    NSLayoutConstraint.activate([
      row.leadingAnchor.constraint(equalTo: leadingAnchor),
      row.trailingAnchor.constraint(lessThanOrEqualTo: trailingAnchor),
      row.centerYAnchor.constraint(equalTo: centerYAnchor),
    ])
  }

  func configure(group: SessionGroup) {
    glyphLabel.stringValue = SidebarGlyphs.rollupGlyph(for: group.rollup)
    titleLabel.stringValue = "\(group.project) (\(group.rows.count))"
    subtitleLabel.stringValue = group.stale ? "\(group.session) ⚠︎ stale" : group.session
    subtitleLabel.textColor = group.stale ? .systemOrange : .secondaryLabelColor
    alphaValue = group.stale ? 0.7 : 1
  }
}

private final class SidebarAgentCellView: NSTableCellView {
  private let glyphLabel = NSTextField(labelWithString: "")
  private let titleLabel = NSTextField(labelWithString: "")
  private let detailLabel = NSTextField(labelWithString: "")

  override init(frame frameRect: NSRect) {
    super.init(frame: frameRect)
    setUp()
  }

  @available(*, unavailable)
  required init?(coder: NSCoder) {
    fatalError("init(coder:) is not supported")
  }

  private func setUp() {
    titleLabel.lineBreakMode = .byTruncatingTail
    detailLabel.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
    detailLabel.textColor = .secondaryLabelColor
    detailLabel.lineBreakMode = .byTruncatingTail

    let textStack = NSStackView(views: [titleLabel, detailLabel])
    textStack.orientation = .vertical
    textStack.alignment = .leading
    textStack.spacing = 1

    let row = NSStackView(views: [glyphLabel, textStack])
    row.orientation = .horizontal
    row.alignment = .centerY
    row.spacing = 6
    row.translatesAutoresizingMaskIntoConstraints = false

    addSubview(row)
    NSLayoutConstraint.activate([
      row.leadingAnchor.constraint(equalTo: leadingAnchor, constant: 12),
      row.trailingAnchor.constraint(lessThanOrEqualTo: trailingAnchor),
      row.centerYAnchor.constraint(equalTo: centerYAnchor),
    ])
  }

  func configure(agent: AgentSnapshot) {
    glyphLabel.stringValue = SidebarGlyphs.agentGlyph(for: agent)
    titleLabel.stringValue = agent.ticket != 0 ? "\(agent.name)  #\(agent.ticket)" : agent.name

    var detail = ""
    if agent.contextPct != 0 { detail += "\(agent.contextPct)%  " }
    detail += agent.lastText
    detailLabel.stringValue = detail
  }
}
