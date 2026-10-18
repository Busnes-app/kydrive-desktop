#pragma once

#include <QHash>
#include <QJsonValue>
#include <QLocalSocket>
#include <QObject>
#include <QTimer>
#include <QVariantList>
#include <QVariantMap>

#include <functional>

// The shell's only link to kydrive-sync: JSON-RPC 1.0 ("Core.<Method>") over the core's
// owner-only Unix socket. It holds no sync logic; it renders what the core reports.
class CoreClient : public QObject
{
    Q_OBJECT
    Q_PROPERTY(QVariantMap status READ status NOTIFY statusChanged)
    Q_PROPERTY(QVariantList workspaces READ workspaces NOTIFY workspacesChanged)
    Q_PROPERTY(QVariantList conflicts READ conflicts NOTIFY conflictsChanged)
    Q_PROPERTY(bool connected READ connected NOTIFY connectedChanged)
    Q_PROPERTY(bool linking READ linking NOTIFY linkingChanged)
    Q_PROPERTY(QString error READ error NOTIFY errorChanged)

public:
    using Reply = std::function<void(const QJsonValue& result, const QString& error)>;

    // corePath, when set, is started once if nothing answers on socketPath.
    explicit CoreClient(const QString& socketPath, const QString& corePath, QObject* parent = nullptr);
    ~CoreClient() override;

    static QString defaultSocketPath();

    QVariantMap status() const { return m_status; }
    QVariantList workspaces() const { return m_workspaces; }
    QVariantList conflicts() const { return m_conflicts; }
    bool connected() const { return m_socket.state() == QLocalSocket::ConnectedState; }
    bool linking() const { return m_linking; }
    QString error() const { return m_error; }

    void call(const QString& method, const QVariantMap& params, Reply done = {});

    Q_INVOKABLE void refresh();
    Q_INVOKABLE void loadWorkspaces();
    Q_INVOKABLE void link(const QString& server, const QString& name);
    Q_INVOKABLE void unlink(bool removeFiles);
    Q_INVOKABLE void choose(const QStringList& ids);
    Q_INVOKABLE void pause(bool paused);
    Q_INVOKABLE void confirmDeletes(const QString& workspace, bool allow);
    Q_INVOKABLE void openFolder();
    Q_INVOKABLE void openInBrowser();

signals:
    void statusChanged();
    void workspacesChanged();
    void conflictsChanged();
    void connectedChanged();
    void linkingChanged();
    void errorChanged();
    void warning(const QString& message);

private:
    void flush();
    void readReplies();
    void failAll(const QString& error);
    void setError(const QString& error);
    Reply reportErrors(Reply done = {});

    QString m_socketPath;
    QString m_corePath;
    QLocalSocket m_socket;
    QTimer m_poll;
    QList<QByteArray> m_outbox;
    QHash<qint64, Reply> m_pending;
    qint64 m_nextId = 1;
    qint64 m_lastActivity = -1;
    bool m_started = false;
    bool m_linking = false;
    QVariantMap m_status;
    QVariantList m_workspaces;
    QVariantList m_conflicts;
    QString m_error;
};
