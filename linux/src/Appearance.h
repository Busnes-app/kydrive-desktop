#pragma once

#include <QColor>
#include <QList>
#include <QObject>
#include <QSettings>
#include <QVariantMap>

// The suite's six light/dark theme pairs (ky-ui 0.11.0 src/themes.js THEME_PAIRS and
// src/tokens.css), the saved choice of pair and light/dark mode, and the colours they give.
// Copy palette values from ky-ui, never approximate them.

struct ThemeHalf {
    QString id;
    bool dark;
    QColor bg, panel, ink, inkStrong, accent, accentSoft, line, buttonText, danger, warning, success;
};

struct ThemePair {
    QString id, name;
    ThemeHalf light, dark;
};

const QList<ThemePair>& themePairs();

// Status pill fill: the status mixed 12% over the panel, as ky-ui's --ky-*-soft.
QColor softTint(const QColor& status, const QColor& panel);

struct ThemeChoice {
    QString pair; // a ThemePair id
    QString mode; // "system", "light" or "dark"
    bool operator==(const ThemeChoice&) const = default;
};

// Maps a saved suite theme (an ID, name or alias from ky-ui's THEME_CATALOG, or a pair ID)
// to a pair and mode. A half keeps its pair with that half forced; anything removed or
// unknown is Busnes following the system.
ThemeChoice migrateTheme(const QString& saved);

class Appearance : public QObject
{
    Q_OBJECT
    Q_PROPERTY(QString pair READ pair WRITE setPair NOTIFY changed)
    Q_PROPERTY(QString mode READ mode WRITE setMode NOTIFY changed)
    Q_PROPERTY(bool dark READ dark NOTIFY changed)
    Q_PROPERTY(QVariantMap colors READ colors NOTIFY changed)
    Q_PROPERTY(QVariantList pairs READ pairs CONSTANT)

public:
    // Reads appearance/pair and appearance/mode, migrating a legacy "theme" value once.
    explicit Appearance(QSettings* settings, QObject* parent = nullptr);

    QString pair() const { return m_choice.pair; }
    QString mode() const { return m_choice.mode; }
    void setPair(const QString& pair);
    void setMode(const QString& mode);
    bool dark() const;
    const ThemeHalf& half() const;
    QVariantMap colors() const;
    QVariantList pairs() const;

signals:
    void changed();

private:
    void save();
    QSettings* m_settings;
    ThemeChoice m_choice;
};
