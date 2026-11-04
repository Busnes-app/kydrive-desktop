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
    private var started: Date? // when this app last started the core

    func applicationDidFinishLaunching(_ notification: Notification) {
        _ = AppearanceStore.load() // migrates a saved suite theme once
        if agent.status != .enabled {
            do { try agent.register() } catch { NSLog("KyDrive: launch agent not registered: %@", String(describing: error)) }
        }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert]) { _, _ in }
        model.onWarning = { message in
            UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: UUID().uuidString, content: notice(message), trigger: nil))
        }
        model.onNotRunning = { [weak self] in self?.startCoreOnce() }
        model.start()
    }

    // Without the launch agent (refused, or disabled in Login Items) the app starts the core itself,
    // at most once a minute. Never alongside the agent: two cores would fight over one socket.
    private func startCoreOnce() {
        guard started.map({ Date().timeIntervalSince($0) > 60 }) ?? true, agent.status != .enabled,
              let core = Bundle.main.url(forAuxiliaryExecutable: "kydrive-sync") else { return }
        started = Date()
        let process = Process()
        process.executableURL = core
        do { try process.run() } catch { NSLog("KyDrive: could not start kydrive-sync: %@", String(describing: error)) }
    }
}
