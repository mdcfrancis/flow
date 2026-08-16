// FlowMenuBar — a macOS status-bar item for a running HDM console.
//
// It polls the edge console's read-only /status and /perf endpoints and renders
// the result as a menu-bar glyph plus a detail dropdown, with a click-through
// that opens the viewer in the default browser. It is a VIEWER ONLY: every
// request is a GET, so the helper can never perturb the live system it watches
// (the console's mutating routes — /build, /focus, /feedback — are never called).
//
// Build: menubar/build.sh   Run: menubar/build/FlowMenuBar.app
import AppKit

// pollInterval is how often the helper refreshes. The evolution heartbeat is 15s,
// so 5s keeps the readout live without polling far faster than the state changes.
let pollInterval: TimeInterval = 5.0

let defaultConsole = "http://127.0.0.1:8420"

// consoleURL honours FLOW_CONSOLE so a helper can watch a console on a non-default
// port (the address main.go binds is 127.0.0.1:8420).
var consoleURL: String {
    if let v = ProcessInfo.processInfo.environment["FLOW_CONSOLE"], !v.isEmpty { return v }
    return defaultConsole
}

/// Stats is the flattened readout the menu renders — the subset of /status and
/// /perf worth surfacing at a glance.
struct Stats {
    var online = false
    var phase = ""
    var reason = ""
    var target = ""
    var tick = 0
    var nextFrameTick = 0
    var cellCounts: [(String, Int)] = []
    var totalCells = 0
    var liveCells = 0
    var tokens = 0
    var model = ""
    var memoHitRate = 0.0
}

/// fetchJSON performs one short-timeout GET and decodes a JSON object. A failure
/// (server down, malformed body) yields nil rather than throwing — "offline" is a
/// normal, expected state for this helper, not an error worth surfacing loudly.
func fetchJSON(_ path: String, timeout: TimeInterval = 3.0) -> [String: Any]? {
    guard let url = URL(string: consoleURL + path) else { return nil }
    var req = URLRequest(url: url)
    req.timeoutInterval = timeout
    req.cachePolicy = .reloadIgnoringLocalCacheData

    var result: [String: Any]?
    let sem = DispatchSemaphore(value: 0)
    URLSession.shared.dataTask(with: req) { data, _, _ in
        defer { sem.signal() }
        guard let data = data,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return }
        result = obj
    }.resume()
    // Wait slightly longer than the request timeout so the semaphore can never
    // outlive the task it is guarding.
    _ = sem.wait(timeout: .now() + timeout + 1.0)
    return result
}

func loadStats() -> Stats {
    var s = Stats()
    guard let st = fetchJSON("/status") else { return s }
    s.online = true
    s.phase = st["phase"] as? String ?? ""
    s.reason = st["reason"] as? String ?? ""
    s.target = st["target"] as? String ?? ""
    s.tick = st["tick"] as? Int ?? 0
    s.nextFrameTick = st["nextFrameTick"] as? Int ?? 0

    if let cells = st["cells"] as? [[String: Any]] {
        s.totalCells = cells.count
        // Count by state, keeping a stable, meaningful order rather than whatever
        // order a dictionary iterates in.
        var counts: [String: Int] = [:]
        for c in cells {
            let state = c["state"] as? String ?? "unknown"
            counts[state, default: 0] += 1
        }
        s.liveCells = counts["live"] ?? 0
        let preferred = ["live", "mutating", "held", "stalled", "failed"]
        var ordered: [(String, Int)] = []
        for k in preferred where counts[k] != nil {
            ordered.append((k, counts[k]!))
            counts.removeValue(forKey: k)
        }
        for k in counts.keys.sorted() { ordered.append((k, counts[k]!)) }
        s.cellCounts = ordered
    }

    if let perf = fetchJSON("/perf"), let models = perf["models"] as? [String: Any] {
        s.tokens = models["totalTokens"] as? Int ?? 0
        if let b = models["bindings"] as? [String: String] {
            // Every logical type usually binds the same base model; show the single
            // name when they agree, and mark it "mixed" when a per-type override
            // has actually split them.
            let distinct = Set(b.values)
            s.model = distinct.count == 1 ? (distinct.first ?? "") : "mixed (\(distinct.count))"
        }
        if let perf2 = perf["memo"] as? [String: Any] {
            s.memoHitRate = perf2["hitRate"] as? Double ?? 0
        }
    }
    return s
}

/// abbreviate renders a token count compactly (128000 -> "128k") so the menu
/// stays narrow.
func abbreviate(_ n: Int) -> String {
    if n >= 1_000_000 { return String(format: "%.1fM", Double(n) / 1_000_000) }
    if n >= 1_000 { return String(format: "%.0fk", Double(n) / 1_000) }
    return "\(n)"
}

/// shortURN trims the long `urn:hdm:apps:foo:bar` form to its last two segments,
/// which is what identifies the cell to a reader.
func shortURN(_ urn: String) -> String {
    let parts = urn.split(separator: ":")
    guard parts.count > 2 else { return urn }
    return parts.suffix(2).joined(separator: ":")
}

class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    let menu = NSMenu()
    var timer: Timer?
    var stats = Stats()

    func applicationDidFinishLaunching(_ n: Notification) {
        menu.delegate = self
        statusItem.menu = menu
        if let btn = statusItem.button {
            btn.image = NSImage(systemSymbolName: "circle.hexagongrid.fill",
                                accessibilityDescription: "flow")
            btn.imagePosition = .imageLeading
        }
        refresh()
        timer = Timer.scheduledTimer(withTimeInterval: pollInterval, repeats: true) { [weak self] _ in
            self?.refresh()
        }
    }

    /// refresh polls off the main thread and applies the result back on it —
    /// a blocking fetch on the main thread would stall the menu bar for the
    /// whole timeout every time the console is down.
    func refresh() {
        DispatchQueue.global(qos: .utility).async { [weak self] in
            let s = loadStats()
            DispatchQueue.main.async {
                self?.stats = s
                self?.render()
            }
        }
    }

    func render() {
        if let btn = statusItem.button {
            btn.title = stats.online ? " \(stats.liveCells)/\(stats.totalCells)" : " –"
            // Dim the glyph while the console is unreachable, so "not running" is
            // legible at a glance without opening the menu.
            btn.appearsDisabled = !stats.online
        }
        rebuildMenu()
    }

    func item(_ title: String) -> NSMenuItem {
        let i = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        i.isEnabled = false
        return i
    }

    func rebuildMenu() {
        menu.removeAllItems()

        if !stats.online {
            menu.addItem(item("flow — not running"))
            menu.addItem(item(consoleURL))
            menu.addItem(.separator())
        } else {
            let phase = stats.phase.isEmpty ? "—" : stats.phase
            menu.addItem(item("Phase: \(phase)"))
            if !stats.target.isEmpty {
                menu.addItem(item("Target: \(shortURN(stats.target))"))
            }
            menu.addItem(item("Tick \(stats.tick) · next frame \(stats.nextFrameTick)"))
            menu.addItem(.separator())

            let breakdown = stats.cellCounts.map { "\($0.1) \($0.0)" }.joined(separator: " · ")
            menu.addItem(item("Cells: \(stats.totalCells)"))
            if !breakdown.isEmpty { menu.addItem(item("  \(breakdown)")) }
            menu.addItem(.separator())

            if !stats.model.isEmpty { menu.addItem(item("Model: \(stats.model)")) }
            menu.addItem(item("Tokens: \(abbreviate(stats.tokens))"))
            if stats.memoHitRate > 0 {
                menu.addItem(item(String(format: "Memo hit rate: %.0f%%", stats.memoHitRate * 100)))
            }
            menu.addItem(.separator())
        }

        let open = NSMenuItem(title: "Open Viewer", action: #selector(openViewer), keyEquivalent: "o")
        open.target = self
        menu.addItem(open)

        let refreshItem = NSMenuItem(title: "Refresh Now", action: #selector(refreshNow), keyEquivalent: "r")
        refreshItem.target = self
        menu.addItem(refreshItem)

        menu.addItem(.separator())
        let quit = NSMenuItem(title: "Quit", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        menu.addItem(quit)
    }

    // Refresh as the menu opens so the numbers are current at the moment they are
    // actually read, not up to pollInterval stale.
    func menuWillOpen(_ menu: NSMenu) { refresh() }

    @objc func openViewer() {
        if let url = URL(string: consoleURL) { NSWorkspace.shared.open(url) }
    }

    @objc func refreshNow() { refresh() }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
// .accessory keeps the helper out of the Dock and the ⌘-Tab switcher — it lives
// only in the menu bar.
app.setActivationPolicy(.accessory)
app.run()
