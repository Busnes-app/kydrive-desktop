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
        VStack(alignment: .leading, spacing: 10) {
            if let error = model.shownError { ErrorBanner(text: error) }
            if model.status.linked { linked } else { linkForm }
            Divider()
            HStack {
                SettingsButton()
                Button("Quit KyDrive") { NSApp.terminate(nil) }
            }
        }
        .padding()
        .frame(width: 360)
        .kyThemed()
    }

    // The browser does the sign-in and approval.
    private var linkForm: some View {
        VStack(alignment: .leading) {
            Text("Link this computer").font(.headline)
            TextField("https://drive.example.com", text: $server)
            TextField("This computer's name", text: $name)
            Button(model.linking ? "Waiting for approval in your browser…" : "Link") {
                Task { await model.link(server: server, name: name) }
            }
            .disabled(model.linking || server.isEmpty)
        }
        .disabled(model.linking)
    }

    @ViewBuilder private var linked: some View {
        Text(verbatim: model.status.server).font(.headline)
        StatusLine(state: model.status.paused ? "paused" : model.status.state)
        ForEach((model.status.workspaces ?? []).filter { $0.heldDeletes > 0 }) { w in
            HeldDeletesBanner(workspace: w, model: model)
        }

        Text("Workspaces on this computer").font(.headline)
        ForEach(model.workspaces) { c in
            WorkspaceRow(choice: c, isOn: Binding(get: { picked[c.id] ?? c.chosen }, set: { picked[c.id] = $0 }))
        }
        Text("Unchecking a workspace removes its files from this computer. Edits that never uploaded are kept in the Unsynced folder.")
            .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        HStack {
            Button("Apply") {
                let ids = model.workspaces.filter { picked[$0.id] ?? $0.chosen }.map(\.id)
                picked = [:]
                Task { await model.choose(ids) }
            }
            Button("Reload") { Task { await model.loadWorkspaces() } }
        }

        if !model.conflicts.isEmpty {
            Text("Conflicts and kept files").font(.headline)
            ForEach(model.conflicts) { ConflictRow(issue: $0) }
        }

        Divider()
        HStack {
            Button("Open folder") { model.openFolder() }
            Button("Open in browser") { model.openInBrowser() }
            Button(model.status.paused ? "Resume" : "Pause") { Task { await model.pause(!model.status.paused) } }
        }
        if unlinking { unlinkConfirm } else { Button("Unlink…") { unlinking = true } }
    }

    private var unlinkConfirm: some View {
        GroupBox {
            VStack(alignment: .leading) {
                Text("Unlink this computer?").font(.headline)
                Text("KyDrive stops syncing and this computer is revoked on the server.")
                    .fixedSize(horizontal: false, vertical: true)
                Toggle("Also remove KyDrive files from this computer (unuploaded edits are kept in Unsynced)", isOn: $removeFiles)
                HStack {
                    Button("Cancel") { unlinking = false }
                    Button("Unlink") {
                        unlinking = false
                        Task { await model.unlink(removeFiles: removeFiles) }
                    }
                }
            }
        }
    }
}

// Status colours never stand alone: each comes with a word and an icon or dot.
struct ErrorBanner: View {
    let text: String
    @Environment(\.themeHalf) private var half
    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Image(systemName: "xmark.octagon.fill")
            Text(verbatim: text).fixedSize(horizontal: false, vertical: true)
        }
        .foregroundStyle(half.danger.color)
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(half.soft(half.danger).color, in: RoundedRectangle(cornerRadius: 6))
    }
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
            Text(verbatim: "Status: " + state)
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
        Text(verbatim: issue.path + " — " + issue.detail).fixedSize(horizontal: false, vertical: true)
    }
}

// The large-delete guard: deletes wait here until the user answers.
struct HeldDeletesBanner: View {
    let workspace: WorkspaceStatus
    let model: AppModel
    @Environment(\.themeHalf) private var half
    var body: some View {
        GroupBox {
            VStack(alignment: .leading) {
                HStack(alignment: .firstTextBaseline) {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(half.warning.color)
                    Text(verbatim: "\(workspace.heldDeletes) items were deleted from \(workspace.name) on this computer.")
                        .fixedSize(horizontal: false, vertical: true)
                }
                HStack {
                    Button("Delete from KyDrive too") { Task { await model.confirmDeletes(workspace: workspace.id, allow: true) } }
                    Button("Restore them") { Task { await model.confirmDeletes(workspace: workspace.id, allow: false) } }
                }
            }
        }
    }
}
