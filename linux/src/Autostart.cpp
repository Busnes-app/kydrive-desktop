#include "Autostart.h"

#include <QDBusConnection>
#include <QDBusMessage>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QRandomGenerator>
#include <QSaveFile>
#include <QSettings>
#include <QStandardPaths>

namespace {
const QString flatpakKey = QStringLiteral("autostart/enabled");

// Quotes an Exec argument as the Desktop Entry spec asks.
QString quoted(QString arg)
{
    for (const char* c : {"\\", "\"", "`", "$"})
        arg.replace(QLatin1String(c), QLatin1Char('\\') + QLatin1String(c));
    return QLatin1Char('"') + arg + QLatin1Char('"');
}
}

Autostart::Autostart(const QString& file, const QString& exec, bool flatpak, QObject* parent)
    : QObject(parent)
    , m_file(file)
    , m_exec(exec)
    , m_flatpak(flatpak)
{
}

QString Autostart::defaultFile()
{
    return QStandardPaths::writableLocation(QStandardPaths::GenericConfigLocation) + QStringLiteral("/autostart/com.kysecurity.drive.desktop");
}

bool Autostart::enabled() const
{
    // The portal's entry is on the host, out of the sandbox's sight: keep its last answer.
    if (m_flatpak)
        return QSettings().value(flatpakKey, false).toBool();
    // A desktop's startup settings switch an entry off without deleting it.
    QFile f(m_file);
    if (!f.open(QIODevice::ReadOnly))
        return false;
    const QString entry = QString::fromUtf8(f.readAll());
    return !entry.contains(QStringLiteral("\nHidden=true")) && !entry.contains(QStringLiteral("\nX-GNOME-Autostart-enabled=false"));
}

void Autostart::refresh()
{
    // The portal is the only judge of the host's entry: asking again with the last answer
    // brings a permission revoked since back to off (no prompt once the user has decided).
    if (m_flatpak && enabled())
        setEnabled(true);
}

void Autostart::setEnabled(bool on)
{
    if (m_flatpak) {
        QDBusConnection bus = QDBusConnection::sessionBus();
        const QString token = QStringLiteral("kydrive%1").arg(QRandomGenerator::global()->generate());
        // Subscribe before asking: the answer can arrive before the call returns.
        m_request = QStringLiteral("/org/freedesktop/portal/desktop/request/%1/%2")
                        .arg(bus.baseService().mid(1).replace(QLatin1Char('.'), QLatin1Char('_')), token);
        bus.connect(QStringLiteral("org.freedesktop.portal.Desktop"), m_request, QStringLiteral("org.freedesktop.portal.Request"),
                    QStringLiteral("Response"), this, SLOT(portalResponse(uint, QVariantMap)));
        QDBusMessage msg = QDBusMessage::createMethodCall(QStringLiteral("org.freedesktop.portal.Desktop"), QStringLiteral("/org/freedesktop/portal/desktop"),
                                                          QStringLiteral("org.freedesktop.portal.Background"), QStringLiteral("RequestBackground"));
        msg << QString() << QVariantMap{{QStringLiteral("handle_token"), token},
                                        {QStringLiteral("reason"), tr("Keep your KyDrive folder in sync after you sign in.")},
                                        {QStringLiteral("autostart"), on},
                                        {QStringLiteral("commandline"), QStringList{QStringLiteral("kydrive-desktop")}}};
        if (const QDBusMessage reply = bus.call(msg); reply.type() == QDBusMessage::ErrorMessage) {
            emit failed(tr("Could not change start at login: %1").arg(reply.errorMessage()));
            emit enabledChanged();
        }
        return;
    }
    bool ok = true;
    if (on) {
        QDir().mkpath(QFileInfo(m_file).absolutePath());
        QSaveFile f(m_file);
        ok = f.open(QIODevice::WriteOnly)
            && f.write(QStringLiteral("[Desktop Entry]\nType=Application\nName=KyDrive\nExec=%1\nIcon=com.kysecurity.drive\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n")
                           .arg(quoted(m_exec)).toUtf8()) > 0
            && f.commit();
    } else if (QFileInfo::exists(m_file)) {
        ok = QFile::remove(m_file);
    }
    if (!ok)
        emit failed(tr("Could not change start at login in %1").arg(QFileInfo(m_file).absolutePath()));
    emit enabledChanged();
}

void Autostart::portalResponse(uint response, const QVariantMap& results)
{
    QDBusConnection::sessionBus().disconnect(QStringLiteral("org.freedesktop.portal.Desktop"), m_request, QStringLiteral("org.freedesktop.portal.Request"),
                                             QStringLiteral("Response"), this, SLOT(portalResponse(uint, QVariantMap)));
    if (response == 0)
        QSettings().setValue(flatpakKey, results.value(QStringLiteral("autostart")).toBool());
    else
        emit failed(tr("Start at login was not changed."));
    emit enabledChanged();
}
