import Foundation
@testable import KyDriveKit

/// A stand-in for kydrive-sync on a real Unix socket: answers each "Core.<Method>" with a
/// canned result or error and records what it was asked. With holdLink, Core.Link waits for
/// releaseLink().
final class FakeCore: @unchecked Sendable {
    let path: String
    private let listener: Int32
    private let lock = NSLock()
    private var results: [String: Any] = [:]
    private var errors: [String: String] = [:]
    private var recorded: [(method: String, params: [String: Any])] = []
    private let linkGate = DispatchSemaphore(value: 0)
    var holdLink = false

    static let status: [String: Any] = [
        "Linked": true, "Server": "https://drive.example.com", "DevicesURL": "", "Root": "/Users/a/KyDrive",
        "State": "idle", "Error": "", "Paused": false, "LastSync": 0, "Workspaces": NSNull(),
    ]

    init() throws {
        path = tempDir("kdfake") + "/sync.sock"
        listener = socket(AF_UNIX, SOCK_STREAM, 0)
        let addr = try unixAddress(path)
        guard withSockaddr(addr, { bind(listener, $0, $1) }) == 0, listen(listener, 16) == 0 else {
            throw CoreError.message("fake core: " + String(cString: strerror(errno)))
        }
        answer("Core.Status", FakeCore.status)
        answer("Core.Conflicts", [Any]())
        answer("Core.Activity", [Any]())
        answer("Core.Workspaces", [Any]())
        Thread.detachNewThread { [self] in
            while true {
                let c = accept(listener, nil, nil)
                if c < 0 { return }
                Thread.detachNewThread { self.serve(c) }
            }
        }
    }

    func stop() {
        close(listener)
        unlink(path)
        if holdLink { releaseLink() }
    }

    func answer(_ method: String, _ result: Any) { locked { results[method] = result; errors[method] = nil } }
    func fail(_ method: String, _ error: String) { locked { errors[method] = error } }
    func releaseLink() { linkGate.signal() }
    func params(_ method: String) -> [[String: Any]] { locked { recorded.filter { $0.method == method }.map(\.params) } }
    var methods: [String] { locked { recorded.map(\.method) } }

    private func locked<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }

    private func serve(_ c: Int32) {
        defer { close(c) }
        guard let line = readLine(c),
              let req = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
              let method = req["method"] as? String else { return }
        let params = (req["params"] as? [[String: Any]])?.first ?? [:]
        locked { recorded.append((method, params)) }
        if method == "Core.Link" && holdLink { linkGate.wait() }
        let (result, error): (Any, Any) = locked {
            if let e = errors[method] { return (NSNull(), e) }
            return (results[method] ?? [String: Any](), NSNull())
        }
        let reply = try! JSONSerialization.data(withJSONObject: ["id": req["id"] ?? NSNull(), "result": result, "error": error])
        _ = writeAll(c, reply + Data("\n".utf8))
    }
}

/// A fresh directory under /tmp: the per-user temp dir is too long for sun_path.
func tempDir(_ prefix: String) -> String {
    var template = Array("/tmp/\(prefix).XXXXXX".utf8CString)
    return String(cString: mkdtemp(&template)!)
}
