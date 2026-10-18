#include "CoreClient.h"
#include "FakeCore.h"

#include <QDir>
#include <QJsonArray>
#include <QStandardPaths>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLocalServer>
#include <QLocalSocket>
#include <QSignalSpy>
#include <QTemporaryDir>
#include <QTest>

class TestCoreClient : public QObject
{
    Q_OBJECT
private slots:
    void refreshFillsStatusAndConflicts()
    {
        QTemporaryDir dir;
        FakeCore core(dir.filePath(QStringLiteral("sync.sock")));
        CoreClient client(dir.filePath(QStringLiteral("sync.sock")), QString());
        QSignalSpy status(&client, &CoreClient::statusChanged);
        QSignalSpy conflicts(&client, &CoreClient::conflictsChanged);
        client.refresh();
        QVERIFY(status.wait());
        QVERIFY(conflicts.count() || conflicts.wait());
        QCOMPARE(client.status().value(QStringLiteral("State")).toString(), QStringLiteral("idle"));
        QCOMPARE(client.conflicts().size(), 1);
        QVERIFY(client.connected());
    }

    void repliesMatchedById()
    {
        QTemporaryDir dir;
        FakeCore core(dir.filePath(QStringLiteral("sync.sock")));
        CoreClient client(dir.filePath(QStringLiteral("sync.sock")), QString());
        bool linked = false;
        client.call(QStringLiteral("Link"), {}, [&](const QJsonValue&, const QString& error) { linked = error.isEmpty(); });
        QSignalSpy status(&client, &CoreClient::statusChanged);
        client.refresh();
        QVERIFY(status.wait());
        QTRY_VERIFY(linked);
    }

    // The first activity read only sets the baseline; later warnings are announced once.
    void warnsOnNewActivityOnly()
    {
        QTemporaryDir dir;
        FakeCore core(dir.filePath(QStringLiteral("sync.sock")));
        CoreClient client(dir.filePath(QStringLiteral("sync.sock")), QString());
        QSignalSpy warning(&client, &CoreClient::warning);
        QSignalSpy status(&client, &CoreClient::statusChanged);
        client.refresh();
        QVERIFY(status.wait());
        QTest::qWait(100);
        QCOMPARE(warning.count(), 0);
        core.activityId = 8;
        client.refresh();
        QTRY_COMPARE(warning.count(), 1);
        QCOMPARE(warning.first().first().toString(), QStringLiteral("Lost access to Docs"));
    }

    // The core falls back to its config dir, never a shared temp dir (core/cmd/kydrive-sync/main.go).
    void socketPathMatchesCore()
    {
        const QByteArray saved = qgetenv("XDG_RUNTIME_DIR");
        qputenv("XDG_RUNTIME_DIR", "/run/user/4242");
        QCOMPARE(CoreClient::defaultSocketPath(), QStringLiteral("/run/user/4242/kydrive/sync.sock"));
        qunsetenv("XDG_RUNTIME_DIR");
        const QString path = CoreClient::defaultSocketPath();
        qputenv("XDG_RUNTIME_DIR", saved);
        QVERIFY2(!path.startsWith(QDir::tempPath()), qPrintable(path));
        QCOMPARE(path, QStandardPaths::writableLocation(QStandardPaths::GenericConfigLocation) + QStringLiteral("/kydrive/sync.sock"));
    }

    // Notification bodies take markup, so core text reaches them escaped.
    void warningIsEscaped()
    {
        QTemporaryDir dir;
        FakeCore core(dir.filePath(QStringLiteral("sync.sock")));
        core.activityMessage = QStringLiteral("Lost access to <img src=\"http://x/\">");
        CoreClient client(dir.filePath(QStringLiteral("sync.sock")), QString());
        QSignalSpy warning(&client, &CoreClient::warning);
        QSignalSpy status(&client, &CoreClient::statusChanged);
        client.refresh();
        QVERIFY(status.wait());
        QTest::qWait(100);
        core.activityId = 8;
        client.refresh();
        QTRY_COMPARE(warning.count(), 1);
        QCOMPARE(warning.first().first().toString(), QStringLiteral("Lost access to &lt;img src=&quot;http://x/&quot;&gt;"));
    }

    void missingCoreReportsError()
    {
        QTemporaryDir dir;
        CoreClient client(dir.filePath(QStringLiteral("none.sock")), QString());
        QString err;
        client.call(QStringLiteral("Status"), {}, [&](const QJsonValue&, const QString& e) { err = e; });
        QTRY_VERIFY(!err.isEmpty());
        QVERIFY(!client.connected());
    }
};

QTEST_GUILESS_MAIN(TestCoreClient)
#include "tst_coreclient.moc"
