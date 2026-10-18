#include "CoreClient.h"
#include "FakeCore.h"

#include <KLocalizedQmlContext>

#include <QNetworkAccessManager>
#include <QQmlNetworkAccessManagerFactory>
#include <QQuickItem>

#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQuickWindow>
#include <QSignalSpy>
#include <QTemporaryDir>
#include <QTest>

// Records every network request QML makes; markup from the server must never cause one.
class RecordingNam : public QNetworkAccessManager
{
public:
    explicit RecordingNam(QStringList* log, QObject* parent) : QNetworkAccessManager(parent), m_log(log) {}

protected:
    QNetworkReply* createRequest(Operation op, const QNetworkRequest& req, QIODevice* data) override
    {
        *m_log << req.url().toString();
        return QNetworkAccessManager::createRequest(op, req, data);
    }

private:
    QStringList* m_log;
};

class RecordingFactory : public QQmlNetworkAccessManagerFactory
{
public:
    QStringList requests;
    QNetworkAccessManager* create(QObject* parent) override { return new RecordingNam(&requests, parent); }
};

// Loads the real Main.qml against a fake core and fails on any QML warning. With
// KYDRIVE_SHOT_DIR set it also saves each state as a PNG.
class TestQml : public QObject
{
    Q_OBJECT
private:
    // render shows the window for the fake core's state and returns its top item.
    QQuickItem* render(QQmlApplicationEngine& engine, CoreClient& client, const QString& shot)
    {
        QStringList warnings;
        connect(&engine, &QQmlEngine::warnings, this, [&](const QList<QQmlError>& list) {
            for (const QQmlError& e : list)
                warnings << e.toString();
        });
        KLocalization::setupLocalizedContext(&engine);
        engine.rootContext()->setContextProperty(QStringLiteral("Core"), &client);
        engine.load(QUrl(QStringLiteral("qrc:/qml/Main.qml")));
        if (engine.rootObjects().size() != 1)
            return nullptr;
        auto* window = qobject_cast<QQuickWindow*>(engine.rootObjects().constFirst());
        QSignalSpy status(&client, &CoreClient::statusChanged);
        client.refresh();
        if (!status.wait())
            return nullptr;
        window->show();
        if (!QTest::qWaitForWindowExposed(window))
            return nullptr;
        QTest::qWait(500);
        if (!warnings.isEmpty())
            qWarning() << warnings;
        if (const QString dir = qEnvironmentVariable("KYDRIVE_SHOT_DIR"); !dir.isEmpty())
            window->grabWindow().save(dir + QLatin1Char('/') + shot + QStringLiteral(".png"));
        return warnings.isEmpty() ? window->contentItem() : nullptr;
    }

    void show(FakeCore&, const QString& socket, const QString& shot)
    {
        CoreClient client(socket, QString());
        QQmlApplicationEngine engine;
        QVERIFY(render(engine, client, shot));
    }

private slots:
    void unlinked()
    {
        QTemporaryDir dir;
        const QString socket = dir.filePath(QStringLiteral("s"));
        FakeCore core(socket);
        core.status = {{QStringLiteral("Linked"), false}, {QStringLiteral("State"), QStringLiteral("unlinked")}};
        core.conflicts = {};
        show(core, socket, QStringLiteral("unlinked"));
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
        show(core, socket, QStringLiteral("linked"));
    }

    // Names and paths from the server render as the literal characters and load nothing.
    void markupStaysText()
    {
        const QString img = QStringLiteral("<img src=\"http://127.0.0.1:9/leak.png\">");
        QTemporaryDir dir;
        const QString socket = dir.filePath(QStringLiteral("s"));
        FakeCore core(socket);
        core.status = {{QStringLiteral("Linked"), true}, {QStringLiteral("State"), QStringLiteral("idle")}, {QStringLiteral("Error"), img + QStringLiteral("error")},
                       {QStringLiteral("Workspaces"), QJsonArray{QJsonObject{{QStringLiteral("ID"), QStringLiteral("w1")}, {QStringLiteral("Name"), img + QStringLiteral("Docs")}, {QStringLiteral("HeldDeletes"), 26}}}}};
        core.workspaces = {QJsonObject{{QStringLiteral("ID"), QStringLiteral("w1")}, {QStringLiteral("Name"), img + QStringLiteral("Docs")}, {QStringLiteral("Chosen"), true}}};
        core.conflicts = {QJsonObject{{QStringLiteral("Kind"), QStringLiteral("conflict")}, {QStringLiteral("Path"), img + QStringLiteral("<b>x</b>.txt")}, {QStringLiteral("Detail"), QStringLiteral("both kept")}}};
        CoreClient client(socket, QString());
        QQmlApplicationEngine engine;
        RecordingFactory nam;
        engine.setNetworkAccessManagerFactory(&nam);
        QQuickItem* top = render(engine, client, QStringLiteral("markup"));
        QVERIFY(top);

        // What each text item shows: a TextEdit's plain content, a Text's text when plain.
        QStringList shown;
        QList<QQuickItem*> todo{top};
        while (!todo.isEmpty()) {
            QQuickItem* item = todo.takeFirst();
            todo << item->childItems();
            if (!item->isVisible())
                continue;
            if (item->inherits("QQuickTextEdit")) {
                QString text;
                QMetaObject::invokeMethod(item, "getText", Q_RETURN_ARG(QString, text), Q_ARG(int, 0), Q_ARG(int, item->property("length").toInt()));
                shown << text;
            } else if (item->property("textFormat").isValid()) {
                const QString text = item->property("text").toString();
                // Text.PlainText is 0; any other format must not carry the server's tags unescaped.
                if (item->property("textFormat").toInt() != 0)
                    QVERIFY2(!text.contains(QStringLiteral("<img")) && !text.contains(QStringLiteral("<b>")), qPrintable(QStringLiteral("rendered as markup: ") + text));
                shown << text;
            }
        }
        const QString all = shown.join(QLatin1Char('\n'));
        for (const QString& literal : {img + QStringLiteral("<b>x</b>.txt"), img + QStringLiteral("Docs"), img + QStringLiteral("error")})
            QVERIFY2(all.contains(literal), qPrintable(QStringLiteral("not shown literally: ") + literal + QStringLiteral("\n--\n") + all));
        QVERIFY2(nam.requests.isEmpty(), qPrintable(nam.requests.join(QLatin1Char(' '))));
    }
};

QTEST_MAIN(TestQml)
#include "tst_qml.moc"
