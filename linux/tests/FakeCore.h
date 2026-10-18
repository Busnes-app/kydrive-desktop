#pragma once

#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLocalServer>
#include <QLocalSocket>

// A stand-in for kydrive-sync: answers Core.Status, Core.Conflicts and Core.Activity, and
// holds Core.Link until Core.Status has been answered, so replies arrive out of order.
class FakeCore : public QObject
{
public:
    explicit FakeCore(const QString& path)
    {
        m_server.listen(path);
        connect(&m_server, &QLocalServer::newConnection, this, [this] {
            QLocalSocket* s = m_server.nextPendingConnection();
            connect(s, &QLocalSocket::readyRead, this, [this, s] {
                while (s->canReadLine())
                    answer(s, QJsonDocument::fromJson(s->readLine()).object());
            });
        });
    }
    QList<QString> methods;
    qint64 activityId = 7;
    QString activityMessage = QStringLiteral("Lost access to Docs");
    QJsonObject status{{QStringLiteral("Linked"), true}, {QStringLiteral("State"), QStringLiteral("idle")}};
    QJsonArray workspaces;
    QJsonArray conflicts{QJsonObject{{QStringLiteral("Path"), QStringLiteral("a (conflict).txt")}}};

private:
    void answer(QLocalSocket* s, const QJsonObject& req)
    {
        const QString method = req[QStringLiteral("method")].toString();
        methods << method;
        QJsonValue result;
        if (method == QStringLiteral("Core.Status"))
            result = status;
        else if (method == QStringLiteral("Core.Conflicts"))
            result = conflicts;
        else if (method == QStringLiteral("Core.Workspaces"))
            result = workspaces;
        else if (method == QStringLiteral("Core.Activity"))
            result = QJsonArray{QJsonObject{{QStringLiteral("ID"), activityId}, {QStringLiteral("Level"), QStringLiteral("warn")}, {QStringLiteral("Message"), activityMessage}}};
        else if (method == QStringLiteral("Core.Link")) {
            m_held = QJsonObject{{QStringLiteral("id"), req[QStringLiteral("id")]}, {QStringLiteral("result"), QJsonObject{}}, {QStringLiteral("error"), QJsonValue::Null}};
            return;
        }
        s->write(QJsonDocument(QJsonObject{{QStringLiteral("id"), req[QStringLiteral("id")]}, {QStringLiteral("result"), result}, {QStringLiteral("error"), QJsonValue::Null}}).toJson(QJsonDocument::Compact) + '\n');
        if (!m_held.isEmpty() && method == QStringLiteral("Core.Status")) {
            s->write(QJsonDocument(m_held).toJson(QJsonDocument::Compact) + '\n');
            m_held = {};
        }
    }
    QLocalServer m_server;
    QJsonObject m_held;
};
