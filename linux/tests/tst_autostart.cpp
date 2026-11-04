#include "Autostart.h"

#include <QFile>
#include <QSignalSpy>
#include <QTemporaryDir>
#include <QTest>

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
};

QTEST_GUILESS_MAIN(TestAutostart)
#include "tst_autostart.moc"
