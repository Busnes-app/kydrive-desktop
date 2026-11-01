import QtQuick
import QtQuick.Controls as Controls
import QtQuick.Layouts
import org.kde.kirigami as Kirigami

// Theme pair and light/dark choice, saved by Appearance as soon as they change.
Kirigami.Dialog {
    id: dialog
    required property QtObject theme
    title: i18n("Appearance")
    standardButtons: Kirigami.Dialog.Close
    // ky-ui's card padding, so the form never touches the dialog's edge.
    padding: Kirigami.Units.gridUnit * 1.5
    preferredWidth: Kirigami.Units.gridUnit * 22
    // A popup takes neither the window's painted Theme nor its palette.
    palette.window: theme.panel
    palette.windowText: theme.inkStrong
    palette.base: theme.panel
    palette.text: theme.inkStrong
    palette.button: theme.panel
    palette.buttonText: theme.inkStrong
    palette.highlight: theme.accent
    palette.highlightedText: theme.buttonText
    Component.onCompleted: {
        for (const item of [dialog, dialog.header, dialog.contentItem, dialog.footer])
            if (item)
                dialog.theme.paint(item.Kirigami.Theme, () => dialog.theme.panel)
    }
    // The default background ignores a painted Theme.
    background: Kirigami.ShadowedRectangle {
        color: dialog.theme.panel
        radius: dialog.theme.radius
        border.color: dialog.theme.line
        border.width: 1
        shadow.size: Kirigami.Units.gridUnit
        shadow.color: Qt.rgba(0, 0, 0, 0.3)
    }

    // The title at ky-ui's section size, inset like the content; the close button stays.
    header: Controls.Control {
        leftPadding: dialog.padding
        rightPadding: dialog.padding / 2
        topPadding: dialog.padding
        contentItem: RowLayout {
            Controls.Label {
                Layout.fillWidth: true
                text: dialog.title
                font.pixelSize: dialog.theme.typeSection
                font.weight: Font.DemiBold
                color: dialog.theme.inkStrong
            }
            Controls.ToolButton {
                icon.name: "dialog-close"
                text: i18n("Close")
                display: Controls.AbstractButton.IconOnly
                onClicked: dialog.close()
            }
        }
    }

    // Radios mark the choice in the accent, as ky-ui's do.
    component AccentRadio: Controls.RadioButton {
        id: radio
        indicator: Rectangle {
            implicitWidth: 18
            implicitHeight: 18
            x: radio.leftPadding
            y: radio.topPadding + (radio.availableHeight - height) / 2
            radius: width / 2
            color: dialog.theme.panel
            border.width: radio.checked ? 2 : 1
            border.color: radio.checked || radio.visualFocus ? dialog.theme.accent : dialog.theme.ink
            Rectangle {
                anchors.centerIn: parent
                width: 8
                height: 8
                radius: 4
                color: dialog.theme.accent
                visible: radio.checked
            }
        }
    }

    Kirigami.FormLayout {
        Controls.ComboBox {
            Kirigami.FormData.label: i18n("Theme:")
            model: Appearance.pairs
            textRole: "name"
            valueRole: "id"
            currentIndex: Appearance.pairs.findIndex(p => p.id === Appearance.pair)
            onActivated: Appearance.pair = currentValue
        }
        AccentRadio {
            Kirigami.FormData.label: i18n("Light or dark:")
            text: i18n("Match system")
            checked: Appearance.mode === "system"
            onToggled: Appearance.mode = "system"
        }
        AccentRadio {
            text: i18n("Light")
            checked: Appearance.mode === "light"
            onToggled: Appearance.mode = "light"
        }
        AccentRadio {
            text: i18n("Dark")
            checked: Appearance.mode === "dark"
            onToggled: Appearance.mode = "dark"
        }
    }
}
