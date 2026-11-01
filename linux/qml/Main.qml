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

    onLinkedChanged: if (linked) Core.loadWorkspaces()
    function showAppearance() {
        appearanceDialog.open()
    }

    Theme { id: theme }
    // For components with their own "theme" property, where a bare "theme" would mean their own.
    readonly property QtObject ky: theme
    // Controls, the toolbar and dialogs draw from the window palette, not Kirigami.Theme.
    palette.window: theme.bg
    palette.windowText: theme.inkStrong
    palette.base: theme.panel
    palette.alternateBase: theme.bg
    palette.text: theme.inkStrong
    palette.button: theme.panel
    palette.buttonText: theme.inkStrong
    palette.highlight: theme.accent
    palette.highlightedText: theme.buttonText
    palette.placeholderText: theme.ink
    font.pixelSize: theme.typeBody

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
                icon.color: theme.danger
                visible: root.linked
                onTriggered: unlinkDialog.open()
            },
            Kirigami.Action {
                text: i18n("Appearance")
                icon.name: "preferences-desktop-theme-global"
                displayHint: Kirigami.DisplayHint.AlwaysHide
                onTriggered: root.showAppearance()
            }
        ]

        // ky-ui's content inset, and the column capped at 720px on a wide window.
        readonly property int inset: Kirigami.Units.gridUnit * 2
        leftPadding: Math.max(inset, (width - 720) / 2)
        rightPadding: leftPadding
        topPadding: inset
        bottomPadding: inset

        // The brand row: logo tile and ink wordmark, nothing under it; the page name is below.
        titleDelegate: RowLayout {
            spacing: Kirigami.Units.smallSpacing * 2
            Item { implicitWidth: Kirigami.Units.smallSpacing }
            Image {
                source: "qrc:/qml/kydrive.png"
                sourceSize: Qt.size(56, 56)
                Layout.preferredWidth: 28
                Layout.preferredHeight: 28
                smooth: true
                mipmap: true
            }
            Controls.Label {
                text: "KyDrive"
                font.pixelSize: theme.typeSection
                font.weight: Font.Bold
                color: theme.inkStrong
            }
        }

        ColumnLayout {
            spacing: Kirigami.Units.gridUnit * 1.5

            // The page head: the linked server (or the link step) and the connection state.
            ColumnLayout {
                Layout.fillWidth: true
                spacing: Kirigami.Units.smallSpacing * 2
                Controls.Label {
                    Layout.fillWidth: true
                    text: root.linked ? (Core.status.Server || "") : i18n("Link this computer")
                    textFormat: Text.PlainText
                    wrapMode: Text.Wrap
                    font.pixelSize: theme.typeTitle
                    font.weight: Font.DemiBold
                    color: theme.inkStrong
                }
                // A dot in the status colour beside the word, never colour alone.
                RowLayout {
                    visible: root.linked
                    spacing: Kirigami.Units.smallSpacing * 2
                    readonly property string state: Core.status.Paused ? "paused" : (Core.status.State || "")
                    Rectangle {
                        implicitWidth: 8
                        implicitHeight: 8
                        radius: 4
                        color: ({idle: theme.success, syncing: theme.accent, paused: theme.warning,
                                 error: theme.danger, revoked: theme.danger, signed_out: theme.danger})[parent.state] || theme.ink
                    }
                    Controls.Label {
                        text: i18n("Status: %1", parent.state)
                        textFormat: Text.PlainText
                        font.pixelSize: theme.typeMeta
                        color: theme.ink
                    }
                }
            }

            Banner {
                theme: root.ky
                kind: "danger"
                readonly property string raw: Core.error !== "" ? Core.error : (Core.status.Error || "")
                visible: raw !== ""
                text: raw
            }

            // The large-delete guard: deletes wait here until the user answers.
            Repeater {
                model: root.linked ? (Core.status.Workspaces || []) : []
                delegate: Banner {
                    required property var modelData
                    theme: root.ky
                    kind: "warning"
                    visible: modelData.HeldDeletes > 0
                    text: i18n("%1 items were deleted from %2 on this computer.", modelData.HeldDeletes, modelData.Name)
                    KyButton {
                        theme: root.ky
                        kind: "danger"
                        text: i18n("Delete from KyDrive too")
                        onClicked: Core.confirmDeletes(modelData.ID, true)
                    }
                    KyButton {
                        theme: root.ky
                        text: i18n("Restore them")
                        onClicked: Core.confirmDeletes(modelData.ID, false)
                    }
                }
            }

            // Not linked: the browser does the sign-in and approval.
            Card {
                theme: root.ky
                visible: !root.linked
                Kirigami.FormLayout {
                    Layout.fillWidth: true
                    Controls.TextField {
                        id: server
                        implicitWidth: Kirigami.Units.gridUnit * 18
                        Kirigami.FormData.label: i18n("KyDrive address:")
                        placeholderText: "https://drive.example.com"
                        enabled: !Core.linking
                    }
                    Controls.TextField {
                        id: deviceName
                        implicitWidth: Kirigami.Units.gridUnit * 18
                        Kirigami.FormData.label: i18n("Computer name:")
                        placeholderText: i18n("This computer's name")
                        enabled: !Core.linking
                    }
                    KyButton {
                        theme: root.ky
                        kind: "primary"
                        text: Core.linking ? i18n("Waiting for approval in your browser…") : i18n("Link")
                        enabled: !Core.linking && server.text !== ""
                        onClicked: Core.link(server.text, deviceName.text)
                    }
                }
            }

            ColumnLayout {
                visible: root.linked
                Layout.fillWidth: true
                spacing: Kirigami.Units.smallSpacing * 2
                SectionTitle { theme: root.ky; text: i18n("Workspaces on this computer") }
                Card {
                    theme: root.ky
                    padding: 0
                    Repeater {
                        id: choices
                        model: root.linked ? Core.workspaces : []
                        // The name sits in its own plain-text label; a CheckBox's text may render markup.
                        delegate: Item {
                            required property var modelData
                            required property int index
                            property alias checked: box.checked
                            Layout.fillWidth: true
                            implicitHeight: 48
                            Hairline { theme: root.ky; visible: index > 0 }
                            RowLayout {
                                anchors.fill: parent
                                anchors.leftMargin: Kirigami.Units.gridUnit
                                anchors.rightMargin: Kirigami.Units.gridUnit
                                spacing: Kirigami.Units.smallSpacing * 2
                                Controls.CheckBox {
                                    id: box
                                    checked: modelData.Chosen
                                }
                                Controls.Label {
                                    Layout.fillWidth: true
                                    text: modelData.Name
                                    textFormat: Text.PlainText
                                    elide: Text.ElideRight
                                    font.pixelSize: theme.typeBody
                                    color: theme.inkStrong
                                }
                            }
                        }
                    }
                    Hairline { theme: root.ky; Layout.fillWidth: true }
                    ColumnLayout {
                        Layout.fillWidth: true
                        Layout.margins: Kirigami.Units.gridUnit
                        spacing: Kirigami.Units.gridUnit * 0.75
                        Controls.Label {
                            Layout.fillWidth: true
                            wrapMode: Text.WordWrap
                            font.pixelSize: theme.typeMeta
                            color: theme.ink
                            text: i18n("Unchecking a workspace removes its files from this computer. Edits that never uploaded are kept in the Unsynced folder.")
                        }
                        RowLayout {
                            spacing: Kirigami.Units.smallSpacing * 2
                            KyButton {
                                theme: root.ky
                                kind: "primary"
                                text: i18n("Apply")
                                onClicked: {
                                    let ids = []
                                    for (let i = 0; i < choices.count; i++)
                                        if (choices.itemAt(i).checked)
                                            ids.push(choices.itemAt(i).modelData.ID)
                                    Core.choose(ids)
                                }
                            }
                            KyButton {
                                theme: root.ky
                                text: i18n("Reload")
                                onClicked: Core.loadWorkspaces()
                            }
                        }
                    }
                }
            }

            ColumnLayout {
                visible: root.linked && Core.conflicts.length > 0
                Layout.fillWidth: true
                spacing: Kirigami.Units.smallSpacing * 2
                SectionTitle { theme: root.ky; text: i18n("Conflicts and kept files") }
                Card {
                    theme: root.ky
                    padding: 0
                    Repeater {
                        model: root.linked ? Core.conflicts : []
                        delegate: Item {
                            required property var modelData
                            required property int index
                            Layout.fillWidth: true
                            implicitHeight: conflict.implicitHeight + Kirigami.Units.gridUnit * 1.5
                            Hairline { theme: root.ky; visible: index > 0 }
                            ColumnLayout {
                                id: conflict
                                anchors.left: parent.left
                                anchors.right: parent.right
                                anchors.verticalCenter: parent.verticalCenter
                                anchors.leftMargin: Kirigami.Units.gridUnit
                                anchors.rightMargin: Kirigami.Units.gridUnit
                                spacing: 2
                                Controls.Label {
                                    Layout.fillWidth: true
                                    wrapMode: Text.Wrap
                                    font.pixelSize: theme.typeBody
                                    color: theme.inkStrong
                                    textFormat: Text.PlainText
                                    text: modelData.Path
                                }
                                Controls.Label {
                                    Layout.fillWidth: true
                                    wrapMode: Text.Wrap
                                    font.pixelSize: theme.typeMeta
                                    color: theme.ink
                                    textFormat: Text.PlainText
                                    text: modelData.Detail
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    // A section's sentence-case title, at ky-ui's section size.
    component SectionTitle: Controls.Label {
        required property QtObject theme
        Layout.fillWidth: true
        font.pixelSize: theme.typeSection
        font.weight: Font.DemiBold
        color: theme.inkStrong
    }
    // A 1px line between rows.
    component Hairline: Rectangle {
        required property QtObject theme
        width: parent ? parent.width : 0
        implicitHeight: 1
        color: theme.line
    }
    // A flat card: the panel, a 1px line and ky-ui's radius, no shadow.
    component Card: Rectangle {
        required property QtObject theme
        default property alias content: column.data
        property int padding: Kirigami.Units.gridUnit * 1.5
        Layout.fillWidth: true
        implicitHeight: column.implicitHeight + 2 * padding
        color: theme.panel
        radius: theme.radius
        border.width: 1
        border.color: theme.line
        ColumnLayout {
            id: column
            anchors.fill: parent
            anchors.margins: parent.padding
            spacing: 0
        }
    }

    AppearanceDialog {
        id: appearanceDialog
        theme: root.ky
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
                icon.color: theme.danger
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
