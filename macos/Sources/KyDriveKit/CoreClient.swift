import Foundation

// The shell's only link to kydrive-sync: JSON-RPC 1.0 ("Core.<Method>"), one JSON object per
// line over the core's owner-only Unix socket, one connection per call so a ten-minute Link
// never blocks Status.

public enum SocketPath {
    /// The core's choice, filepath.Join(os.UserConfigDir(), "kydrive", "sync.sock"), where
    /// UserConfigDir on macOS is $HOME/Library/Application Support. Anywhere else could be
    /// answered by another user in the core's place.
    public static func `default`(environment: [String: String] = ProcessInfo.processInfo.environment) throws -> String {
        guard let home = environment["HOME"], !home.isEmpty else { throw CoreError.message("$HOME is not set") }
        return NSTemporaryDirectory() + "kydrive/sync.sock"
    }
}

public enum CoreError: LocalizedError, Equatable {
    case notRunning, stopped, pathTooLong(String), message(String)

    public var errorDescription: String? {
        switch self {
        case .notRunning: return "KyDrive sync is not running"
        case .stopped: return "KyDrive sync stopped"
        case .pathTooLong(let path):
            return "The KyDrive socket path is \(path.utf8.count) bytes, over the 103 macOS allows: \(path)"
        case .message(let text): return text
        }
    }
}

public struct Status: Decodable, Equatable {
    var linked = false
    var server = "", devicesURL = "", root = "", state = "", error = ""
    var paused = false
    var lastSync: Int64 = 0
    var workspaces: [WorkspaceStatus]? = nil // null when none are chosen

    enum CodingKeys: String, CodingKey {
        case linked = "Linked", server = "Server", devicesURL = "DevicesURL", root = "Root", state = "State",
             error = "Error", paused = "Paused", lastSync = "LastSync", workspaces = "Workspaces"
    }
}

public struct WorkspaceStatus: Decodable, Equatable, Identifiable {
    public var id = "", name = "", dir = ""
    var heldDeletes = 0
    enum CodingKeys: String, CodingKey { case id = "ID", name = "Name", dir = "Dir", heldDeletes = "HeldDeletes" }
}

public struct Choice: Decodable, Equatable, Identifiable {
    public var id = "", name = "", kind = "", role = ""
    var chosen = false
    enum CodingKeys: String, CodingKey { case id = "ID", name = "Name", kind = "Kind", role = "Role", chosen = "Chosen" }
}

public struct Issue: Decodable, Equatable, Identifiable {
    public var id: Int64 = 0
    var workspace = "", path = "", kind = "", detail = ""
    var created: Int64 = 0
    enum CodingKeys: String, CodingKey {
        case id = "ID", workspace = "Workspace", path = "Path", kind = "Kind", detail = "Detail", created = "Created"
    }
}

struct Activity: Decodable {
    var id: Int64, created: Int64, level: String, message: String
    enum CodingKeys: String, CodingKey { case id = "ID", created = "Created", level = "Level", message = "Message" }
}

struct Empty: Decodable {}

public struct CoreClient: Sendable {
    public let socketPath: String
    public init(socketPath: String) { self.socketPath = socketPath }

    func call<R: Decodable>(_ method: String, _ params: [String: Any] = [:]) async throws -> R {
        let request = try JSONSerialization.data(withJSONObject: ["method": "Core." + method, "params": [params], "id": 1])
            + Data("\n".utf8)
        let path = socketPath
        let line = try await Task.detached { try Self.exchange(path, request) }.value
        let reply = try JSONDecoder().decode(Reply<R>.self, from: line)
        if let error = reply.error { throw CoreError.message(error) }
        guard let result = reply.result else { throw CoreError.message("kydrive-sync sent an empty reply") }
        return result
    }

    private struct Reply<R: Decodable>: Decodable {
        let result: R?
        let error: String?
    }

    private static func exchange(_ path: String, _ request: Data) throws -> Data {
        let addr = try unixAddress(path)
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw CoreError.message(String(cString: strerror(errno))) }
        defer { close(fd) }
        var one: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        guard withSockaddr(addr, { connect(fd, $0, $1) }) == 0 else { throw CoreError.notRunning }
        guard writeAll(fd, request), let line = readLine(fd) else { throw CoreError.stopped }
        return line
    }
}

/// A sockaddr_un for path, refusing one longer than sun_path holds (104 bytes with the NUL).
func unixAddress(_ path: String) throws -> sockaddr_un {
    var addr = sockaddr_un()
    let bytes = Array(path.utf8)
    if bytes.isEmpty { throw CoreError.message("$HOME is not set") }
    guard bytes.count <= MemoryLayout.size(ofValue: addr.sun_path) else { throw CoreError.pathTooLong(path) }
    addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
    addr.sun_family = sa_family_t(AF_UNIX)
    withUnsafeMutableBytes(of: &addr.sun_path) { $0.copyBytes(from: bytes) }
    return addr
}

func withSockaddr(_ addr: sockaddr_un, _ body: (UnsafePointer<sockaddr>, socklen_t) -> Int32) -> Int32 {
    withUnsafePointer(to: addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { body($0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
    }
}

func writeAll(_ fd: Int32, _ data: Data) -> Bool {
    data.withUnsafeBytes { buf in
        var off = 0
        while off < buf.count {
            let n = write(fd, buf.baseAddress! + off, buf.count - off)
            if n <= 0 { return false }
            off += n
        }
        return true
    }
}

/// The bytes before the first newline, or nil if the peer closes first.
func readLine(_ fd: Int32) -> Data? {
    var out = Data()
    var buf = [UInt8](repeating: 0, count: 4096)
    while true {
        let n = read(fd, &buf, buf.count)
        if n <= 0 { return nil }
        if let i = buf[..<n].firstIndex(of: 10) {
            out.append(contentsOf: buf[..<i])
            return out
        }
        out.append(contentsOf: buf[..<n])
    }
}
