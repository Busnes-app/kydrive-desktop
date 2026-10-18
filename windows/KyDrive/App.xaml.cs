using System.Runtime.InteropServices;
using Microsoft.UI.Xaml;
using Microsoft.Windows.AppNotifications;

namespace KyDrive;

public partial class App : Application
{
    MainWindow? window;

    public App() => InitializeComponent();

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        window = new MainWindow(Notifications());
    }

    // Called from any thread.
    public void Reveal() => window?.Reveal();

    // Toasts are optional: without them the tray and window still work.
    bool Notifications()
    {
        try
        {
            AppNotificationManager.Default.NotificationInvoked += (_, _) => Reveal();
            AppNotificationManager.Default.Register();
            return true;
        }
        catch (COMException)
        {
            return false;
        }
    }
}
