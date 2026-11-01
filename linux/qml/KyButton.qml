import QtQuick
import QtQuick.Controls as Controls

// The suite's button hierarchy: one filled "primary" per group, the rest "secondary";
// destructive actions are "danger".
Controls.Button {
    id: control
    required property QtObject theme
    property string kind: "secondary"
    readonly property bool primary: kind === "primary" && enabled
    readonly property color edge: kind === "danger" ? Qt.alpha(theme.danger, 0.55) : theme.line

    implicitHeight: 36
    leftPadding: 16
    rightPadding: 16
    font.pixelSize: theme.typeNav
    font.weight: Font.DemiBold
    opacity: enabled || kind === "primary" ? 1 : 0.6

    contentItem: Controls.Label {
        text: control.text
        textFormat: Text.PlainText
        font: control.font
        horizontalAlignment: Text.AlignHCenter
        verticalAlignment: Text.AlignVCenter
        elide: Text.ElideRight
        color: control.primary ? control.theme.buttonText
             : !control.enabled ? control.theme.ink
             : control.kind === "danger" ? control.theme.danger : control.theme.inkStrong
    }
    background: Rectangle {
        radius: control.theme.controlRadius
        color: control.primary
            ? (control.hovered || control.down ? Qt.tint(control.theme.accent, Qt.alpha(control.theme.inkStrong, 0.14)) : control.theme.accent)
            : (control.hovered || control.down ? Qt.tint(control.theme.panel, Qt.alpha(control.theme.inkStrong, 0.06)) : control.theme.panel)
        border.width: control.visualFocus ? 2 : (control.primary ? 0 : 1)
        border.color: control.visualFocus ? control.theme.accent : control.edge
    }
}
