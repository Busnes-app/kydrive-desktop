import KyDriveKit
import ServiceManagement
import SwiftUI
import UserNotifications

@main
struct KyDriveApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        MenuBarExtra("KyDrive", systemImage: "externaldrive.badge.icloud") {
            ScrollingMenu { MenuContent(model: delegate.model) }
        }
        .menuBarExtraStyle(.window)
        Settings { AppearanceSettings() }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let model = AppModel(core: CoreClient(socketPath: (try? SocketPath.default()) ?? ""))
    // The bundled core runs as this per-user launch agent (Contents/Library/LaunchAgents).
    private let agent = SMAppService.agent(plistName: "com.kysecurity.drive.sync.plist")
    private var started: Date? // when this app last started the core
    private var process: Process? // the core this app started, without the agent

    func applicationDidFinishLaunching(_ notification: Notification) {
        _ = AppearanceStore.load() // migrates a saved suite theme once
        // Start at login is the agent: an install that registered it keeps it; a fresh one starts off.
        model.startsAtLogin = { [agent] in agent.status == .enabled }
        model.setStartsAtLogin = { [weak self] on in try self?.setAgent(on) }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert]) { _, _ in }
        model.onWarning = { message in
            UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: UUID().uuidString, content: notice(message), trigger: nil))
        }
        model.version = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? ""
        model.onNotRunning = { [weak self] in self?.startCoreOnce() }
        model.start()
    }

    // Without the launch agent (off, or disabled in Login Items) the app starts the core itself,
    // at most once a minute. Never alongside the agent: two cores would fight over one socket.
    // With it, launchd is asked to start the agent's core: it does not restart one that exited
    // cleanly (an older core's Quit after an upgrade, a plain SIGTERM).
    private func startCoreOnce() {
        guard started.map({ Date().timeIntervalSince($0) > 60 }) ?? true else { return }
        if agent.status == .enabled {
            started = Date()
            let launchctl = Process()
            launchctl.executableURL = URL(fileURLWithPath: "/bin/launchctl")
            launchctl.arguments = ["kickstart", "gui/" + String(getuid()) + "/com.kysecurity.drive.sync"]
            do { try launchctl.run() } catch { NSLog("KyDrive: could not start the launch agent: %@", String(describing: error)) }
            return
        }
        guard let core = Bundle.main.url(forAuxiliaryExecutable: "kydrive-sync") else { return }
        started = Date()
        let process = Process()
        process.executableURL = core
        do { try process.run() } catch { NSLog("KyDrive: could not start kydrive-sync: %@", String(describing: error)) }
        self.process = process
    }

    private func setAgent(_ on: Bool) throws {
        if on {
            // The agent runs its own core: stop the one this app started (launchd retries the
            // agent's until it has exited).
            if let p = process, p.isRunning { p.terminate() }
            process = nil
            try agent.register()
            if agent.status == .requiresApproval { SMAppService.openSystemSettingsLoginItems() }
        } else {
            // launchd stops the agent's core; the next refresh finds none and the app starts its own.
            try agent.unregister()
        }
    }
}
