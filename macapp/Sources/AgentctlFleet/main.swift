import AppKit

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
// Phase 2 has a real main window (sidebar + terminal), so it needs a Dock
// icon and menu bar like any regular app. Phase 3 turns this back into a
// menu-bar-only (`.accessory`) app once geometry/chrome ownership lands.
app.setActivationPolicy(.regular)
app.run()
