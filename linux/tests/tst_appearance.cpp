#include "Appearance.h"

#include <QTemporaryDir>
#include <QTest>
#include <cmath>

namespace {
// ky-ui scripts/check-status-colours.mjs, ported: WCAG contrast and CIEDE2000.
double linear(double v)
{
    v /= 255;
    return v <= 0.04045 ? v / 12.92 : std::pow((v + 0.055) / 1.055, 2.4);
}

double luminance(const QColor& c)
{
    return 0.2126 * linear(c.red()) + 0.7152 * linear(c.green()) + 0.0722 * linear(c.blue());
}

double contrast(const QColor& a, const QColor& b)
{
    const double la = luminance(a), lb = luminance(b);
    return (std::max(la, lb) + 0.05) / (std::min(la, lb) + 0.05);
}

std::array<double, 3> lab(const QColor& c)
{
    const double r = linear(c.red()), g = linear(c.green()), b = linear(c.blue());
    double xyz[3] = {(0.4124564 * r + 0.3575761 * g + 0.1804375 * b) / 0.95047, 0.2126729 * r + 0.7151522 * g + 0.072175 * b,
                     (0.0193339 * r + 0.119192 * g + 0.9503041 * b) / 1.08883};
    for (double& t : xyz)
        t = t > 216.0 / 24389 ? std::cbrt(t) : (24389.0 / 27 * t + 16) / 116;
    return {116 * xyz[1] - 16, 500 * (xyz[0] - xyz[1]), 200 * (xyz[1] - xyz[2])};
}

// Sharma, Wu and Dalal (2005), kL = kC = kH = 1.
double deltaE(const QColor& x, const QColor& y)
{
    const auto [L1, a1, b1] = lab(x);
    const auto [L2, a2, b2] = lab(y);
    const double rad = M_PI / 180;
    const double C = (std::hypot(a1, b1) + std::hypot(a2, b2)) / 2;
    const double G = 0.5 * (1 - std::sqrt(std::pow(C, 7) / (std::pow(C, 7) + std::pow(25, 7))));
    const double ap1 = a1 * (1 + G), ap2 = a2 * (1 + G);
    const double Cp1 = std::hypot(ap1, b1), Cp2 = std::hypot(ap2, b2);
    const auto hue = [&](double b, double a) { return std::fmod(std::atan2(b, a) / rad + 360, 360); };
    const double hp1 = hue(b1, ap1), hp2 = hue(b2, ap2);
    const double dL = L2 - L1, dC = Cp2 - Cp1;
    double dh = Cp1 * Cp2 == 0 ? 0 : hp2 - hp1;
    if (dh > 180)
        dh -= 360;
    else if (dh < -180)
        dh += 360;
    const double dH = 2 * std::sqrt(Cp1 * Cp2) * std::sin(dh * rad / 2);
    const double Lm = (L1 + L2) / 2, Cm = (Cp1 + Cp2) / 2;
    double hm = hp1 + hp2;
    if (Cp1 * Cp2 != 0)
        hm = std::abs(hp1 - hp2) > 180 ? (hm + (hm < 360 ? 360 : -360)) / 2 : hm / 2;
    const double T = 1 - 0.17 * std::cos((hm - 30) * rad) + 0.24 * std::cos(2 * hm * rad) + 0.32 * std::cos((3 * hm + 6) * rad)
        - 0.2 * std::cos((4 * hm - 63) * rad);
    const double SL = 1 + 0.015 * std::pow(Lm - 50, 2) / std::sqrt(20 + std::pow(Lm - 50, 2));
    const double SC = 1 + 0.045 * Cm, SH = 1 + 0.015 * Cm * T;
    const double RT = -2 * std::sqrt(std::pow(Cm, 7) / (std::pow(Cm, 7) + std::pow(25, 7)))
        * std::sin(60 * std::exp(-std::pow((hm - 275) / 25, 2)) * rad);
    return std::sqrt(std::pow(dL / SL, 2) + std::pow(dC / SC, 2) + std::pow(dH / SH, 2) + RT * (dC / SC) * (dH / SH));
}
} // namespace

class TestAppearance : public QObject
{
    Q_OBJECT
private slots:
    void migrate_data()
    {
        QTest::addColumn<QString>("saved");
        QTest::addColumn<QString>("pair");
        QTest::addColumn<QString>("mode");
        QTest::newRow("busnes light id") << "busnes-light" << "busnes-pair" << "light";
        QTest::newRow("busnes dark id") << "busnes-dark" << "busnes-pair" << "dark";
        QTest::newRow("busnes light name") << "Busnes Light" << "busnes-pair" << "light";
        QTest::newRow("busnes dark name") << "Busnes Dark" << "busnes-pair" << "dark";
        QTest::newRow("kept half") << "dark-matter" << "matter-pair" << "dark";
        QTest::newRow("kept alias") << "White Cliffs" << "ocean-pair" << "light";
        QTest::newRow("pair id") << "nord-pair" << "nord-pair" << "system";
        QTest::newRow("system") << "system" << "busnes-pair" << "system";
        for (const char* removed : {"tropics", "tropic-night", "coffee", "cyber-punk", "neon-purple", "space", "sky", "sun", "cyber", "oled"})
            QTest::newRow(removed) << removed << "busnes-pair" << "system";
        QTest::newRow("empty") << "" << "busnes-pair" << "system";
    }

    void migrate()
    {
        QFETCH(QString, saved);
        QFETCH(QString, pair);
        QFETCH(QString, mode);
        QCOMPARE(migrateTheme(saved), (ThemeChoice{pair, mode}));
    }

    // A legacy "theme" is migrated once and removed; the new keys win from then on.
    void migratesSavedSetting()
    {
        QTemporaryDir dir;
        QSettings s(dir.filePath(QStringLiteral("a.ini")), QSettings::IniFormat);
        s.setValue(QStringLiteral("theme"), QStringLiteral("busnes-dark"));
        Appearance a(&s);
        QCOMPARE(a.pair(), QStringLiteral("busnes-pair"));
        QCOMPARE(a.mode(), QStringLiteral("dark"));
        QVERIFY(a.dark());
        QVERIFY(!s.contains(QStringLiteral("theme")));
        QCOMPARE(s.value(QStringLiteral("appearance/mode")).toString(), QStringLiteral("dark"));

        s.setValue(QStringLiteral("theme"), QStringLiteral("coffee"));
        a.setPair(QStringLiteral("ocean-pair"));
        Appearance b(&s);
        QCOMPARE(b.pair(), QStringLiteral("ocean-pair"));
        QCOMPARE(b.mode(), QStringLiteral("dark"));
    }

    void removedSettingResets()
    {
        QTemporaryDir dir;
        QSettings s(dir.filePath(QStringLiteral("a.ini")), QSettings::IniFormat);
        s.setValue(QStringLiteral("theme"), QStringLiteral("neon-purple"));
        Appearance a(&s);
        QCOMPARE(a.pair(), QStringLiteral("busnes-pair"));
        QCOMPARE(a.mode(), QStringLiteral("system"));

        QSettings t(dir.filePath(QStringLiteral("b.ini")), QSettings::IniFormat);
        t.setValue(QStringLiteral("appearance/pair"), QStringLiteral("coffee-pair"));
        t.setValue(QStringLiteral("appearance/mode"), QStringLiteral("sepia"));
        Appearance b(&t);
        QCOMPARE(b.pair(), QStringLiteral("busnes-pair"));
        QCOMPARE(b.mode(), QStringLiteral("system"));
    }

    void defaultsToBusnes()
    {
        QTemporaryDir dir;
        QSettings s(dir.filePath(QStringLiteral("a.ini")), QSettings::IniFormat);
        Appearance a(&s);
        QCOMPARE(a.pair(), QStringLiteral("busnes-pair"));
        QCOMPARE(a.mode(), QStringLiteral("system"));
        a.setMode(QStringLiteral("light"));
        QCOMPARE(a.colors().value(QStringLiteral("accent")).value<QColor>(), QColor(0xbf3f18));
        QCOMPARE(a.colors().value(QStringLiteral("danger")).value<QColor>(), QColor(0xa8174e));
        a.setMode(QStringLiteral("dark"));
        QCOMPARE(a.colors().value(QStringLiteral("bg")).value<QColor>(), QColor(0x182326));
        QCOMPARE(a.colors().value(QStringLiteral("danger")).value<QColor>(), QColor(0xff8fb1));
    }

    // The status colour gate: every half, every check, as ky-ui's script.
    void statusColours()
    {
        int checks = 0;
        QStringList failures;
        const auto check = [&](const QString& id, const QString& what, double value, double min) {
            ++checks;
            if (!(value >= min))
                failures << QStringLiteral("%1: %2 %3 < %4").arg(id, what).arg(value, 0, 'f', 2).arg(min);
        };
        for (const ThemePair& p : themePairs()) {
            QCOMPARE(p.light.dark, false);
            QCOMPARE(p.dark.dark, true);
            for (const ThemeHalf& h : {p.light, p.dark}) {
                const std::pair<const char*, QColor> statuses[] = {{"danger", h.danger}, {"warning", h.warning}, {"success", h.success}};
                for (const auto& [name, c] : statuses) {
                    const QString s = QString::fromLatin1(name);
                    check(h.id, s + QStringLiteral(" on bg"), contrast(c, h.bg), 4.5);
                    check(h.id, s + QStringLiteral(" on panel"), contrast(c, h.panel), 4.5);
                    check(h.id, s + QStringLiteral(" on its tint"), contrast(c, softTint(c, h.panel)), 4.5);
                }
                check(h.id, QStringLiteral("ΔE accent–danger"), deltaE(h.accent, h.danger), 20);
                const std::pair<QColor, QColor> pairs[] = {{h.accent, h.warning}, {h.accent, h.success}, {h.danger, h.warning},
                                                           {h.danger, h.success}, {h.warning, h.success}};
                for (const auto& [a, b] : pairs)
                    check(h.id, QStringLiteral("ΔE %1–%2").arg(a.name(), b.name()), deltaE(a, b), 10);
            }
        }
        QVERIFY2(failures.isEmpty(), qPrintable(failures.join(QLatin1Char('\n'))));
        QCOMPARE(checks, 180);
        qInfo("status colours: %d checks pass across 12 halves", checks);
    }

    // Spot values from ky-ui's own table (check-status-colours.mjs --table), to prove the port.
    void portMatchesKyUi()
    {
        QCOMPARE(qRound(contrast(QColor(0xa8174e), QColor(0xf8f6f0)) * 100), 671);
        QCOMPARE(qRound(deltaE(QColor(0xbf3f18), QColor(0xa8174e)) * 100), 2388);
    }
};

QTEST_MAIN(TestAppearance)
#include "tst_appearance.moc"
