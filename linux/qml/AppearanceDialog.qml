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
    padding: Kirigami.Units.largeSpacing
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
        radius: Kirigami.Units.cornerRadius
        border.color: dialog.theme.line
        border.width: 1
        shadow.size: Kirigami.Units.gridUnit
        shadow.color: Qt.rgba(0, 0, 0, 0.3)
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
        Controls.RadioButton {
            Kirigami.FormData.label: i18n("Light or dark:")
            text: i18n("Match system")
            checked: Appearance.mode === "system"
            onToggled: Appearance.mode = "system"
        }
        Controls.RadioButton {
            text: i18n("Light")
            checked: Appearance.mode === "light"
            onToggled: Appearance.mode = "light"
        }
        Controls.RadioButton {
            text: i18n("Dark")
            checked: Appearance.mode === "dark"
            onToggled: Appearance.mode = "dark"
        }
    }
}
