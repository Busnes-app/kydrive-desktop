import QtQuick
import QtQuick.Controls as Controls
import QtQuick.Layouts
import org.kde.kirigami as Kirigami

// The one status message: the status tint, a 1px status edge, an icon and plain text, then any
// buttons. Errors ("danger") and held deletes ("warning") both use it, as on macOS.
Rectangle {
    id: banner
    required property QtObject theme
    property string kind: "danger"
    property string text
    default property alias buttons: row.data
    readonly property color tone: kind === "warning" ? theme.warning : theme.danger

    Layout.fillWidth: true
    implicitHeight: body.implicitHeight + 2 * Kirigami.Units.gridUnit * 0.75
    radius: theme.radius
    color: kind === "warning" ? theme.c.warningSoft : theme.c.dangerSoft
    border.width: 1
    border.color: Qt.alpha(tone, 0.45)

    ColumnLayout {
        id: body
        anchors.fill: parent
        anchors.margins: Kirigami.Units.gridUnit * 0.75
        spacing: 12
        RowLayout {
            spacing: 10
            Kirigami.Icon {
                Layout.alignment: Qt.AlignTop
                Layout.topMargin: 2
                implicitWidth: 16
                implicitHeight: 16
                source: banner.kind === "warning" ? "dialog-warning-symbolic" : "dialog-error-symbolic"
                isMask: true
                color: banner.tone
            }
            Controls.Label {
                Layout.fillWidth: true
                text: banner.text
                textFormat: Text.PlainText
                wrapMode: Text.Wrap
                font.pixelSize: banner.theme.typeBody
                color: banner.theme.inkStrong
            }
        }
        RowLayout {
            id: row
            visible: children.length > 0
            Layout.leftMargin: 26
            spacing: 8
        }
    }
}
