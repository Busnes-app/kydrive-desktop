import QtQuick

// The suite's default palettes ("Dark Matter" / "Light Matter" from KyPost's AppTheme),
// following the system light or dark preference.
QtObject {
    readonly property bool dark: Qt.styleHints.colorScheme !== Qt.ColorScheme.Light
    readonly property color bg: dark ? "#1A1A1E" : "#F5EFE5"
    readonly property color panel: dark ? "#252530" : "#FFF8EE"
    readonly property color ink: dark ? "#D4C5E2" : "#4C3D32"
    readonly property color inkStrong: dark ? "#E8DDF5" : "#2D1F15"
    readonly property color accent: "#C29A72"
    readonly property color line: dark ? "#404050" : "#C5B29D"

    // Kirigami paints from the platform palette unless told otherwise.
    function paint(theme, background) {
        theme.inherit = false
        theme.backgroundColor = Qt.binding(background)
        theme.alternateBackgroundColor = Qt.binding(() => panel)
        theme.textColor = Qt.binding(() => inkStrong)
        theme.disabledTextColor = Qt.binding(() => ink)
        theme.highlightColor = Qt.binding(() => accent)
        theme.highlightedTextColor = Qt.binding(() => dark ? "#1A1A1E" : "#FFFFFF")
        theme.hoverColor = Qt.binding(() => accent)
        theme.focusColor = Qt.binding(() => accent)
        theme.linkColor = Qt.binding(() => accent)
    }
}
