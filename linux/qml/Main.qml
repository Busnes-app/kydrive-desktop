import QtQuick
import QtQuick.Controls as Controls
import QtQuick.Layouts
import org.kde.kirigami as Kirigami

// The KyDrive window: link this computer, then choose workspaces, answer held deletes, see
// conflicts and unlink. Everything shown comes from the core (Core = CoreClient).
Kirigami.ApplicationWindow {
    id: root
    title: "KyDrive"
    width: Kirigami.Units.gridUnit * 30
    height: Kirigami.Units.gridUnit * 36
    readonly property bool linked: Core.status.Linked === true

    // InlineMessage renders rich text it cannot be told otherwise: escape server text and wrap
    // it so the escapes are decoded, never interpreted.
    function rich(s) {
        return "<span>" + String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;") + "</span>"
    }
    onLinkedChanged: if (linked) Core.loadWorkspaces()

    Theme { id: theme }

    pageStack.initialPage: Kirigami.ScrollablePage {
        id: page
        title: root.linked ? Core.status.Server : i18n("Link this computer")
        background: Rectangle { color: theme.bg }
        Component.onCompleted: theme.paint(Kirigami.Theme, () => theme.bg)
        onGlobalToolBarItemChanged: if (globalToolBarItem) theme.paint(globalToolBarItem.Kirigami.Theme, () => theme.panel)

        actions: [
            Kirigami.Action {
                text: i18n("Open folder")
                icon.name: "folder"
                visible: root.linked
                onTriggered: Core.openFolder()
            },
            Kirigami.Action {
                text: i18n("Open in browser")
                icon.name: "internet-services"
                visible: root.linked
                onTriggered: Core.openInBrowser()
            },
            Kirigami.Action {
                text: Core.status.Paused ? i18n("Resume") : i18n("Pause")
                icon.name: Core.status.Paused ? "media-playback-start" : "media-playback-pause"
                visible: root.linked
                onTriggered: Core.pause(!Core.status.Paused)
            },
            Kirigami.Action {
                text: i18n("Unlink…")
                icon.name: "network-disconnect"
                visible: root.linked
                onTriggered: unlinkDialog.open()
            }
        ]

        ColumnLayout {
            spacing: Kirigami.Units.largeSpacing

            Kirigami.InlineMessage {
                Layout.fillWidth: true
                type: Kirigami.MessageType.Error
                readonly property string raw: Core.error !== "" ? Core.error : (Core.status.Error || "")
                visible: raw !== ""
                text: root.rich(raw)
            }

            // Not linked: the browser does the sign-in and approval.
            Kirigami.FormLayout {
                visible: !root.linked
                Layout.fillWidth: true
                Controls.TextField {
                    id: server
                    Kirigami.FormData.label: i18n("KyDrive address:")
                    placeholderText: "https://drive.example.com"
                    enabled: !Core.linking
                }
                Controls.TextField {
                    id: deviceName
                    Kirigami.FormData.label: i18n("Computer name:")
                    placeholderText: i18n("This computer's name")
                    enabled: !Core.linking
                }
                Controls.Button {
                    text: Core.linking ? i18n("Waiting for approval in your browser…") : i18n("Link")
                    enabled: !Core.linking && server.text !== ""
                    onClicked: Core.link(server.text, deviceName.text)
                }
            }

            Controls.Label {
                visible: root.linked
                text: i18n("Status: %1", Core.status.State || "")
                textFormat: Text.PlainText
                color: theme.inkStrong
            }

            // The large-delete guard: deletes wait here until the user answers.
            Repeater {
                model: root.linked ? (Core.status.Workspaces || []) : []
                delegate: Kirigami.InlineMessage {
                    required property var modelData
                    Layout.fillWidth: true
                    visible: modelData.HeldDeletes > 0
                    type: Kirigami.MessageType.Warning
                    text: root.rich(i18n("%1 items were deleted from %2 on this computer.", modelData.HeldDeletes, modelData.Name))
                    actions: [
                        Kirigami.Action {
                            text: i18n("Delete from KyDrive too")
                            onTriggered: Core.confirmDeletes(modelData.ID, true)
                        },
                        Kirigami.Action {
                            text: i18n("Restore them")
                            onTriggered: Core.confirmDeletes(modelData.ID, false)
                        }
                    ]
                }
            }

            Kirigami.Heading {
                visible: root.linked
                level: 3
                text: i18n("Workspaces on this computer")
                color: theme.inkStrong
            }
            Repeater {
                id: choices
                model: root.linked ? Core.workspaces : []
                // The name sits in its own plain-text label; a CheckBox's text may render markup.
                delegate: RowLayout {
                    required property var modelData
                    property alias checked: box.checked
                    Controls.CheckBox {
                        id: box
                        checked: modelData.Chosen
                    }
                    Controls.Label {
                        text: modelData.Name
                        textFormat: Text.PlainText
                        color: theme.inkStrong
                    }
                }
            }
            Controls.Label {
                visible: root.linked
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                color: theme.ink
                text: i18n("Unchecking a workspace removes its files from this computer. Edits that never uploaded are kept in the Unsynced folder.")
            }
            RowLayout {
                visible: root.linked
                Controls.Button {
                    text: i18n("Apply")
                    onClicked: {
                        let ids = []
                        for (let i = 0; i < choices.count; i++)
                            if (choices.itemAt(i).checked)
                                ids.push(choices.itemAt(i).modelData.ID)
                        Core.choose(ids)
                    }
                }
                Controls.Button {
                    text: i18n("Reload")
                    onClicked: Core.loadWorkspaces()
                }
            }

            Kirigami.Heading {
                visible: root.linked && Core.conflicts.length > 0
                level: 3
                text: i18n("Conflicts and kept files")
                color: theme.inkStrong
            }
            Repeater {
                model: root.linked ? Core.conflicts : []
                delegate: Controls.Label {
                    required property var modelData
                    Layout.fillWidth: true
                    wrapMode: Text.WordWrap
                    color: theme.ink
                    textFormat: Text.PlainText
                    text: modelData.Path + " — " + modelData.Detail
                }
            }
        }
    }

    Kirigami.PromptDialog {
        id: unlinkDialog
        title: i18n("Unlink this computer?")
        subtitle: i18n("KyDrive stops syncing and this computer is revoked on the server.")
        standardButtons: Kirigami.Dialog.Cancel
        customFooterActions: [
            Kirigami.Action {
                text: i18n("Unlink")
                icon.name: "network-disconnect"
                onTriggered: {
                    Core.unlink(removeFiles.checked)
                    unlinkDialog.close()
                }
            }
        ]
        Controls.CheckBox {
            id: removeFiles
            text: i18n("Also remove KyDrive files from this computer (unuploaded edits are kept in Unsynced)")
        }
    }
}
