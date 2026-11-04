#include "Autostart.h"

#include <QDBusConnection>
#include <QDBusMessage>
#include <QDBusObjectPath>
#include <QFile>
#include <QSettings>
#include <QSignalSpy>
#include <QStandardPaths>
#include <QTemporaryDir>
#include <QTest>

// The Background portal as the Flatpak sees it: it records each request and answers when told.
class FakePortal : public QObject
{
    Q_OBJECT
    Q_CLASSINFO("D-Bus Interface", "org.freedesktop.portal.Background")
public:
    struct Request {
        QString path;
        bool autostart;
    };
    QList<Request> requests;

    void answer(int i, uint response, bool autostart)
    {
        QDBusMessage s = QDBusMessage::createSignal(requests[i].path, QStringLiteral("org.freedesktop.portal.Request"), QStringLiteral("Response"));
        s << response << QVariantMap{{QStringLiteral("autostart"), autostart}};
        QDBusConnection::sessionBus().send(s);
    }

public Q_SLOTS:
    QDBusObjectPath RequestBackground(const QString&, const QVariantMap& options)
    {
        const QString path = QStringLiteral("/org/freedesktop/portal/desktop/request/%1/%2")
                                 .arg(QDBusConnection::sessionBus().baseService().mid(1).replace(QLatin1Char('.'), QLatin1Char('_')),
                                      options.value(QStringLiteral("handle_token")).toString());
        requests.append({path, options.value(QStringLiteral("autostart")).toBool()});
        return QDBusObjectPath(path);
    }
};

class TestAutostart : public QObject
{
    Q_OBJECT
private slots:
    // Off until turned on; the setting is the entry's real presence, and it quotes the command.
    void nativeEntryFollowsTheSetting()
    {
        QTemporaryDir dir;
        const QString file = dir.filePath(QStringLiteral("autostart/com.kysecurity.drive.desktop"));
        Autostart a(file, QStringLiteral("/opt/My Apps/kydrive-desktop"), false);
        QSignalSpy changed(&a, &Autostart::enabledChanged);
        QVERIFY(!a.enabled());
        a.setEnabled(true);
        QVERIFY(a.enabled());
        QCOMPARE(changed.count(), 1);
        QFile f(file);
        QVERIFY(f.open(QIODevice::ReadOnly));
        const QString entry = QString::fromUtf8(f.readAll());
        QVERIFY2(entry.contains(QStringLiteral("\nExec=\"/opt/My Apps/kydrive-desktop\"\n")), qPrintable(entry));
        a.setEnabled(false);
        QVERIFY(!a.enabled());
        QVERIFY(!QFile::exists(file));
    }

    // An entry the desktop switched off reads as off, and turning it on rewrites it.
    void disabledEntryIsOff()
    {
        QTemporaryDir dir;
        const QString file = dir.filePath(QStringLiteral("com.kysecurity.drive.desktop"));
        QFile f(file);
        QVERIFY(f.open(QIODevice::WriteOnly));
        f.write("[Desktop Entry]\nType=Application\nExec=kydrive-desktop\nHidden=true\n");
        f.close();
        Autostart a(file, QStringLiteral("kydrive-desktop"), false);
        QVERIFY(!a.enabled());
        a.setEnabled(true);
        QVERIFY(a.enabled());
    }

    void failureIsReported()
    {
        QTemporaryDir dir;
        QFile blocker(dir.filePath(QStringLiteral("autostart")));
        QVERIFY(blocker.open(QIODevice::WriteOnly)); // a file where the directory should be
        blocker.close();
        Autostart a(dir.filePath(QStringLiteral("autostart/com.kysecurity.drive.desktop")), QStringLiteral("kydrive-desktop"), false);
        QSignalSpy failed(&a, &Autostart::failed);
        a.setEnabled(true);
        QCOMPARE(failed.count(), 1);
        QVERIFY(!a.enabled());
    }

    // The Flatpak asks the portal one request at a time: a change made while one is open is
    // sent once it is answered, so the setting ends as the host has it (council pass 2).
    void flatpakRequestsDoNotOverlap()
    {
        QDBusConnection bus = QDBusConnection::sessionBus();
        if (!bus.isConnected())
            QSKIP("no D-Bus session bus (ctest runs this under dbus-run-session when it is installed)");
        QStandardPaths::setTestModeEnabled(true);
        QSettings().remove(QStringLiteral("autostart"));
        FakePortal portal;
        QVERIFY(bus.registerObject(QStringLiteral("/org/freedesktop/portal/desktop"), &portal, QDBusConnection::ExportAllSlots));
        QVERIFY(bus.registerService(QStringLiteral("org.freedesktop.portal.Desktop")));
        Autostart a(QString(), QStringLiteral("kydrive-desktop"), true);
        QVERIFY(!a.enabled());
        a.setEnabled(true);
        a.setEnabled(false); // while the first is open
        QCOMPARE(portal.requests.size(), 1);
        portal.answer(0, 0, true);
        QTRY_COMPARE(portal.requests.size(), 2);
        QVERIFY(!portal.requests[1].autostart);
        QVERIFY(a.enabled()); // what the host has until the second answer
        portal.answer(1, 0, false);
        QTRY_VERIFY(!a.enabled());
        // A refusal reads as off and is reported.
        QSignalSpy failed(&a, &Autostart::failed);
        a.setEnabled(true);
        QCOMPARE(portal.requests.size(), 3);
        portal.answer(2, 1, false);
        QTRY_COMPARE(failed.count(), 1);
        QVERIFY(!a.enabled());
        bus.unregisterService(QStringLiteral("org.freedesktop.portal.Desktop"));
        bus.unregisterObject(QStringLiteral("/org/freedesktop/portal/desktop"));
    }
};

QTEST_GUILESS_MAIN(TestAutostart)
#include "tst_autostart.moc"
