import SwiftUI

// The suite's six light/dark theme pairs (ky-ui 0.11.0 src/themes.js THEME_PAIRS and
// src/tokens.css). Copy palette values from ky-ui, never approximate them. Status colours never
// reuse the accent; show each with a word and an icon or dot.

/// A colour as 0xRRGGBB plus alpha.
public struct RGB: Equatable {
    public let hex: UInt32
    public let alpha: Double
    init(_ hex: UInt32, alpha: Double = 1) { self.hex = hex; self.alpha = alpha }
    public var r: Double { Double(hex >> 16 & 0xff) }
    public var g: Double { Double(hex >> 8 & 0xff) }
    public var b: Double { Double(hex & 0xff) }
    public var color: Color { Color(.sRGB, red: r / 255, green: g / 255, blue: b / 255, opacity: alpha) }
}

public struct ThemeHalf {
    public let id: String
    public let dark: Bool
    public let bg, panel, ink, inkStrong, accent, accentSoft, line, buttonText, danger, warning, success: RGB

    /// Status pill fill: the status mixed 12% over the panel, as ky-ui's --ky-*-soft.
    public func soft(_ status: RGB) -> RGB {
        func mix(_ s: Double, _ p: Double) -> UInt32 { UInt32((s * 0.12 + p * 0.88).rounded()) }
        return RGB(mix(status.r, panel.r) << 16 | mix(status.g, panel.g) << 8 | mix(status.b, panel.b))
    }
}

public struct ThemePair: Identifiable {
    public let id, name: String
    public let light, dark: ThemeHalf
}

// Shared status colours by scheme (tokens.css --ky-{danger,success,warning}-{light,dark}).
private let dangerLight = RGB(0xa8174e), successLight = RGB(0x267442), warningLight = RGB(0x7a5f00)
private let dangerDark = RGB(0xff8fb1), successDark = RGB(0x86efac), warningDark = RGB(0xffd25e)

private func light(_ id: String, _ bg: UInt32, _ panel: UInt32, _ ink: UInt32, _ inkStrong: UInt32, _ accent: UInt32,
                   _ accentSoft: UInt32, _ line: RGB, _ buttonText: UInt32, success: RGB = successLight) -> ThemeHalf {
    ThemeHalf(id: id, dark: false, bg: RGB(bg), panel: RGB(panel), ink: RGB(ink), inkStrong: RGB(inkStrong), accent: RGB(accent),
              accentSoft: RGB(accentSoft), line: line, buttonText: RGB(buttonText), danger: dangerLight, warning: warningLight, success: success)
}

private func dark(_ id: String, _ bg: UInt32, _ panel: UInt32, _ ink: UInt32, _ inkStrong: UInt32, _ accent: UInt32,
                  _ accentSoft: RGB, _ line: RGB, _ buttonText: UInt32, success: RGB = successDark, danger: RGB = dangerDark) -> ThemeHalf {
    ThemeHalf(id: id, dark: true, bg: RGB(bg), panel: RGB(panel), ink: RGB(ink), inkStrong: RGB(inkStrong), accent: RGB(accent),
              accentSoft: accentSoft, line: line, buttonText: RGB(buttonText), danger: danger, warning: warningDark, success: success)
}

/// Busnes from tokens.css (line is ink-strong at 0.22 / 0.24); the rest from THEME_PALETTES.
public let themePairs: [ThemePair] = [
    ThemePair(id: "busnes-pair", name: "Busnes",
              light: light("busnes-light", 0xf8f6f0, 0xffffff, 0x566461, 0x182326, 0xbf3f18, 0xfbf0ec, RGB(0x182326, alpha: 0.22), 0xffffff),
              dark: dark("busnes-dark", 0x182326, 0x1f2b2e, 0xb3bcb8, 0xf2efe8, 0xf5865f, RGB(0x2b2622), RGB(0xf2efe8, alpha: 0.24), 0x182326)),
    ThemePair(id: "matter-pair", name: "Matter",
              light: light("light-matter", 0xf5efe5, 0xfff8ee, 0x4c3d32, 0x2d1f15, 0x805a39, 0xe2d0b8, RGB(0xd4c7b5), 0xffffff),
              dark: dark("dark-matter", 0x1d1b19, 0x292725, 0xcfc6bb, 0xf0e9df, 0xc29a72, RGB(0x49392c), RGB(0x45403a), 0x24170f)),
    ThemePair(id: "ocean-pair", name: "Ocean",
              light: light("white-cliffs", 0xf7f9fb, 0xffffff, 0x2e4c63, 0x163246, 0x1d5fa6, 0xdde9f7, RGB(0xc7d6df), 0xffffff),
              dark: dark("ocean", 0x0f1b24, 0x152a36, 0xb8d8e8, 0xe0f2fb, 0x6aa8e8, RGB(0x1f3a55), RGB(0x2f5567), 0x0a1b22)),
    ThemePair(id: "forest-pair", name: "Forest",
              light: light("forest-light", 0xf3f6ef, 0xfcfdf9, 0x435342, 0x203322, 0x4b6b37, 0xe2ebda, RGB(0xc5d0bc), 0xffffff, success: RGB(0x00737a)),
              dark: dark("forest", 0x142018, 0x1f2f24, 0xc7dbc7, 0xe3f0df, 0x8faa74, RGB(0x3a5837), RGB(0x4f694f), 0x12200f, success: RGB(0x6ee0d0))),
    ThemePair(id: "ky-pair", name: "Ky",
              light: light("polished-ky", 0xeef2f6, 0xffffff, 0x475569, 0x0f172a, 0x08758b, 0xdaf2f4, RGB(0xc2cdd8), 0xffffff),
              dark: dark("patina-ky", 0x10161e, 0x1b2530, 0xafbdca, 0xe2e8f0, 0x6fc9cc, RGB(0x203d41), RGB(0x34414d), 0x10161e)),
    ThemePair(id: "nord-pair", name: "Nord",
              light: light("paper", 0xeceff4, 0xffffff, 0x4c566a, 0x2e3440, 0x4a6a94, 0xdbe4ef, RGB(0xc5cdd9), 0xffffff),
              dark: dark("nord", 0x2e3440, 0x3b4252, 0xd8dee9, 0xeceff4, 0x88c0d0, RGB(0x88c0d0, alpha: 0.2), RGB(0x4c566a), 0x0d0f14,
                         success: RGB(0xbcd4aa), danger: RGB(0xecc4c8))),
]

public enum ThemeMode: String, CaseIterable, Identifiable {
    case system, light, dark
    public var id: String { rawValue }
    public var label: String {
        switch self {
        case .system: "Match system"
        case .light: "Light"
        case .dark: "Dark"
        }
    }
}

public struct ThemeChoice: Equatable {
    public var pair: String
    public var mode: ThemeMode
}

// Old names and aliases from THEME_CATALOG, so a saved name finds its half.
private let themeAliases: [String: String] = [
    "Busnes Light": "busnes-light", "Busnes Dark": "busnes-dark",
    "Matter Light": "light-matter", "Light Matter": "light-matter",
    "Matter Dark": "dark-matter", "Dark Matter": "dark-matter", "Current": "dark-matter",
    "Ocean Light": "white-cliffs", "White Cliffs": "white-cliffs", "Cliffs": "white-cliffs",
    "Ocean Dark": "ocean", "Ocean": "ocean",
    "Forest Light": "forest-light", "Forest Dark": "forest", "Forest": "forest",
    "Ky Light": "polished-ky", "Polished Ky": "polished-ky",
    "Ky Dark": "patina-ky", "Patina Ky": "patina-ky", "patina": "patina-ky", "Patina Ky (base)": "patina-ky",
    "Nord Light": "paper", "Paper Clean": "paper",
    "Nord Dark": "nord", "Nord": "nord", "Nord Slate": "nord",
]

/// Maps a saved suite theme (an ID, name or alias from ky-ui's THEME_CATALOG, or a pair ID) to a
/// pair and mode. A half keeps its pair with that half forced; anything removed or unknown is
/// Busnes following the system.
public func migrateTheme(_ saved: String) -> ThemeChoice {
    if themePairs.contains(where: { $0.id == saved }) { return ThemeChoice(pair: saved, mode: .system) }
    let id = themeAliases[saved] ?? saved
    for p in themePairs {
        if p.light.id == id { return ThemeChoice(pair: p.id, mode: .light) }
        if p.dark.id == id { return ThemeChoice(pair: p.id, mode: .dark) }
    }
    return ThemeChoice(pair: "busnes-pair", mode: .system)
}

/// The saved pair and mode in UserDefaults (appearancePair, appearanceMode). A legacy "theme"
/// value is migrated once and removed; unknown values fall back to Busnes following the system.
public enum AppearanceStore {
    public static let pairKey = "appearancePair", modeKey = "appearanceMode", legacyKey = "theme"

    public static func load(_ defaults: UserDefaults = .standard) -> ThemeChoice {
        if let legacy = defaults.string(forKey: legacyKey), defaults.object(forKey: pairKey) == nil {
            let choice = migrateTheme(legacy)
            defaults.removeObject(forKey: legacyKey)
            defaults.set(choice.pair, forKey: pairKey)
            defaults.set(choice.mode.rawValue, forKey: modeKey)
        }
        let pair = defaults.string(forKey: pairKey).flatMap { id in themePairs.first { $0.id == id }?.id } ?? "busnes-pair"
        return ThemeChoice(pair: pair, mode: defaults.string(forKey: modeKey).flatMap(ThemeMode.init) ?? .system)
    }
}

/// The half to paint for a choice and the system's scheme.
public func themeHalf(_ choice: ThemeChoice, systemDark: Bool) -> ThemeHalf {
    let pair = themePairs.first { $0.id == choice.pair } ?? themePairs[0]
    switch choice.mode {
    case .light: return pair.light
    case .dark: return pair.dark
    case .system: return systemDark ? pair.dark : pair.light
    }
}

private struct ThemeHalfKey: EnvironmentKey {
    static let defaultValue = themePairs[0].light
}

extension EnvironmentValues {
    var themeHalf: ThemeHalf {
        get { self[ThemeHalfKey.self] }
        set { self[ThemeHalfKey.self] = newValue }
    }
}

/// Paints content with the saved theme: forces the half's scheme, fills its background and
/// passes the half down for status colours.
struct Themed: ViewModifier {
    @AppStorage(AppearanceStore.pairKey) private var pair = "busnes-pair"
    @AppStorage(AppearanceStore.modeKey) private var mode = ThemeMode.system.rawValue
    @Environment(\.colorScheme) private var scheme

    func body(content: Content) -> some View {
        let choice = ThemeChoice(pair: pair, mode: ThemeMode(rawValue: mode) ?? .system)
        // With .system the forced scheme is nil, so `scheme` is the system's.
        let half = themeHalf(choice, systemDark: scheme == .dark)
        content
            .environment(\.themeHalf, half)
            .foregroundStyle(half.inkStrong.color)
            .tint(half.accent.color)
            .background(half.bg.color)
            .preferredColorScheme(choice.mode == .system ? nil : (half.dark ? .dark : .light))
    }
}

public extension View {
    func kyThemed() -> some View { modifier(Themed()) }
}

/// The Settings window: theme pair and light/dark choice.
public struct AppearanceSettings: View {
    @AppStorage(AppearanceStore.pairKey) private var pair = "busnes-pair"
    @AppStorage(AppearanceStore.modeKey) private var mode = ThemeMode.system.rawValue

    public init() {}

    public var body: some View {
        Form {
            Picker("Theme", selection: $pair) {
                ForEach(themePairs) { Text(verbatim: $0.name).tag($0.id) }
            }
            LabeledContent("Light or dark") {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(ThemeMode.allCases) { m in AccentRadio(label: m.label, isOn: mode == m.rawValue) { mode = m.rawValue } }
                }
            }
        }
        .padding(24)
        .frame(width: 360)
        .kyThemed()
    }
}

/// A radio that marks the choice in the theme's accent; the system radio ignores the tint.
struct AccentRadio: View {
    let label: String
    let isOn: Bool
    let select: () -> Void
    @Environment(\.themeHalf) private var half
    var body: some View {
        Button(action: select) {
            HStack(spacing: 8) {
                Circle().strokeBorder((isOn ? half.accent : half.ink).color, lineWidth: isOn ? 2 : 1)
                    .background(Circle().fill(half.panel.color))
                    .overlay(Circle().fill(half.accent.color).frame(width: 6, height: 6).opacity(isOn ? 1 : 0))
                    .frame(width: 16, height: 16)
                Text(verbatim: label)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(isOn ? [.isSelected] : [])
    }
}
