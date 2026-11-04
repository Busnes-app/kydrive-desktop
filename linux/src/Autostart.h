#pragma once

#include <QObject>
#include <QString>
#include <QVariantMap>

// Start at login: a setting, off until the user turns it on. A native install writes or
// removes an XDG autostart entry; the Flatpak asks the Background portal, which manages the
// entry on the host and answers whether autostart is now on.
class Autostart : public QObject
{
    Q_OBJECT
    Q_PROPERTY(bool enabled READ enabled NOTIFY enabledChanged)

public:
    // file is the autostart entry, exec the command it runs; flatpak uses the portal instead.
    Autostart(const QString& file, const QString& exec, bool flatpak, QObject* parent = nullptr);
    // The entry for this app in the user's XDG autostart directory.
    static QString defaultFile();

    bool enabled() const;
    Q_INVOKABLE void setEnabled(bool on);
    // At start: the Flatpak checks its last answer with the portal again.
    void refresh();

signals:
    void enabledChanged();
    void failed(const QString& message);

private Q_SLOTS:
    void portalResponse(uint response, const QVariantMap& results);

private:
    QString m_file;
    QString m_exec;
    bool m_flatpak;
    QString m_request; // the portal request waiting for its answer
    bool m_asked = false; // that request asked to turn it on
    bool m_quiet = false; // that request is refresh()'s check, not the user's
};
