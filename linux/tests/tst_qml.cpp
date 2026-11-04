#include "Appearance.h"
#include "Autostart.h"
#include "CoreClient.h"
#include "FakeCore.h"

#include <KLocalizedQmlContext>

#include <QIcon>
#include <QNetworkAccessManager>
#include <QQmlNetworkAccessManagerFactory>
#include <QQuickItem>

#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQuickWindow>
#include <QSettings>
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
// KYDRIVE_SHOT_DIR set it also saves each state as a 1440x900 PNG.
class TestQml : public QObject
{
    Q_OBJECT
private:
    QTemporaryDir m_settingsDir;
    QSettings m_settings{m_settingsDir.filePath(QStringLiteral("kydrive.ini")), QSettings::IniFormat};
    Appearance m_appearance{&m_settings};
    Autostart m_autostart{m_settingsDir.filePath(QStringLiteral("autostart/com.kysecurity.drive.desktop")), QStringLiteral("kydrive-desktop"), false};
    QStringList m_warnings;

    void save(QQuickWindow* window, const QString& shot)
    {
        if (const QString dir = qEnvironmentVariable("KYDRIVE_SHOT_DIR"); !dir.isEmpty())
            window->grabWindow().save(dir + QLatin1Char('/') + shot + QStringLiteral(".png"));
    }

    // render shows the window for the fake core's state and returns its top item.
    QQuickItem* render(QQmlApplicationEngine& engine, CoreClient& client, const QString& shot)
    {
        m_warnings.clear();
        connect(&engine, &QQmlEngine::warnings, this, [this](const QList<QQmlError>& list) {
            for (const QQmlError& e : list)
                m_warnings << e.toString();
        });
        loadBundledFonts();
        QIcon::setThemeName(QStringLiteral("breeze")); // so message icons show, as on a desktop
        KLocalization::setupLocalizedContext(&engine);
        engine.rootContext()->setContextProperty(QStringLiteral("Core"), &client);
        engine.rootContext()->setContextProperty(QStringLiteral("Appearance"), &m_appearance);
        engine.rootContext()->setContextProperty(QStringLiteral("Autostart"), &m_autostart);
        engine.load(QUrl(QStringLiteral("qrc:/qml/Main.qml")));
        if (engine.rootObjects().size() != 1)
            return nullptr;
        auto* window = qobject_cast<QQuickWindow*>(engine.rootObjects().constFirst());
        QSignalSpy status(&client, &CoreClient::statusChanged);
        client.refresh();
        if (!status.wait())
            return nullptr;
        if (!qEnvironmentVariable("KYDRIVE_SHOT_DIR").isEmpty())
            window->resize(1440, 900);
        window->show();
        if (!QTest::qWaitForWindowExposed(window))
            return nullptr;
        QTest::qWait(500);
        if (!m_warnings.isEmpty())
            qWarning() << m_warnings;
        save(window, shot);
        return m_warnings.isEmpty() ? window->contentItem() : nullptr;
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

    // A plain 401, or no credential in the keyring: the sign-in card instead of the link form.
    void signedOut()
    {
        QTemporaryDir dir;
        const QString socket = dir.filePath(QStringLiteral("s"));
        FakeCore core(socket);
        core.status = {{QStringLiteral("Linked"), false}, {QStringLiteral("State"), QStringLiteral("signed_out")},
                       {QStringLiteral("Server"), QStringLiteral("https://drive.example.com")}};
        core.conflicts = {};
        show(core, socket, QStringLiteral("signed-out"));
    }

    void linked()
    {
        QTemporaryDir dir;
        const QString socket = dir.filePath(QStringLiteral("s"));
        FakeCore core(socket);
        fillLinked(core);
        show(core, socket, QStringLiteral("linked"));
    }

    // Each theme pair and half renders without warnings: the main window, the Appearance page and
    // the link form.
    void themes_data()
    {
        QTest::addColumn<QString>("pair");
        QTest::addColumn<QString>("mode");
        QTest::addColumn<QString>("shot");
        QTest::newRow("busnes light") << "busnes-pair" << "light" << "busnes-light";
        QTest::newRow("busnes dark") << "busnes-pair" << "dark" << "busnes-dark";
        for (const ThemePair& p : themePairs())
            for (const char* mode : {"light", "dark"})
                if (p.id != QLatin1String("busnes-pair"))
                    QTest::newRow(qPrintable(p.id + QLatin1Char(' ') + QLatin1String(mode)))
                        << p.id << QString::fromLatin1(mode) << QString(p.id.chopped(5) + QLatin1Char('-') + QLatin1String(mode));
    }

    void themes()
    {
        QFETCH(QString, pair);
        QFETCH(QString, mode);
        QFETCH(QString, shot);
        m_appearance.setPair(pair);
        m_appearance.setMode(mode);
        QTemporaryDir dir;
        const QString socket = dir.filePath(QStringLiteral("s"));
        FakeCore core(socket);
        fillLinked(core);
        core.status[QStringLiteral("Error")] = QStringLiteral("Could not reach drive.example.com");
        CoreClient client(socket, QString());
        QQmlApplicationEngine engine;
        QVERIFY(render(engine, client, QStringLiteral("main-") + shot));
        auto* window = qobject_cast<QQuickWindow*>(engine.rootObjects().constFirst());
        QMetaObject::invokeMethod(window, "showAppearance");
        QTest::qWait(800);
        QVERIFY2(m_warnings.isEmpty(), qPrintable(m_warnings.join(QLatin1Char('\n'))));
        save(window, QStringLiteral("settings-") + shot);

        // The link form, in the same half.
        QTemporaryDir unlinkedDir;
        const QString unlinkedSocket = unlinkedDir.filePath(QStringLiteral("s"));
        FakeCore unlinkedCore(unlinkedSocket);
        unlinkedCore.status = {{QStringLiteral("Linked"), false}, {QStringLiteral("State"), QStringLiteral("unlinked")}};
        unlinkedCore.conflicts = {};
        CoreClient unlinkedClient(unlinkedSocket, QString());
        QQmlApplicationEngine unlinkedEngine;
        QVERIFY(render(unlinkedEngine, unlinkedClient, QStringLiteral("unlinked-") + shot));
        m_appearance.setPair(QStringLiteral("busnes-pair"));
        m_appearance.setMode(QStringLiteral("system"));
    }

private:
    static void fillLinked(FakeCore& core)
    {
        core.status = {{QStringLiteral("Linked"), true}, {QStringLiteral("State"), QStringLiteral("idle")},
                       {QStringLiteral("Server"), QStringLiteral("https://drive.example.com")},
                       {QStringLiteral("Workspaces"), QJsonArray{QJsonObject{{QStringLiteral("ID"), QStringLiteral("w1")}, {QStringLiteral("Name"), QStringLiteral("Docs")}, {QStringLiteral("HeldDeletes"), 26}}}}};
        core.workspaces = {QJsonObject{{QStringLiteral("ID"), QStringLiteral("w1")}, {QStringLiteral("Name"), QStringLiteral("Docs")}, {QStringLiteral("Chosen"), true}},
                           QJsonObject{{QStringLiteral("ID"), QStringLiteral("w2")}, {QStringLiteral("Name"), QStringLiteral("Projects")}, {QStringLiteral("Chosen"), false}}};
        core.conflicts = {QJsonObject{{QStringLiteral("Kind"), QStringLiteral("conflict")}, {QStringLiteral("Path"), QStringLiteral("report (conflict from laptop 2026-10-18).txt")}, {QStringLiteral("Detail"), QStringLiteral("edited here and on the server; both kept")}}};
    }

private slots:

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
