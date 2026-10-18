#include "CoreClient.h"

#include <QDesktopServices>
#include <QDir>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QProcess>
#include <QUrl>

CoreClient::CoreClient(const QString& socketPath, const QString& corePath, QObject* parent)
    : QObject(parent)
    , m_socketPath(socketPath)
    , m_corePath(corePath)
{
    connect(&m_socket, &QLocalSocket::connected, this, [this] {
        emit connectedChanged();
        flush();
    });
    connect(&m_socket, &QLocalSocket::disconnected, this, [this] {
        emit connectedChanged();
        failAll(tr("KyDrive sync stopped"));
    });
    connect(&m_socket, &QLocalSocket::errorOccurred, this, [this](QLocalSocket::LocalSocketError) {
        if (m_socket.state() == QLocalSocket::ConnectedState)
            return;
        // Nothing listening: start the core once, then let the next call retry.
        if (!m_started && !m_corePath.isEmpty()) {
            m_started = true;
            QProcess::startDetached(m_corePath, {});
        }
        failAll(tr("KyDrive sync is not running"));
    });
    connect(&m_socket, &QLocalSocket::readyRead, this, &CoreClient::readReplies);
    m_poll.setInterval(3000);
    connect(&m_poll, &QTimer::timeout, this, &CoreClient::refresh);
    m_poll.start();
}

// The socket outlives the other members and reports its disconnect while dying.
CoreClient::~CoreClient()
{
    m_socket.disconnect(this);
}

QString CoreClient::defaultSocketPath()
{
    return qEnvironmentVariable("XDG_RUNTIME_DIR", QDir::tempPath()) + QStringLiteral("/kydrive/sync.sock");
}

void CoreClient::call(const QString& method, const QVariantMap& params, Reply done)
{
    const qint64 id = m_nextId++;
    m_pending.insert(id, std::move(done));
    const QJsonObject req{{QStringLiteral("method"), QStringLiteral("Core.") + method},
                          {QStringLiteral("params"), QJsonArray{QJsonObject::fromVariantMap(params)}},
                          {QStringLiteral("id"), id}};
    m_outbox << QJsonDocument(req).toJson(QJsonDocument::Compact) + '\n';
    if (m_socket.state() == QLocalSocket::UnconnectedState)
        m_socket.connectToServer(m_socketPath);
    else
        flush();
}

void CoreClient::flush()
{
    if (m_socket.state() != QLocalSocket::ConnectedState)
        return;
    for (const QByteArray& line : std::as_const(m_outbox))
        m_socket.write(line);
    m_outbox.clear();
}

void CoreClient::readReplies()
{
    while (m_socket.canReadLine()) {
        const QJsonObject reply = QJsonDocument::fromJson(m_socket.readLine()).object();
        Reply done = m_pending.take(reply[QStringLiteral("id")].toInteger());
        const QJsonValue err = reply[QStringLiteral("error")];
        if (done)
            done(reply[QStringLiteral("result")], err.isNull() ? QString() : err.toString());
    }
}

void CoreClient::failAll(const QString& error)
{
    m_outbox.clear();
    const auto pending = std::exchange(m_pending, {});
    for (const Reply& done : pending)
        if (done)
            done(QJsonValue(), error);
}

void CoreClient::setError(const QString& error)
{
    if (error != m_error) {
        m_error = error;
        emit errorChanged();
    }
}

CoreClient::Reply CoreClient::reportErrors(Reply done)
{
    return [this, done](const QJsonValue& result, const QString& error) {
        setError(error);
        if (done && error.isEmpty())
            done(result, error);
        refresh();
    };
}

void CoreClient::refresh()
{
    call(QStringLiteral("Status"), {}, [this](const QJsonValue& result, const QString& error) {
        if (!error.isEmpty())
            return setError(error);
        m_status = result.toObject().toVariantMap();
        emit statusChanged();
    });
    call(QStringLiteral("Conflicts"), {}, [this](const QJsonValue& result, const QString& error) {
        if (error.isEmpty()) {
            m_conflicts = result.toArray().toVariantList();
            emit conflictsChanged();
        }
    });
    call(QStringLiteral("Activity"), {{QStringLiteral("After"), qMax<qint64>(m_lastActivity, 0)}},
         [this](const QJsonValue& result, const QString& error) {
             if (!error.isEmpty())
                 return;
             const bool baseline = m_lastActivity < 0;
             const QJsonArray entries = result.toArray();
             // Newest first; announce oldest first.
             for (qsizetype i = entries.size() - 1; i >= 0; --i) {
                 const QJsonObject a = entries[i].toObject();
                 const qint64 id = a[QStringLiteral("ID")].toInteger();
                 if (id <= m_lastActivity)
                     continue;
                 if (!baseline && a[QStringLiteral("Level")].toString() == QStringLiteral("warn"))
                     emit warning(a[QStringLiteral("Message")].toString());
                 m_lastActivity = id;
             }
             m_lastActivity = qMax<qint64>(m_lastActivity, 0);
         });
}

void CoreClient::loadWorkspaces()
{
    call(QStringLiteral("Workspaces"), {}, reportErrors([this](const QJsonValue& result, const QString&) {
             m_workspaces = result.toArray().toVariantList();
             emit workspacesChanged();
         }));
}

void CoreClient::link(const QString& server, const QString& name)
{
    m_linking = true;
    emit linkingChanged();
    // Returns once the user approves in the browser, or after the core's ten-minute timeout.
    call(QStringLiteral("Link"), {{QStringLiteral("Server"), server}, {QStringLiteral("Name"), name}},
         [this](const QJsonValue&, const QString& error) {
             m_linking = false;
             emit linkingChanged();
             setError(error);
             if (error.isEmpty())
                 loadWorkspaces();
             refresh();
         });
}

void CoreClient::unlink(bool removeFiles)
{
    call(QStringLiteral("Unlink"), {{QStringLiteral("RemoveFiles"), removeFiles}}, reportErrors());
}

void CoreClient::choose(const QStringList& ids)
{
    call(QStringLiteral("Choose"), {{QStringLiteral("IDs"), ids}}, reportErrors([this](const QJsonValue&, const QString&) { loadWorkspaces(); }));
}

void CoreClient::pause(bool paused)
{
    call(QStringLiteral("Pause"), {{QStringLiteral("Paused"), paused}}, reportErrors());
}

void CoreClient::confirmDeletes(const QString& workspace, bool allow)
{
    call(QStringLiteral("ConfirmDeletes"), {{QStringLiteral("Workspace"), workspace}, {QStringLiteral("Allow"), allow}}, reportErrors());
}

void CoreClient::openFolder()
{
    QDesktopServices::openUrl(QUrl::fromLocalFile(m_status.value(QStringLiteral("Root")).toString()));
}

void CoreClient::openInBrowser()
{
    const QUrl url(m_status.value(QStringLiteral("Server")).toString());
    if (url.scheme() == QStringLiteral("https") || url.scheme() == QStringLiteral("http"))
        QDesktopServices::openUrl(url);
}
