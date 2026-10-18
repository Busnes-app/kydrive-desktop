import AppKit
import UserNotifications

/// What the menu shows. Holds no sync logic: every value comes from the core's answers.
@MainActor
public final class AppModel: ObservableObject {
    @Published private(set) var status = Status()
    @Published private(set) var workspaces: [Choice] = []
    @Published private(set) var conflicts: [Issue] = []
    @Published private(set) var linking = false
    @Published private(set) var actionError = "" // the last action's error, cleared by the next success
    @Published private(set) var offline: String? // set while Status fails

    /// A new `warn` activity, as plain text.
    public var onWarning: (String) -> Void = { _ in }
    /// Nothing answers on the socket.
    public var onNotRunning: () -> Void = {}
    var openURL: (URL) -> Void = { NSWorkspace.shared.open($0) }

    let core: CoreClient
    private var lastActivity: Int64 = -1
    private var refreshing = false
    private var timer: Timer?

    public init(core: CoreClient) { self.core = core }

    public func start() {
        Task { await refresh() }
        timer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] _ in
            Task { await self?.refresh() }
        }
    }

    var shownError: String? {
        let e = offline ?? (actionError.isEmpty ? status.error : actionError)
        return e.isEmpty ? nil : e
    }

    func refresh() async {
        guard !refreshing else { return }
        refreshing = true
        defer { refreshing = false }
        do {
            let s: Status = try await core.call("Status")
            let wasLinked = status.linked
            status = s
            offline = nil
            conflicts = try await core.call("Conflicts")
            announce(try await core.call("Activity", ["After": max(lastActivity, 0)]))
            if s.linked && !wasLinked { await loadWorkspaces() }
        } catch {
            offline = error.localizedDescription
            if error as? CoreError == .notRunning { onNotRunning() }
        }
    }

    /// The first read only sets the baseline; later warnings are announced once, oldest first.
    private func announce(_ newestFirst: [Activity]) {
        let baseline = lastActivity < 0
        for a in newestFirst.reversed() where a.id > lastActivity {
            if !baseline && a.level == "warn" { onWarning(a.message) }
            lastActivity = a.id
        }
        lastActivity = max(lastActivity, 0)
    }

    func loadWorkspaces() async {
        do {
            workspaces = try await core.call("Workspaces")
            actionError = ""
        } catch {
            actionError = error.localizedDescription
        }
    }

    /// Returns once the user approves in the browser the core opened, or after its ten-minute timeout.
    func link(server: String, name: String) async {
        linking = true
        await act("Link", ["Server": server, "Name": name])
        linking = false
        if actionError.isEmpty { await loadWorkspaces() }
    }

    func unlink(removeFiles: Bool) async { await act("Unlink", ["RemoveFiles": removeFiles]) }
    func pause(_ paused: Bool) async { await act("Pause", ["Paused": paused]) }
    func confirmDeletes(workspace: String, allow: Bool) async {
        await act("ConfirmDeletes", ["Workspace": workspace, "Allow": allow])
    }
    func choose(_ ids: [String]) async {
        await act("Choose", ["IDs": ids])
        if actionError.isEmpty { await loadWorkspaces() }
    }

    private func act(_ method: String, _ params: [String: Any]) async {
        do {
            let _: Empty = try await core.call(method, params)
            actionError = ""
        } catch {
            actionError = error.localizedDescription
        }
        await refresh()
    }

    /// Shows the sync root in Finder; never launches what is there.
    func openFolder() {
        if !status.root.isEmpty { NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: status.root) }
    }

    func openInBrowser() {
        if let url = webURL(status.server) { openURL(url) }
    }
}

/// url only if it is http or https with a host; anything else could run an app or a script.
public func webURL(_ s: String) -> URL? {
    guard let url = URL(string: s), let scheme = url.scheme?.lowercased(), scheme == "https" || scheme == "http",
          let host = url.host, !host.isEmpty else { return nil }
    return url
}

/// A notification for a core warning. Its body is plain text; nothing in it is interpreted.
public func notice(_ message: String) -> UNMutableNotificationContent {
    let content = UNMutableNotificationContent()
    content.title = "KyDrive"
    content.body = message
    return content
}
