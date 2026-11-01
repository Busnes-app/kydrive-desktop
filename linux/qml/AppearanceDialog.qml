import QtQuick
import QtQuick.Controls as Controls
import QtQuick.Layouts
import org.kde.kirigami as Kirigami

// Theme pair and light/dark choice, saved by Appearance.
Kirigami.ScrollablePage {
    id: page
    required property QtObject theme
    title: i18n("Appearance")
    background: Rectangle { color: page.theme.bg }
    Component.onCompleted: page.theme.paint(Kirigami.Theme, () => page.theme.bg)
    onGlobalToolBarItemChanged: if (globalToolBarItem) page.theme.paint(globalToolBarItem.Kirigami.Theme, () => page.theme.panel)

    Kirigami.FormLayout {
        Controls.ComboBox {
            Kirigami.FormData.label: i18n("Theme:")
            model: Appearance.pairs
            textRole: "name"
            valueRole: "id"
            currentIndex: indexOfValue(Appearance.pair)
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
