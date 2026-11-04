import SwiftUI

// Every string from the core or the server goes through Text(verbatim:). A string literal or
// LocalizedStringKey would parse Markdown, so a workspace named "[x](https://…)" became a link.
// PlainTextTests enforces this for every file that imports SwiftUI.

/// The menu bar window: link this computer, then choose workspaces, answer held deletes, see
/// conflicts and unlink.
public struct MenuContent: View {
    @ObservedObject var model: AppModel
    @State private var server = ""
    @State private var name = ""
    @State private var picked: [String: Bool] = [:]
    @State private var unlinking = false
    @State private var removeFiles = false

    public init(model: AppModel) { self.model = model }

    public var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            BrandRow()
            VStack(alignment: .leading, spacing: 4) {
                if model.status.linked || model.signedOut {
                    Text(verbatim: model.status.server).font(KyType.title).fixedSize(horizontal: false, vertical: true)
                    StatusLine(state: model.status.paused ? "paused" : model.status.state)
                } else {
                    Text("Link this computer").font(KyType.title)
                }
            }
            if let error = model.shownError { ErrorBanner(text: error, fill: true) }
            if model.signedOut { signInForm }
            if model.status.linked { linked } else if !model.signedOut { linkForm }
            Hairline()
            HStack {
                SettingsButton().buttonStyle(KyButtonStyle())
                Button("Quit KyDrive") { NSApp.terminate(nil) }.buttonStyle(KyButtonStyle())
            }
        }
        .padding(16)
        .frame(width: 360)
        .font(KyType.body)
        .kyThemed()
    }

    // The browser does the sign-in and approval.
    private var linkForm: some View {
        Card {
            // Each label sits above its full-width field.
            VStack(alignment: .leading, spacing: 6) {
                Text("KyDrive address").font(KyType.nav)
                TextField("https://drive.example.com", text: $server).accessibilityLabel(Text("KyDrive address"))
                    .disabled(model.linking)
                Text("Computer name").font(KyType.nav).padding(.top, 10)
                TextField("This computer's name", text: $name).accessibilityLabel(Text("Computer name"))
                    .disabled(model.linking)
                HStack {
                    Button(model.linking ? "Waiting for approval in your browser…" : "Link") {
                        Task { await model.link(server: server, name: name) }
                    }
                    .buttonStyle(KyButtonStyle(kind: .primary))
                    .disabled(model.linking || server.isEmpty)
                    if model.linking { Button("Cancel") { Task { await model.cancelLink() } }.buttonStyle(KyButtonStyle()) }
                }
                .padding(.top, 10)
            }
            .padding(16)
        }
    }

    // Signed out: the browser signs in to the same account; the workspaces stay.
    private var signInForm: some View {
        Card {
            VStack(alignment: .leading, spacing: 10) {
                Text("KyDrive no longer accepts this computer's sign-in. Sign in again with the same account to keep syncing your workspaces.")
                    .fixedSize(horizontal: false, vertical: true)
                HStack {
                    Button(model.linking ? "Waiting for approval in your browser…" : "Sign in again") { Task { await model.signIn() } }
                        .buttonStyle(KyButtonStyle(kind: .primary))
                        .disabled(model.linking)
                    if model.linking { Button("Cancel") { Task { await model.cancelLink() } }.buttonStyle(KyButtonStyle()) }
                }
                if !model.status.linked { unlinkControl }
            }
            .padding(16)
        }
    }

    @ViewBuilder private var unlinkControl: some View {
        if unlinking { unlinkConfirm } else {
            // Removing files is chosen afresh each time.
            Button("Unlink…") { removeFiles = false; unlinking = true }.buttonStyle(KyButtonStyle(kind: .danger))
        }
    }

    @ViewBuilder private var linked: some View {
        ForEach((model.status.workspaces ?? []).filter { $0.heldDeletes > 0 }) { w in
            HeldDeletesBanner(workspace: w, model: model, fill: true)
        }

        KySection(Text("Workspaces on this computer")) {
            Card {
                ForEach(Array(model.workspaces.enumerated()), id: \.element.id) { i, c in
                    if i > 0 { Hairline() }
                    WorkspaceRow(choice: c, isOn: Binding(get: { picked[c.id] ?? c.chosen }, set: { picked[c.id] = $0 }))
                        .padding(.horizontal, 12).frame(minHeight: 40)
                }
                Hairline()
                VStack(alignment: .leading, spacing: 10) {
                    Text("Unchecking a workspace removes its files from this computer. Edits that never uploaded are kept in the Unsynced folder.")
                        .font(KyType.meta).modifier(Muted()).fixedSize(horizontal: false, vertical: true)
                    HStack {
                        Button("Apply") {
                            let ids = model.workspaces.filter { picked[$0.id] ?? $0.chosen }.map(\.id)
                            picked = [:]
                            Task { await model.choose(ids) }
                        }
                        .buttonStyle(KyButtonStyle(kind: .primary))
                        // An empty list is one that did not load: applying it would remove every workspace.
                        .disabled(model.workspaces.isEmpty)
                        Button("Reload") { Task { await model.loadWorkspaces() } }.buttonStyle(KyButtonStyle())
                    }
                }
                .padding(12)
            }
        }

        if !model.conflicts.isEmpty {
            KySection(Text("Conflicts and kept files")) {
                Card {
                    ForEach(Array(model.conflicts.enumerated()), id: \.element.id) { i, issue in
                        if i > 0 { Hairline() }
                        ConflictRow(issue: issue).padding(12)
                    }
                }
            }
        }

        HStack {
            Button("Open folder") { model.openFolder() }
            Button("Open in browser") { model.openInBrowser() }
            Button(model.status.paused ? "Resume" : "Pause") { Task { await model.pause(!model.status.paused) } }
        }
        .buttonStyle(KyButtonStyle())
        unlinkControl
    }

    private var unlinkConfirm: some View {
        Card {
            VStack(alignment: .leading, spacing: 10) {
                Text("Unlink this computer?").font(KyType.section)
                Text("KyDrive stops syncing and this computer is revoked on the server.")
                    .fixedSize(horizontal: false, vertical: true)
                Toggle("Also remove KyDrive files from this computer (unuploaded edits are kept in Unsynced)", isOn: $removeFiles)
                HStack {
                    Button("Cancel") { unlinking = false }.buttonStyle(KyButtonStyle())
                    Button("Unlink") {
                        unlinking = false
                        Task { await model.unlink(removeFiles: removeFiles) }
                    }
                    .buttonStyle(KyButtonStyle(kind: .dangerFilled))
                }
            }
            .padding(16)
        }
    }
}

/// ky-ui's type scale (--ky-type-*): title, section, body, nav (buttons) and meta.
enum KyType {
    static let title = Font.system(size: 24, weight: .semibold)
    static let section = Font.system(size: 17, weight: .semibold)
    static let body = Font.system(size: 15)
    static let nav = Font.system(size: 14, weight: .semibold)
    static let meta = Font.system(size: 13)
}

/// The brand row: the app icon and the ink wordmark, nothing under it.
struct BrandRow: View {
    var body: some View {
        HStack(spacing: 8) {
            Image(nsImage: NSApplication.shared.applicationIconImage).resizable().frame(width: 28, height: 28)
            Text("KyDrive").font(.system(size: 17, weight: .bold))
        }
    }
}

/// A section's sentence-case title over its content.
struct KySection<Content: View>: View {
    let title: Text
    @ViewBuilder let content: Content
    init(_ title: Text, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            title.font(KyType.section)
            content
        }
    }
}

/// Secondary text in the theme's ink.
struct Muted: ViewModifier {
    @Environment(\.themeHalf) private var half
    func body(content: Content) -> some View { content.foregroundStyle(half.ink.color) }
}

/// A flat card: the panel, a 1px line and ky-ui's 10px radius, no shadow.
struct Card<Content: View>: View {
    @ViewBuilder let content: Content
    @Environment(\.themeHalf) private var half
    var body: some View {
        VStack(alignment: .leading, spacing: 0) { content }
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(half.panel.color, in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(half.line.color, lineWidth: 1))
    }
}

/// A 1px line between rows.
struct Hairline: View {
    @Environment(\.themeHalf) private var half
    var body: some View { Rectangle().fill(half.line.color).frame(height: 1) }
}

/// The suite's button hierarchy: one filled primary per group, the rest secondary; destructive
/// actions use danger (outlined, or filled for the final confirmation). A disabled primary stays
/// filled, dimmed.
struct KyButtonStyle: ButtonStyle {
    enum Kind { case primary, secondary, danger, dangerFilled }
    var kind: Kind = .secondary
    @Environment(\.themeHalf) private var half
    @Environment(\.isEnabled) private var enabled

    func makeBody(configuration: Configuration) -> some View {
        let (fill, text, edge): (RGB, RGB, RGB?) = switch kind {
        case .primary: (half.accent, half.buttonText, nil)
        case .secondary: (half.panel, half.inkStrong, half.line)
        case .danger: (half.panel, half.danger, RGB(half.danger.hex, alpha: 0.55))
        case .dangerFilled: (half.danger, half.dark ? half.buttonText : half.panel, nil)
        }
        let shown = enabled || kind == .primary
        configuration.label
            .font(KyType.nav)
            .lineLimit(1)
            .fixedSize()
            .foregroundStyle((shown ? text : half.ink).color)
            .padding(.horizontal, 12)
            .frame(minHeight: 28)
            .background((shown ? fill : half.panel).color, in: RoundedRectangle(cornerRadius: 8))
            .overlay {
                if let edge = shown ? edge : half.line {
                    RoundedRectangle(cornerRadius: 8).strokeBorder(edge.color, lineWidth: 1)
                }
            }
            .overlay(RoundedRectangle(cornerRadius: 8).fill(half.inkStrong.color.opacity(configuration.isPressed ? 0.12 : 0)))
            .contentShape(RoundedRectangle(cornerRadius: 8))
            .opacity(enabled ? 1 : (kind == .primary ? 0.5 : 1))
    }
}

/// The one status message, as on Linux: the status tint, a 1px status edge, an icon and plain
/// text, then any buttons. Errors and held deletes both use it.
struct Banner<Buttons: View>: View {
    let danger: Bool
    let text: String
    let fill: Bool
    let buttons: Buttons
    /// fill stretches it to the menu's width; without it the banner is its text's size.
    init(danger: Bool, verbatim text: String, fill: Bool, @ViewBuilder buttons: () -> Buttons) {
        self.danger = danger
        self.text = text
        self.fill = fill
        self.buttons = buttons()
    }
    @Environment(\.themeHalf) private var half
    var body: some View {
        let tone = danger ? half.danger : half.warning
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: danger ? "xmark.octagon.fill" : "exclamationmark.triangle.fill").foregroundStyle(tone.color)
                Text(verbatim: text).fixedSize(horizontal: false, vertical: true)
            }
            // Side by side when they fit, else stacked; a label never wraps.
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) { buttons }
                VStack(alignment: .leading, spacing: 8) { buttons }
            }
            .padding(.leading, 24)
        }
        .padding(12)
        .frame(maxWidth: fill ? .infinity : nil, alignment: .leading)
        .background(half.soft(tone).color, in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(RGB(tone.hex, alpha: 0.45).color, lineWidth: 1))
    }
}

// Status colours never stand alone: each comes with a word and an icon or dot.
struct ErrorBanner: View {
    let text: String
    var fill = false
    var body: some View { Banner(danger: true, verbatim: text, fill: fill) { EmptyView() } }
}

/// Connection state: a dot in the status colour beside the word.
struct StatusLine: View {
    let state: String
    @Environment(\.themeHalf) private var half
    var body: some View {
        let colour: RGB = switch state {
        case "idle": half.success
        case "syncing": half.accent
        case "paused": half.warning
        case "error", "revoked", "signed_out": half.danger
        default: half.ink
        }
        HStack(spacing: 6) {
            Circle().fill(colour.color).frame(width: 8, height: 8)
            Text(verbatim: "Status: " + state).font(KyType.meta).modifier(Muted())
        }
    }
}

/// Opens the Settings window (Appearance).
struct SettingsButton: View {
    var body: some View {
        if #available(macOS 14, *) {
            SettingsLink { Text("Settings…") }
                .simultaneousGesture(TapGesture().onEnded { NSApp.activate(ignoringOtherApps: true) })
        } else {
            Button("Settings…") {
                NSApp.activate(ignoringOtherApps: true)
                NSApp.sendAction(Selector(("showSettingsWindow:")), to: nil, from: nil)
            }
        }
    }
}

struct WorkspaceRow: View {
    let choice: Choice
    @Binding var isOn: Bool
    var body: some View {
        Toggle(isOn: $isOn) { Text(verbatim: choice.name) }
    }
}

struct ConflictRow: View {
    let issue: Issue
    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(verbatim: issue.path).fixedSize(horizontal: false, vertical: true)
            Text(verbatim: issue.detail).font(KyType.meta).modifier(Muted()).fixedSize(horizontal: false, vertical: true)
        }
    }
}

// The large-delete guard: deletes wait here until the user answers.
struct HeldDeletesBanner: View {
    let workspace: WorkspaceStatus
    let model: AppModel
    var fill = false
    var body: some View {
        Banner(danger: false, verbatim: "\(workspace.heldDeletes) items were deleted from \(workspace.name) on this computer.", fill: fill) {
            Button("Delete from KyDrive too") { Task { await model.confirmDeletes(workspace: workspace.id, allow: true) } }
                .buttonStyle(KyButtonStyle(kind: .danger))
            Button("Restore them") { Task { await model.confirmDeletes(workspace: workspace.id, allow: false) } }
                .buttonStyle(KyButtonStyle())
        }
    }
}
