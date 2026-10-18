#include "CoreClient.h"
#include "FakeCore.h"

#include <KLocalizedQmlContext>

#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQuickWindow>
#include <QSignalSpy>
#include <QTemporaryDir>
#include <QTest>

// Loads the real Main.qml against a fake core and fails on any QML warning. With
// KYDRIVE_SHOT_DIR set it also saves each state as a PNG.
class TestQml : public QObject
{
    Q_OBJECT
private:
    void render(const QString& socket, const QString& shot)
    {
        CoreClient client(socket, QString());
        QQmlApplicationEngine engine;
        QStringList warnings;
        connect(&engine, &QQmlEngine::warnings, this, [&](const QList<QQmlError>& list) {
            for (const QQmlError& e : list)
                warnings << e.toString();
        });
        KLocalization::setupLocalizedContext(&engine);
        engine.rootContext()->setContextProperty(QStringLiteral("Core"), &client);
        engine.load(QUrl(QStringLiteral("qrc:/qml/Main.qml")));
        QCOMPARE(engine.rootObjects().size(), 1);
        auto* window = qobject_cast<QQuickWindow*>(engine.rootObjects().constFirst());
        QSignalSpy status(&client, &CoreClient::statusChanged);
        client.refresh();
        QVERIFY(status.wait());
        window->show();
        QVERIFY(QTest::qWaitForWindowExposed(window));
        QTest::qWait(500);
        QVERIFY2(warnings.isEmpty(), qPrintable(warnings.join(QLatin1Char('\n'))));
        if (const QString dir = qEnvironmentVariable("KYDRIVE_SHOT_DIR"); !dir.isEmpty())
            QVERIFY(window->grabWindow().save(dir + QLatin1Char('/') + shot + QStringLiteral(".png")));
    }

private slots:
    void unlinked()
    {
        QTemporaryDir dir;
        const QString socket = dir.filePath(QStringLiteral("s"));
        FakeCore core(socket);
        core.status = {{QStringLiteral("Linked"), false}, {QStringLiteral("State"), QStringLiteral("unlinked")}};
        core.conflicts = {};
        render(socket, QStringLiteral("unlinked"));
    }

    void linked()
    {
        QTemporaryDir dir;
        const QString socket = dir.filePath(QStringLiteral("s"));
        FakeCore core(socket);
        core.status = {{QStringLiteral("Linked"), true}, {QStringLiteral("State"), QStringLiteral("idle")},
                       {QStringLiteral("Server"), QStringLiteral("https://drive.example.com")},
                       {QStringLiteral("Workspaces"), QJsonArray{QJsonObject{{QStringLiteral("ID"), QStringLiteral("w1")}, {QStringLiteral("Name"), QStringLiteral("Docs")}, {QStringLiteral("HeldDeletes"), 26}}}}};
        core.workspaces = {QJsonObject{{QStringLiteral("ID"), QStringLiteral("w1")}, {QStringLiteral("Name"), QStringLiteral("Docs")}, {QStringLiteral("Chosen"), true}},
                           QJsonObject{{QStringLiteral("ID"), QStringLiteral("w2")}, {QStringLiteral("Name"), QStringLiteral("Projects")}, {QStringLiteral("Chosen"), false}}};
        core.conflicts = {QJsonObject{{QStringLiteral("Kind"), QStringLiteral("conflict")}, {QStringLiteral("Path"), QStringLiteral("report (conflict from laptop 2026-10-18).txt")}, {QStringLiteral("Detail"), QStringLiteral("edited here and on the server; both kept")}}};
        render(socket, QStringLiteral("linked"));
    }
};

QTEST_MAIN(TestQml)
#include "tst_qml.moc"
