import SwiftUI
import XCTest
@testable import KyDriveKit

// ky-ui scripts/check-status-colours.mjs, ported: WCAG contrast and CIEDE2000.
private func linear(_ v: Double) -> Double {
    let c = v / 255
    return c <= 0.04045 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4)
}

private func luminance(_ c: RGB) -> Double {
    let r: Double = 0.2126 * linear(c.r), g: Double = 0.7152 * linear(c.g), b: Double = 0.0722 * linear(c.b)
    return r + g + b
}

func contrast(_ a: RGB, _ b: RGB) -> Double {
    let la = luminance(a), lb = luminance(b)
    return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

private func lab(_ c: RGB) -> (Double, Double, Double) {
    let r = linear(c.r), g = linear(c.g), b = linear(c.b)
    func f(_ t: Double) -> Double { t > 216.0 / 24389 ? cbrt(t) : (24389.0 / 27 * t + 16) / 116 }
    let x: Double = 0.4124564 * r + 0.3575761 * g + 0.1804375 * b
    let y: Double = 0.2126729 * r + 0.7151522 * g + 0.072175 * b
    let z: Double = 0.0193339 * r + 0.119192 * g + 0.9503041 * b
    let xyz: [Double] = [f(x / 0.95047), f(y), f(z / 1.08883)]
    return (116 * xyz[1] - 16, 500 * (xyz[0] - xyz[1]), 200 * (xyz[1] - xyz[2]))
}

// Sharma, Wu and Dalal (2005), kL = kC = kH = 1.
func deltaE(_ x: RGB, _ y: RGB) -> Double {
    let (L1, a1, b1) = lab(x), (L2, a2, b2) = lab(y)
    let rad = Double.pi / 180
    let C = (hypot(a1, b1) + hypot(a2, b2)) / 2
    let G: Double = 0.5 * (1 - sqrt(pow(C, 7) / (pow(C, 7) + pow(25, 7))))
    let ap1 = a1 * (1 + G), ap2 = a2 * (1 + G)
    let Cp1 = hypot(ap1, b1), Cp2 = hypot(ap2, b2)
    func hue(_ b: Double, _ a: Double) -> Double { (atan2(b, a) / rad + 360).truncatingRemainder(dividingBy: 360) }
    let hp1 = hue(b1, ap1), hp2 = hue(b2, ap2)
    let dL = L2 - L1, dC = Cp2 - Cp1
    var dh = Cp1 * Cp2 == 0 ? 0 : hp2 - hp1
    if dh > 180 { dh -= 360 } else if dh < -180 { dh += 360 }
    let dH = 2 * sqrt(Cp1 * Cp2) * sin(dh * rad / 2)
    let Lm = (L1 + L2) / 2, Cm = (Cp1 + Cp2) / 2
    var hm = hp1 + hp2
    if Cp1 * Cp2 != 0 { hm = abs(hp1 - hp2) > 180 ? (hm + (hm < 360 ? 360 : -360)) / 2 : hm / 2 }
    let t1: Double = 0.17 * cos((hm - 30) * rad), t2: Double = 0.24 * cos(2 * hm * rad)
    let t3: Double = 0.32 * cos((3 * hm + 6) * rad), t4: Double = 0.2 * cos((4 * hm - 63) * rad)
    let T: Double = 1 - t1 + t2 + t3 - t4
    let SL: Double = 1 + 0.015 * pow(Lm - 50, 2) / sqrt(20 + pow(Lm - 50, 2))
    let SC = 1 + 0.045 * Cm, SH = 1 + 0.015 * Cm * T
    let RC: Double = -2 * sqrt(pow(Cm, 7) / (pow(Cm, 7) + pow(25, 7)))
    let RT: Double = RC * sin(60 * exp(-pow((hm - 275) / 25, 2)) * rad)
    let l: Double = dL / SL, c: Double = dC / SC, h: Double = dH / SH
    return sqrt(l * l + c * c + h * h + RT * c * h)
}

@MainActor
final class AppearanceTests: XCTestCase {
    func testMigration() {
        let cases: [(String, String, ThemeMode)] = [
            ("busnes-light", "busnes-pair", .light), ("busnes-dark", "busnes-pair", .dark),
            ("Busnes Light", "busnes-pair", .light), ("Busnes Dark", "busnes-pair", .dark),
            ("dark-matter", "matter-pair", .dark), ("White Cliffs", "ocean-pair", .light),
            ("nord-pair", "nord-pair", .system), ("system", "busnes-pair", .system), ("", "busnes-pair", .system),
        ] + ["tropics", "tropic-night", "coffee", "cyber-punk", "neon-purple", "space", "sky", "sun", "cyber", "oled"]
            .map { ($0, "busnes-pair", .system) }
        for (saved, pair, mode) in cases {
            XCTAssertEqual(migrateTheme(saved), ThemeChoice(pair: pair, mode: mode), saved)
        }
    }

    private func defaults() -> UserDefaults {
        let name = "kydrive-tests-" + UUID().uuidString
        let d = UserDefaults(suiteName: name)!
        addTeardownBlock { d.removePersistentDomain(forName: name) }
        return d
    }

    // A legacy "theme" is migrated once and removed; the new keys win from then on.
    func testStoreMigratesOnce() {
        let d = defaults()
        d.set("busnes-dark", forKey: AppearanceStore.legacyKey)
        XCTAssertEqual(AppearanceStore.load(d), ThemeChoice(pair: "busnes-pair", mode: .dark))
        XCTAssertNil(d.object(forKey: AppearanceStore.legacyKey))
        d.set("coffee", forKey: AppearanceStore.legacyKey)
        d.set("ocean-pair", forKey: AppearanceStore.pairKey)
        XCTAssertEqual(AppearanceStore.load(d), ThemeChoice(pair: "ocean-pair", mode: .dark))
    }

    func testStoreResetsRemovedAndUnknown() {
        let d = defaults()
        d.set("neon-purple", forKey: AppearanceStore.legacyKey)
        XCTAssertEqual(AppearanceStore.load(d), ThemeChoice(pair: "busnes-pair", mode: .system))
        let e = defaults()
        e.set("coffee-pair", forKey: AppearanceStore.pairKey)
        e.set("sepia", forKey: AppearanceStore.modeKey)
        XCTAssertEqual(AppearanceStore.load(e), ThemeChoice(pair: "busnes-pair", mode: .system))
        XCTAssertEqual(AppearanceStore.load(defaults()), ThemeChoice(pair: "busnes-pair", mode: .system))
    }

    func testHalfFollowsSystemUnlessForced() {
        let busnes = ThemeChoice(pair: "busnes-pair", mode: .system)
        XCTAssertEqual(themeHalf(busnes, systemDark: false).id, "busnes-light")
        XCTAssertEqual(themeHalf(busnes, systemDark: true).id, "busnes-dark")
        XCTAssertEqual(themeHalf(ThemeChoice(pair: "busnes-pair", mode: .light), systemDark: true).id, "busnes-light")
        XCTAssertEqual(themeHalf(ThemeChoice(pair: "forest-pair", mode: .dark), systemDark: false).success, RGB(0x6ee0d0))
    }

    // The status colour gate: every half, every check, as ky-ui's script.
    func testStatusColours() {
        var checks = 0, failures: [String] = []
        func check(_ id: String, _ what: String, _ value: Double, _ min: Double) {
            checks += 1
            if !(value >= min) { failures.append(id + ": " + what + " " + String(format: "%.2f < %.1f", value, min)) }
        }
        for p in themePairs {
            XCTAssertFalse(p.light.dark)
            XCTAssertTrue(p.dark.dark)
            for h in [p.light, p.dark] {
                for (name, c) in [("danger", h.danger), ("warning", h.warning), ("success", h.success)] {
                    check(h.id, name + " on bg", contrast(c, h.bg), 4.5)
                    check(h.id, name + " on panel", contrast(c, h.panel), 4.5)
                    check(h.id, name + " on its tint", contrast(c, h.soft(c)), 4.5)
                }
                check(h.id, "ΔE accent–danger", deltaE(h.accent, h.danger), 20)
                for (a, b) in [(h.accent, h.warning), (h.accent, h.success), (h.danger, h.warning), (h.danger, h.success), (h.warning, h.success)] {
                    check(h.id, "ΔE " + String(a.hex, radix: 16) + "–" + String(b.hex, radix: 16), deltaE(a, b), 10)
                }
            }
        }
        XCTAssertEqual(failures, [])
        XCTAssertEqual(checks, 180)
    }

    // Spot values from ky-ui's own table (check-status-colours.mjs --table), to prove the port.
    func testPortMatchesKyUi() {
        XCTAssertEqual((contrast(RGB(0xa8174e), RGB(0xf8f6f0)) * 100).rounded(), 671)
        XCTAssertEqual((deltaE(RGB(0xbf3f18), RGB(0xa8174e)) * 100).rounded(), 2388)
    }

    // With KYDRIVE_SHOT_DIR set, saves the menu and the Settings window in three halves.
    func testScreenshots() async throws {
        guard let dir = ProcessInfo.processInfo.environment["KYDRIVE_SHOT_DIR"] else {
            throw XCTSkip("set KYDRIVE_SHOT_DIR to save screenshots")
        }
        let fake = try FakeCore()
        defer { fake.stop() }
        var status = FakeCore.status
        status["Error"] = "Could not reach drive.example.com"
        status["Workspaces"] = [["ID": "w1", "Name": "Docs", "Dir": "Docs", "HeldDeletes": 26]]
        fake.answer("Core.Status", status)
        fake.answer("Core.Workspaces", [["ID": "w1", "Name": "Docs", "Kind": "team", "Role": "editor", "Chosen": true],
                                        ["ID": "w2", "Name": "Projects", "Kind": "team", "Role": "editor", "Chosen": false]])
        fake.answer("Core.Conflicts", [["ID": 1, "Workspace": "w1", "Path": "report (conflict from laptop 2026-10-18).txt", "Kind": "conflict",
                                        "Detail": "edited here and on the server; both kept", "Created": 0]])
        let model = AppModel(core: CoreClient(socketPath: fake.path))
        await model.refresh()
        await model.loadWorkspaces()
        // The bundle's icon, which the brand row shows; a test process has none of its own.
        let icon = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("packaging/AppIcon.iconset/icon_32x32@2x.png")
        NSApplication.shared.applicationIconImage = NSImage(contentsOf: icon)
        for (pair, mode, name) in [("busnes-pair", ThemeMode.light, "busnes-light"), ("busnes-pair", .dark, "busnes-dark"), ("forest-pair", .light, "forest-light")] {
            let d = defaults()
            d.set(pair, forKey: AppearanceStore.pairKey)
            d.set(mode.rawValue, forKey: AppearanceStore.modeKey)
            try snapshot(MenuContent(model: model).defaultAppStorage(d), dark: mode == .dark, to: dir + "/main-" + name + ".png")
            try snapshot(AppearanceSettings().defaultAppStorage(d), dark: mode == .dark, to: dir + "/settings-" + name + ".png")
        }
    }
}

// The window must be shown for every layer to draw; the PNG keeps the view's own background.
@MainActor
func snapshot<V: View>(_ view: V, dark: Bool, to path: String) throws {
    _ = NSApplication.shared
    let host = NSHostingView(rootView: view)
    let window = NSWindow(contentRect: CGRect(x: 0, y: 0, width: 360, height: 600), styleMask: [.titled], backing: .buffered, defer: false)
    window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
    window.contentView = host
    window.setContentSize(host.fittingSize)
    window.orderFrontRegardless()
    RunLoop.main.run(until: Date().addingTimeInterval(1.5))
    host.display()
    let rep = host.bitmapImageRepForCachingDisplay(in: host.bounds)!
    host.cacheDisplay(in: host.bounds, to: rep)
    window.orderOut(nil)
    try rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: path))
}
