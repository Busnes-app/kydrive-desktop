#include "CoreClient.h"
#include "FakeCore.h"

#include <QDir>
#include <QFileInfo>
#include <QProcess>
#include <QJsonArray>
#include <QStandardPaths>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLocalServer>
#include <QLocalSocket>
#include <QSignalSpy>
#include <QTemporaryDir>
#include <QTest>

#include <memory>

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

    // Q5: a core older than this shell is asked to quit; a newer or unversioned one is not.
    void olderCoreIsAskedToQuit()
    {
        for (const auto& [version, quits] : {std::pair{QStringLiteral("0.0.1"), true}, {QStringLiteral("999.0.0"), false}, {QString(), false}}) {
            QTemporaryDir dir;
            FakeCore core(dir.filePath(QStringLiteral("sync.sock")));
            if (!version.isEmpty())
                core.status[QStringLiteral("Version")] = version;
            CoreClient client(dir.filePath(QStringLiteral("sync.sock")), QStringLiteral("/nonexistent/kydrive-sync"));
            QSignalSpy status(&client, &CoreClient::statusChanged);
            client.refresh();
            QVERIFY(status.wait());
            QTest::qWait(100);
            client.refresh(); // asked once a minute, not on every refresh
            QVERIFY(status.wait());
            QTest::qWait(100);
            QCOMPARE(core.methods.count(QStringLiteral("Core.Quit")), quits ? 1 : 0);
        }
    }

    // Q8: an idle unversioned (0.6.0) core that is not this user's kydrive-sync (here, the test
    // itself) is never signalled; the user is told to log out. A syncing one is left alone.
    void unversionedCoreThatCannotEndLeavesNotice()
    {
        for (const bool syncing : {false, true}) {
            QTemporaryDir dir;
            FakeCore core(dir.filePath(QStringLiteral("sync.sock")));
            if (syncing)
                core.status[QStringLiteral("State")] = QStringLiteral("syncing");
            CoreClient client(dir.filePath(QStringLiteral("sync.sock")), QStringLiteral("/nonexistent/kydrive-sync"));
            QSignalSpy status(&client, &CoreClient::statusChanged);
            client.refresh();
            QVERIFY(status.wait());
            QCOMPARE(client.error().startsWith(QStringLiteral("An older KyDrive is still running")), !syncing);
            core.status[QStringLiteral("Version")] = QStringLiteral("999.0.0");
            client.refresh();
            QVERIFY(status.wait());
            QCOMPARE(client.error(), QString());
        }
    }

    // Q8 against the released core: KYDRIVE_OLD_SYNC_BIN, a kydrive-sync built from v0.6.0, is
    // ended by the shell while idle.
    void releasedUnversionedCoreIsEnded()
    {
        const QString bin = qEnvironmentVariable("KYDRIVE_OLD_SYNC_BIN");
        if (bin.isEmpty())
            QSKIP("KYDRIVE_OLD_SYNC_BIN not set");
        QTemporaryDir home;
        QProcessEnvironment env = QProcessEnvironment::systemEnvironment();
        env.insert(QStringLiteral("HOME"), home.path());
        env.insert(QStringLiteral("XDG_RUNTIME_DIR"), home.path());
        env.insert(QStringLiteral("XDG_CONFIG_HOME"), home.filePath(QStringLiteral("config")));
        QProcess old;
        old.setProcessEnvironment(env);
        old.start(bin, {});
        const QString sock = home.filePath(QStringLiteral("kydrive/sync.sock"));
        QTRY_VERIFY_WITH_TIMEOUT(QFileInfo::exists(sock), 10000);
        CoreClient client(sock, QStringLiteral("/nonexistent/kydrive-sync"));
        client.refresh();
        QTRY_COMPARE_WITH_TIMEOUT(old.state(), QProcess::NotRunning, 10000);
        QCOMPARE(old.exitStatus(), QProcess::NormalExit);
        QCOMPARE(client.error().startsWith(QStringLiteral("An older KyDrive")), false);
    }

    // A shell upgraded alone starts the same older core again: it is not asked to quit again.
    void sameOlderCoreIsKept()
    {
        QTemporaryDir dir;
        const QString path = dir.filePath(QStringLiteral("sync.sock"));
        auto core = std::make_unique<FakeCore>(path);
        core->status[QStringLiteral("Version")] = QStringLiteral("0.0.1");
        CoreClient client(path, QStringLiteral("/nonexistent/kydrive-sync"));
        QSignalSpy status(&client, &CoreClient::statusChanged);
        client.refresh();
        QTRY_COMPARE(core->methods.count(QStringLiteral("Core.Quit")), 1);
        QTest::qWait(100);
        core.reset(); // it quit
        core = std::make_unique<FakeCore>(path); // the core at corePath, still 0.0.1
        core->status[QStringLiteral("Version")] = QStringLiteral("0.0.1");
        client.refresh();
        QTRY_VERIFY(status.count() >= 2);
        QTest::qWait(61000); // past the once-a-minute limit
        client.refresh();
        QVERIFY(status.wait());
        QTest::qWait(100);
        QCOMPARE(core->methods.count(QStringLiteral("Core.Quit")), 0);
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

    // A connection error clears once the core answers again.
    void reconnectClearsConnectionError()
    {
        QTemporaryDir dir;
        const QString path = dir.filePath(QStringLiteral("sync.sock"));
        CoreClient client(path, QString());
        client.refresh();
        QTRY_VERIFY(!client.error().isEmpty());
        FakeCore core(path);
        QSignalSpy status(&client, &CoreClient::statusChanged);
        client.refresh();
        QVERIFY(status.wait());
        QCOMPARE(client.error(), QString());
    }
};

QTEST_GUILESS_MAIN(TestCoreClient)
#include "tst_coreclient.moc"
