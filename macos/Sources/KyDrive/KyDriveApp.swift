import KyDriveKit
import ServiceManagement
import SwiftUI
import UserNotifications

@main
struct KyDriveApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        MenuBarExtra("KyDrive", systemImage: "externaldrive.badge.icloud") {
            MenuContent(model: delegate.model)
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
    private var started = false
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
        model.onNotRunning = { [weak self] in self?.startCoreOnce() }
        model.start()
    }

    // Without the launch agent (refused, or disabled in Login Items) the app starts the core
    // itself, once. Never alongside the agent: two cores would fight over one socket.
    private func startCoreOnce() {
        guard !started, agent.status != .enabled, let core = Bundle.main.url(forAuxiliaryExecutable: "kydrive-sync") else { return }
        started = true
        let process = Process()
        process.executableURL = core
        do { try process.run() } catch { NSLog("KyDrive: could not start kydrive-sync: %@", String(describing: error)) }
        self.process = process
    }

    private func setAgent(_ on: Bool) throws {
        if on {
            // The agent runs its own core: stop the one this app started first.
            if let p = process, p.isRunning { p.terminate(); p.waitUntilExit() }
            process = nil
            started = false
            try agent.register()
            if agent.status == .requiresApproval { SMAppService.openSystemSettingsLoginItems() }
        } else {
            // launchd stops the agent's core; the next refresh finds none and the app starts its own.
            try agent.unregister()
        }
    }
}
