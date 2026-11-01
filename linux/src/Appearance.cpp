#include "Appearance.h"

#include <QGuiApplication>
#include <QStyleHints>

namespace {
const QString kPair = QStringLiteral("appearance/pair");
const QString kMode = QStringLiteral("appearance/mode");
const QString kLegacy = QStringLiteral("theme");
const QString kBusnes = QStringLiteral("busnes-pair");

// Shared status colours by scheme (tokens.css --ky-{danger,success,warning}-{light,dark}).
const QColor kDangerLight(0xa8174e), kSuccessLight(0x267442), kWarningLight(0x7a5f00);
const QColor kDangerDark(0xff8fb1), kSuccessDark(0x86efac), kWarningDark(0xffd25e);

ThemeHalf light(const char* id, QRgb bg, QRgb panel, QRgb ink, QRgb inkStrong, QRgb accent, QRgb accentSoft, QColor line,
                QRgb buttonText, QColor success = kSuccessLight)
{
    return {QString::fromLatin1(id), false, QColor(bg), QColor(panel), QColor(ink), QColor(inkStrong), QColor(accent),
            QColor(accentSoft), line, QColor(buttonText), kDangerLight, kWarningLight, success};
}

ThemeHalf dark(const char* id, QRgb bg, QRgb panel, QRgb ink, QRgb inkStrong, QRgb accent, QColor accentSoft, QColor line,
               QRgb buttonText, QColor success = kSuccessDark, QColor danger = kDangerDark)
{
    return {QString::fromLatin1(id), true, QColor(bg), QColor(panel), QColor(ink), QColor(inkStrong), QColor(accent),
            accentSoft, line, QColor(buttonText), danger, kWarningDark, success};
}

// Old names and aliases from THEME_CATALOG, so a saved name finds its half.
const QHash<QString, QString>& aliases()
{
    static const QHash<QString, QString> map{
        {QStringLiteral("Busnes Light"), QStringLiteral("busnes-light")}, {QStringLiteral("Busnes Dark"), QStringLiteral("busnes-dark")},
        {QStringLiteral("Matter Light"), QStringLiteral("light-matter")}, {QStringLiteral("Light Matter"), QStringLiteral("light-matter")},
        {QStringLiteral("Matter Dark"), QStringLiteral("dark-matter")}, {QStringLiteral("Dark Matter"), QStringLiteral("dark-matter")},
        {QStringLiteral("Current"), QStringLiteral("dark-matter")},
        {QStringLiteral("Ocean Light"), QStringLiteral("white-cliffs")}, {QStringLiteral("White Cliffs"), QStringLiteral("white-cliffs")},
        {QStringLiteral("Cliffs"), QStringLiteral("white-cliffs")},
        {QStringLiteral("Ocean Dark"), QStringLiteral("ocean")}, {QStringLiteral("Ocean"), QStringLiteral("ocean")},
        {QStringLiteral("Forest Light"), QStringLiteral("forest-light")},
        {QStringLiteral("Forest Dark"), QStringLiteral("forest")}, {QStringLiteral("Forest"), QStringLiteral("forest")},
        {QStringLiteral("Ky Light"), QStringLiteral("polished-ky")}, {QStringLiteral("Polished Ky"), QStringLiteral("polished-ky")},
        {QStringLiteral("Ky Dark"), QStringLiteral("patina-ky")}, {QStringLiteral("Patina Ky"), QStringLiteral("patina-ky")},
        {QStringLiteral("patina"), QStringLiteral("patina-ky")}, {QStringLiteral("Patina Ky (base)"), QStringLiteral("patina-ky")},
        {QStringLiteral("Nord Light"), QStringLiteral("paper")}, {QStringLiteral("Paper Clean"), QStringLiteral("paper")},
        {QStringLiteral("Nord Dark"), QStringLiteral("nord")}, {QStringLiteral("Nord"), QStringLiteral("nord")},
        {QStringLiteral("Nord Slate"), QStringLiteral("nord")},
    };
    return map;
}

const ThemePair* findPair(const QString& id)
{
    for (const ThemePair& p : themePairs())
        if (p.id == id)
            return &p;
    return nullptr;
}
} // namespace

const QList<ThemePair>& themePairs()
{
    // Busnes from tokens.css; line is rgba(ink-strong, 0.22 / 0.24). The rest from THEME_PALETTES.
    static const QList<ThemePair> pairs{
        {QStringLiteral("busnes-pair"), QStringLiteral("Busnes"),
         light("busnes-light", 0xf8f6f0, 0xffffff, 0x566461, 0x182326, 0xbf3f18, 0xfbf0ec, QColor(24, 35, 38, 56), 0xffffff),
         dark("busnes-dark", 0x182326, 0x1f2b2e, 0xb3bcb8, 0xf2efe8, 0xf5865f, QColor(0x2b2622), QColor(242, 239, 232, 61), 0x182326)},
        {QStringLiteral("matter-pair"), QStringLiteral("Matter"),
         light("light-matter", 0xf5efe5, 0xfff8ee, 0x4c3d32, 0x2d1f15, 0x805a39, 0xe2d0b8, QColor(0xd4c7b5), 0xffffff),
         dark("dark-matter", 0x1d1b19, 0x292725, 0xcfc6bb, 0xf0e9df, 0xc29a72, QColor(0x49392c), QColor(0x45403a), 0x24170f)},
        {QStringLiteral("ocean-pair"), QStringLiteral("Ocean"),
         light("white-cliffs", 0xf7f9fb, 0xffffff, 0x2e4c63, 0x163246, 0x1d5fa6, 0xdde9f7, QColor(0xc7d6df), 0xffffff),
         dark("ocean", 0x0f1b24, 0x152a36, 0xb8d8e8, 0xe0f2fb, 0x6aa8e8, QColor(0x1f3a55), QColor(0x2f5567), 0x0a1b22)},
        {QStringLiteral("forest-pair"), QStringLiteral("Forest"),
         light("forest-light", 0xf3f6ef, 0xfcfdf9, 0x435342, 0x203322, 0x4b6b37, 0xe2ebda, QColor(0xc5d0bc), 0xffffff, QColor(0x00737a)),
         dark("forest", 0x142018, 0x1f2f24, 0xc7dbc7, 0xe3f0df, 0x8faa74, QColor(0x3a5837), QColor(0x4f694f), 0x12200f, QColor(0x6ee0d0))},
        {QStringLiteral("ky-pair"), QStringLiteral("Ky"),
         light("polished-ky", 0xeef2f6, 0xffffff, 0x475569, 0x0f172a, 0x08758b, 0xdaf2f4, QColor(0xc2cdd8), 0xffffff),
         dark("patina-ky", 0x10161e, 0x1b2530, 0xafbdca, 0xe2e8f0, 0x6fc9cc, QColor(0x203d41), QColor(0x34414d), 0x10161e)},
        {QStringLiteral("nord-pair"), QStringLiteral("Nord"),
         light("paper", 0xeceff4, 0xffffff, 0x4c566a, 0x2e3440, 0x4a6a94, 0xdbe4ef, QColor(0xc5cdd9), 0xffffff),
         dark("nord", 0x2e3440, 0x3b4252, 0xd8dee9, 0xeceff4, 0x88c0d0, QColor(136, 192, 208, 51), QColor(0x4c566a), 0x0d0f14,
              QColor(0xbcd4aa), QColor(0xecc4c8))},
    };
    return pairs;
}

QColor softTint(const QColor& status, const QColor& panel)
{
    const auto mix = [](int s, int p) { return qRound(s * 0.12 + p * 0.88); };
    return QColor(mix(status.red(), panel.red()), mix(status.green(), panel.green()), mix(status.blue(), panel.blue()));
}

ThemeChoice migrateTheme(const QString& saved)
{
    if (findPair(saved))
        return {saved, QStringLiteral("system")};
    const QString id = aliases().value(saved, saved);
    for (const ThemePair& p : themePairs()) {
        if (p.light.id == id)
            return {p.id, QStringLiteral("light")};
        if (p.dark.id == id)
            return {p.id, QStringLiteral("dark")};
    }
    return {kBusnes, QStringLiteral("system")};
}

Appearance::Appearance(QSettings* settings, QObject* parent) : QObject(parent), m_settings(settings)
{
    if (m_settings->contains(kLegacy) && !m_settings->contains(kPair)) {
        m_choice = migrateTheme(m_settings->value(kLegacy).toString());
        m_settings->remove(kLegacy);
        save();
    } else {
        m_choice = {m_settings->value(kPair, kBusnes).toString(), m_settings->value(kMode, QStringLiteral("system")).toString()};
    }
    // A pair or mode this build does not know falls back to the defaults.
    if (!findPair(m_choice.pair))
        m_choice.pair = kBusnes;
    if (m_choice.mode != QLatin1String("light") && m_choice.mode != QLatin1String("dark"))
        m_choice.mode = QStringLiteral("system");
    connect(QGuiApplication::styleHints(), &QStyleHints::colorSchemeChanged, this, &Appearance::changed);
}

void Appearance::setPair(const QString& pair)
{
    if (pair == m_choice.pair || !findPair(pair))
        return;
    m_choice.pair = pair;
    save();
}

void Appearance::setMode(const QString& mode)
{
    if (mode == m_choice.mode || (mode != QLatin1String("system") && mode != QLatin1String("light") && mode != QLatin1String("dark")))
        return;
    m_choice.mode = mode;
    save();
}

void Appearance::save()
{
    m_settings->setValue(kPair, m_choice.pair);
    m_settings->setValue(kMode, m_choice.mode);
    emit changed();
}

// System follows the desktop; an unknown scheme stays dark, as before.
bool Appearance::dark() const
{
    if (m_choice.mode != QLatin1String("system"))
        return m_choice.mode == QLatin1String("dark");
    return QGuiApplication::styleHints()->colorScheme() != Qt::ColorScheme::Light;
}

const ThemeHalf& Appearance::half() const
{
    const ThemePair* p = findPair(m_choice.pair);
    return dark() ? p->dark : p->light;
}

QVariantMap Appearance::colors() const
{
    const ThemeHalf& h = half();
    return {
        {QStringLiteral("bg"), h.bg}, {QStringLiteral("panel"), h.panel}, {QStringLiteral("ink"), h.ink},
        {QStringLiteral("inkStrong"), h.inkStrong}, {QStringLiteral("accent"), h.accent}, {QStringLiteral("accentSoft"), h.accentSoft},
        {QStringLiteral("line"), h.line}, {QStringLiteral("buttonText"), h.buttonText},
        {QStringLiteral("danger"), h.danger}, {QStringLiteral("warning"), h.warning}, {QStringLiteral("success"), h.success},
        {QStringLiteral("dangerSoft"), softTint(h.danger, h.panel)}, {QStringLiteral("warningSoft"), softTint(h.warning, h.panel)},
        {QStringLiteral("successSoft"), softTint(h.success, h.panel)},
    };
}

QVariantList Appearance::pairs() const
{
    QVariantList list;
    for (const ThemePair& p : themePairs())
        list << QVariantMap{{QStringLiteral("id"), p.id}, {QStringLiteral("name"), p.name}};
    return list;
}
