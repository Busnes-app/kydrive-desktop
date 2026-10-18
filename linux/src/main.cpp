#include "CoreClient.h"

#include <KDBusService>
#include <KLocalizedQmlContext>
#include <KLocalizedString>
#include <KStatusNotifierItem>

#include <QAction>
#include <QApplication>
#include <QIcon>
#include <QMenu>
#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQuickWindow>

int main(int argc, char* argv[])
{
    // QApplication, not QGuiApplication: the tray's context menu is a QMenu.
    QApplication app(argc, argv);
    app.setApplicationName(QStringLiteral("drive"));
    app.setApplicationDisplayName(QStringLiteral("KyDrive"));
    app.setOrganizationDomain(QStringLiteral("kysecurity.com"));
    app.setDesktopFileName(QStringLiteral("com.kysecurity.drive"));
    app.setQuitOnLastWindowClosed(false);
    QIcon::setFallbackThemeName(QStringLiteral("breeze"));
    app.setWindowIcon(QIcon::fromTheme(QStringLiteral("com.kysecurity.drive")));
    KLocalizedString::setApplicationDomain("kydrive-desktop");
    KDBusService service(KDBusService::Unique);

    CoreClient core(CoreClient::defaultSocketPath(), QStringLiteral(KYDRIVE_SYNC_PATH));
    QQmlApplicationEngine engine;
    KLocalization::setupLocalizedContext(&engine);
    engine.rootContext()->setContextProperty(QStringLiteral("Core"), &core);
    engine.load(QUrl(QStringLiteral("qrc:/qml/Main.qml")));
    if (engine.rootObjects().isEmpty())
        return 1;
    auto* window = qobject_cast<QQuickWindow*>(engine.rootObjects().constFirst());

    KStatusNotifierItem tray(QStringLiteral("com.kysecurity.drive"));
    tray.setIconByName(QStringLiteral("com.kysecurity.drive"));
    tray.setTitle(QStringLiteral("KyDrive"));
    tray.setCategory(KStatusNotifierItem::ApplicationStatus);
    tray.setStatus(KStatusNotifierItem::Active);
    tray.setAssociatedWindow(window);
    tray.setStandardActionsEnabled(true);
    auto* menu = new QMenu;
    menu->addAction(i18n("Open KyDrive folder"), &core, &CoreClient::openFolder);
    menu->addAction(i18n("Open in browser"), &core, &CoreClient::openInBrowser);
    QAction* pause = menu->addAction(i18n("Pause syncing"));
    pause->setCheckable(true);
    QObject::connect(pause, &QAction::triggered, &core, &CoreClient::pause);
    tray.setContextMenu(menu);
    QObject::connect(&tray, &KStatusNotifierItem::quitRequested, &app, &QCoreApplication::quit);

    QObject::connect(&core, &CoreClient::statusChanged, &tray, [&] {
        const QVariantMap s = core.status();
        pause->setChecked(s.value(QStringLiteral("Paused")).toBool());
        tray.setToolTip(QStringLiteral("com.kysecurity.drive"), QStringLiteral("KyDrive"),
                        s.value(QStringLiteral("Linked")).toBool() ? s.value(QStringLiteral("State")).toString() : i18n("Not linked"));
    });
    QObject::connect(&core, &CoreClient::warning, &tray, [&](const QString& message) {
        tray.showMessage(QStringLiteral("KyDrive"), message, QStringLiteral("dialog-warning"));
    });
    // Launching the app again shows the running one's window.
    QObject::connect(&service, &KDBusService::activateRequested, window, [window] {
        window->show();
        window->raise();
        window->requestActivate();
    });
    // Start in the tray once linked; show the window to link.
    QObject::connect(&core, &CoreClient::statusChanged, window, [&] {
        if (!core.status().value(QStringLiteral("Linked")).toBool())
            window->show();
    }, Qt::SingleShotConnection);
    core.refresh();
    return app.exec();
}
