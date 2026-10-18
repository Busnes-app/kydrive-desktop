import XCTest
@testable import KyDriveKit

@MainActor
final class CoreClientTests: XCTestCase {
    private var fake: FakeCore!
    private var model: AppModel!

    override func setUp() async throws {
        fake = try FakeCore()
        model = AppModel(core: CoreClient(socketPath: fake.path))
    }

    override func tearDown() async throws { fake.stop() }

    // core/cmd/kydrive-sync/main.go: filepath.Join(os.UserConfigDir(), "kydrive", "sync.sock").
    func testSocketPathMatchesCore() throws {
        XCTAssertEqual(try SocketPath.default(environment: ["HOME": "/Users/alice"]),
                       "/Users/alice/Library/Application Support/kydrive/sync.sock")
        XCTAssertEqual(try SocketPath.default(environment: ["HOME": "/Users/alice/"]),
                       "/Users/alice/Library/Application Support/kydrive/sync.sock")
        // XDG_RUNTIME_DIR is the core's choice only on Linux.
        XCTAssertEqual(try SocketPath.default(environment: ["HOME": "/Users/alice", "XDG_RUNTIME_DIR": "/run/user/501"]),
                       "/Users/alice/Library/Application Support/kydrive/sync.sock")
        XCTAssertThrowsError(try SocketPath.default(environment: [:]))
    }

    // The real core, given the same HOME, listens exactly where the shell looks.
    func testRealCoreListensOnTheShellsPath() async throws {
        guard let bin = ProcessInfo.processInfo.environment["KYDRIVE_SYNC_BIN"] else {
            throw XCTSkip("set KYDRIVE_SYNC_BIN to a built kydrive-sync")
        }
        let home = tempDir("kdhome")
        let path = try SocketPath.default(environment: ["HOME": home])
        let process = Process()
        process.executableURL = URL(fileURLWithPath: bin)
        process.environment = ["HOME": home, "PATH": "/usr/bin:/bin"]
        try process.run()
        defer { process.terminate(); process.waitUntilExit() }

        let core = CoreClient(socketPath: path)
        var status: Status?
        for _ in 0..<100 where status == nil {
            status = try? await core.call("Status")
            if status == nil { try await Task.sleep(nanoseconds: 100_000_000) }
        }
        let s = try XCTUnwrap(status, "no core answered on \(path)")
        XCTAssertFalse(s.linked)
        XCTAssertEqual(s.state, "unlinked")
        XCTAssertEqual(s.root, home + "/KyDrive")
        let dir = try FileManager.default.attributesOfItem(atPath: (path as NSString).deletingLastPathComponent)
        XCTAssertEqual(dir[.posixPermissions] as? Int, 0o700)
        XCTAssertEqual(try FileManager.default.attributesOfItem(atPath: path)[.posixPermissions] as? Int, 0o600)
    }

    func testOverlongSocketPathIsRefusedClearly() async {
        let path = "/Users/" + String(repeating: "a", count: 100) + "/sync.sock"
        do {
            let _: Status = try await CoreClient(socketPath: path).call("Status")
            XCTFail("an over-long path was accepted")
        } catch {
            XCTAssertEqual(error as? CoreError, .pathTooLong(path))
            XCTAssertTrue(error.localizedDescription.contains("over the 103 macOS allows"), error.localizedDescription)
        }
        XCTAssertNoThrow(try unixAddress("/" + String(repeating: "a", count: 102)))
        XCTAssertThrowsError(try unixAddress("/" + String(repeating: "a", count: 103)))
    }

    func testRefreshFillsStatusAndConflicts() async {
        fake.answer("Core.Conflicts", [["ID": 1, "Workspace": "w", "Path": "a (conflict).txt", "Kind": "conflict", "Detail": "kept both", "Created": 0]])
        await model.refresh()
        XCTAssertNil(model.shownError)
        XCTAssertTrue(model.status.linked)
        XCTAssertEqual(model.status.state, "idle")
        XCTAssertEqual(model.conflicts.map(\.path), ["a (conflict).txt"])
        XCTAssertEqual(fake.methods.prefix(3), ["Core.Status", "Core.Conflicts", "Core.Activity"])
    }

    func testActionsSendTheCoresParams() async {
        await model.choose(["a", "b"])
        await model.pause(true)
        await model.confirmDeletes(workspace: "w", allow: true)
        await model.unlink(removeFiles: true)
        XCTAssertEqual(fake.params("Core.Choose").first?["IDs"] as? [String], ["a", "b"])
        XCTAssertEqual(fake.params("Core.Pause").first?["Paused"] as? Bool, true)
        XCTAssertEqual(fake.params("Core.ConfirmDeletes").first?["Workspace"] as? String, "w")
        XCTAssertEqual(fake.params("Core.ConfirmDeletes").first?["Allow"] as? Bool, true)
        XCTAssertEqual(fake.params("Core.Unlink").first?["RemoveFiles"] as? Bool, true)
    }

    // A failed action's error stays up through the refresh that follows it.
    func testCoreErrorIsShown() async {
        await model.refresh()
        fake.fail("Core.Choose", "workspace not found")
        await model.choose(["x"])
        XCTAssertEqual(model.shownError, "workspace not found")
    }

    // Link waits for the browser for up to ten minutes; Status must keep answering meanwhile.
    func testPendingLinkDoesNotBlockStatus() async {
        fake.holdLink = true
        let link = Task { await model.link(server: "https://drive.example.com", name: "mac") }
        for _ in 0..<500 where !fake.methods.contains("Core.Link") { try? await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertTrue(model.linking)
        let before = fake.params("Core.Status").count
        await model.refresh()
        XCTAssertGreaterThan(fake.params("Core.Status").count, before)
        fake.releaseLink()
        await link.value
        XCTAssertFalse(model.linking)
        XCTAssertEqual(fake.params("Core.Link").first?["Server"] as? String, "https://drive.example.com")
    }

    // The first activity read only sets the baseline; later warnings are announced once.
    func testWarnsOnNewActivityOnly() async {
        var warnings: [String] = []
        model.onWarning = { warnings.append($0) }
        fake.answer("Core.Activity", [["ID": 7, "Created": 0, "Level": "warn", "Message": "old"]])
        await model.refresh()
        XCTAssertEqual(warnings, [])
        fake.answer("Core.Activity", [["ID": 9, "Created": 0, "Level": "warn", "Message": "Lost access to Docs"],
                                      ["ID": 8, "Created": 0, "Level": "info", "Message": "synced"]])
        await model.refresh()
        await model.refresh()
        XCTAssertEqual(warnings, ["Lost access to Docs"])
        XCTAssertEqual(fake.params("Core.Activity").last?["After"] as? Int, 9)
    }

    func testMissingCoreReportsNotRunning() async {
        let lonely = AppModel(core: CoreClient(socketPath: tempDir("kdnone") + "/sync.sock"))
        var asked = 0
        lonely.onNotRunning = { asked += 1 }
        await lonely.refresh()
        XCTAssertEqual(lonely.shownError, "KyDrive sync is not running")
        XCTAssertEqual(asked, 1)
    }

    func testOnlyWebURLsOpen() async {
        XCTAssertNotNil(webURL("https://drive.example.com"))
        XCTAssertNotNil(webURL("HTTP://127.0.0.1:8080"))
        for bad in ["javascript:alert(1)", "file:///Applications/Calculator.app", "x-apple.systempreferences:",
                    "vnc://drive.example.com", "https:///nohost", "", "drive.example.com"] {
            XCTAssertNil(webURL(bad), bad)
        }
        var opened: [URL] = []
        model.openURL = { opened.append($0) }
        var status = FakeCore.status
        status["Server"] = "file:///Applications/Calculator.app"
        fake.answer("Core.Status", status)
        await model.refresh()
        model.openInBrowser()
        XCTAssertEqual(opened, [])
        fake.answer("Core.Status", FakeCore.status)
        await model.refresh()
        model.openInBrowser()
        XCTAssertEqual(opened, [URL(string: "https://drive.example.com")!])
    }
}
