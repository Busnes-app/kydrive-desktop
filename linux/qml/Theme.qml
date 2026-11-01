import QtQuick

// The chosen suite theme pair and light/dark half (Appearance = src/Appearance.cpp, which holds
// ky-ui's values). Status colours never reuse the accent; show each with a word and an icon or dot.
QtObject {
    readonly property var c: Appearance.colors
    readonly property bool dark: Appearance.dark
    readonly property color bg: c.bg
    readonly property color panel: c.panel
    readonly property color ink: c.ink
    readonly property color inkStrong: c.inkStrong
    readonly property color accent: c.accent
    readonly property color accentSoft: c.accentSoft
    readonly property color line: c.line
    readonly property color buttonText: c.buttonText
    readonly property color danger: c.danger
    readonly property color warning: c.warning
    readonly property color success: c.success

    // ky-ui's type scale (--ky-type-*, at 16px to the rem), card and control radii.
    readonly property int typeTitle: 24
    readonly property int typeSection: 17
    readonly property int typeBody: 15
    readonly property int typeNav: 14
    readonly property int typeMeta: 13
    readonly property int radius: 10
    readonly property int controlRadius: 8

    // Kirigami paints from the platform palette unless told otherwise.
    function paint(theme, background) {
        theme.inherit = false
        theme.backgroundColor = Qt.binding(background)
        theme.alternateBackgroundColor = Qt.binding(() => panel)
        theme.textColor = Qt.binding(() => inkStrong)
        theme.disabledTextColor = Qt.binding(() => ink)
        theme.highlightColor = Qt.binding(() => accent)
        theme.highlightedTextColor = Qt.binding(() => buttonText)
        theme.hoverColor = Qt.binding(() => accent)
        theme.focusColor = Qt.binding(() => accent)
        theme.linkColor = Qt.binding(() => accent)
        theme.negativeTextColor = Qt.binding(() => danger)
        theme.neutralTextColor = Qt.binding(() => warning)
        theme.positiveTextColor = Qt.binding(() => success)
        theme.negativeBackgroundColor = Qt.binding(() => c.dangerSoft)
        theme.neutralBackgroundColor = Qt.binding(() => c.warningSoft)
        theme.positiveBackgroundColor = Qt.binding(() => c.successSoft)
    }
}
