import QtQuick
import QtQuick.Controls as Controls

// The suite's text field: the theme box's control radius, line and panel, KyButton's height,
// and the accent ring while it has focus. Disabled, it dims as a secondary button does.
Controls.TextField {
    id: control
    required property QtObject theme

    implicitHeight: 38
    leftPadding: 12
    rightPadding: 12
    verticalAlignment: TextInput.AlignVCenter
    font.pixelSize: theme.typeBody
    color: theme.inkStrong
    placeholderTextColor: theme.ink
    selectionColor: theme.accent
    selectedTextColor: theme.buttonText
    opacity: enabled ? 1 : 0.6

    background: Rectangle {
        radius: control.theme.controlRadius
        color: control.theme.panel
        border.width: control.activeFocus ? 2 : 1
        border.color: control.activeFocus ? control.theme.accent
                    : control.hovered ? Qt.tint(control.theme.line, Qt.alpha(control.theme.inkStrong, 0.25))
                    : control.theme.line
    }
}
