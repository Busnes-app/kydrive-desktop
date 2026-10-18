import SwiftUI
import XCTest
@testable import KyDriveKit

// Server- and core-supplied strings are plain text: a hostile name must not become a link or
// styling. Rendered Markdown "[x](…) **b**" shows only "x b", so a view that interprets it is
// as small as one showing "x b"; a plain one is far larger.
@MainActor
final class PlainTextTests: XCTestCase {
    let hostile = "[x](https://evil.example/" + String(repeating: "a", count: 60) + ") **b** <b>c</b>"
    let rendered = "x b <b>c</b>"

    private func size<V: View>(_ view: V) -> CGSize {
        NSHostingController(rootView: view).sizeThatFits(in: CGSize(width: 10_000, height: 10_000))
    }

    private func assertPlain<V: View>(_ make: (String) -> V, file: StaticString = #filePath, line: UInt = #line) {
        let plain = size(make(hostile)).width, markdown = size(make(rendered)).width
        XCTAssertGreaterThan(plain - markdown, 200, "hostile markup was interpreted", file: file, line: line)
    }

    // The detector works: Markdown-parsed text really does shrink to "x b".
    func testMarkdownWouldShrink() {
        let parsed = size(Text(LocalizedStringKey(hostile))).width
        XCTAssertLessThan(abs(parsed - size(Text(verbatim: rendered)).width), 40)
        XCTAssertGreaterThan(size(Text(verbatim: hostile)).width - parsed, 200)
    }

    func testRowsShowNamesAsPlainText() {
        let model = AppModel(core: CoreClient(socketPath: "/nonexistent"))
        assertPlain { WorkspaceRow(choice: Choice(id: "w", name: $0), isOn: .constant(true)) }
        assertPlain { ConflictRow(issue: Issue(path: $0, detail: "kept")) }
        assertPlain { ConflictRow(issue: Issue(path: "a.txt", detail: $0)) }
        assertPlain { HeldDeletesBanner(workspace: WorkspaceStatus(id: "w", name: $0, heldDeletes: 30), model: model) }
        assertPlain { ErrorBanner(text: $0) }
    }

    // End to end: names from a core over the socket, shown in the whole menu.
    func testMenuShowsCoreTextAsPlainText() async throws {
        func menuHeight(_ text: String) async throws -> CGFloat {
            let fake = try FakeCore()
            defer { fake.stop() }
            var status = FakeCore.status
            status["Error"] = text
            status["Server"] = text
            fake.answer("Core.Status", status)
            fake.answer("Core.Workspaces", [["ID": "w", "Name": text, "Kind": "team", "Role": "editor", "Chosen": true]])
            fake.answer("Core.Conflicts", [["ID": 1, "Workspace": "w", "Path": text, "Kind": "conflict", "Detail": text, "Created": 0]])
            let model = AppModel(core: CoreClient(socketPath: fake.path))
            await model.refresh()
            XCTAssertEqual(model.workspaces.first?.name, text)
            XCTAssertEqual(model.conflicts.first?.path, text)
            return size(MenuContent(model: model)).height
        }
        let plain = try await menuHeight(hostile), markdown = try await menuHeight(rendered)
        XCTAssertGreaterThan(plain - markdown, 100, "hostile markup was interpreted in the menu")
    }

    func testNotificationBodyIsTheMessageVerbatim() {
        let content = notice(hostile)
        XCTAssertEqual(content.title, "KyDrive")
        XCTAssertEqual(content.body, hostile)
    }

    // Every view file passes dynamic text through Text(verbatim:). A literal with
    // interpolation is a LocalizedStringKey, and so is Text(someKey).
    func testViewsOnlyUseVerbatimForDynamicText() throws {
        let sources = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("Sources")
        let files = FileManager.default.enumerator(at: sources, includingPropertiesForKeys: nil)!
            .compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" }
        var checked = 0
        for file in files {
            let text = try String(contentsOf: file)
            guard text.contains("import SwiftUI") else { continue }
            checked += 1
            for (n, line) in text.components(separatedBy: "\n").enumerated() {
                let at = "\(file.lastPathComponent):\(n + 1): \(line)"
                for banned in ["LocalizedStringKey(", "AttributedString(", "markdown:", "Text(.init("] {
                    XCTAssertFalse(line.contains(banned), at)
                }
                if line.contains("\\(") { XCTAssertTrue(line.contains("verbatim:"), at) }
                var rest = Substring(line)
                while let r = rest.range(of: "Text(") {
                    rest = rest[r.upperBound...]
                    XCTAssertTrue(rest.hasPrefix("verbatim:") || rest.hasPrefix("\""), at)
                }
            }
        }
        XCTAssertGreaterThanOrEqual(checked, 2)
    }
}
