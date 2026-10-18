using System.Diagnostics;
using H.NotifyIcon;
using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Microsoft.Windows.AppNotifications;
using Windows.Graphics;
using Windows.System;

namespace KyDrive;

// The KyDrive window and tray icon: link this computer, then choose workspaces, answer held
// deletes, see conflicts and unlink. Everything shown comes from the core, as plain text.
sealed class MainWindow : Window
{
    readonly CoreClient core = new(Paths.SocketPath());
    readonly Warnings warnings = new();
    readonly DispatcherQueue ui;
    readonly DispatcherQueueTimer poll;
    readonly TaskbarIcon tray;
    readonly ToggleMenuFlyoutItem pauseItem = new() { Text = "Pause syncing" };
    readonly bool notifications;
    Status? status;
    string coreError = "", actionError = "", heldKey = "", conflictKey = "";
    bool busy, linking, started, shown, wasLinked, quitting;

    readonly InfoBar errorBar = new() { Severity = InfoBarSeverity.Error, IsClosable = false };
    readonly StackPanel linkForm = new() { Spacing = 12 };
    readonly TextBox server = new() { Header = "KyDrive address", PlaceholderText = "https://drive.example.com" };
    readonly TextBox deviceName = new() { Header = "Computer name", PlaceholderText = "This computer's name" };
    readonly Button linkButton = new();
    readonly StackPanel linked = new() { Spacing = 12 };
    readonly TextBlock serverText = Plain("");
    readonly TextBlock stateText = Plain("");
    readonly Button pauseButton = new();
    readonly StackPanel held = new() { Spacing = 8 };
    readonly StackPanel choices = new();
    readonly TextBlock conflictsHeading = Heading("Conflicts and kept files");
    readonly StackPanel conflicts = new() { Spacing = 4 };

    public MainWindow(bool notifications)
    {
        this.notifications = notifications;
        ui = DispatcherQueue;
        Title = "KyDrive";
        SystemBackdrop = new MicaBackdrop();
        AppWindow.SetIcon(Path.Join(AppContext.BaseDirectory, "Assets", "kydrive.ico"));
        AppWindow.Resize(new SizeInt32(560, 760));
        // Closing hides to the tray; Quit in the tray menu exits.
        AppWindow.Closing += (w, e) =>
        {
            if (quitting)
                return;
            e.Cancel = true;
            w.Hide();
        };

        server.TextChanged += (_, _) => Render();
        linkButton.Click += (_, _) => Link();
        linkForm.Children.Add(server);
        linkForm.Children.Add(deviceName);
        linkForm.Children.Add(linkButton);

        pauseButton.Click += (_, _) => TogglePause();
        var actions = Row(Act("Open folder", OpenFolder), Act("Open in browser", OpenInBrowser), pauseButton, Act("Unlink…", ConfirmUnlink));
        var apply = Act("Apply", () => Run(c => c.Choose(Chosen()), reload: true));
        var reload = Act("Reload", LoadWorkspaces);
        foreach (var e in new UIElement[] { serverText, stateText, actions, held, Heading("Workspaces on this computer"), choices,
                     Plain("Unchecking a workspace removes its files from this computer. Edits that never uploaded are kept in the Unsynced folder."),
                     Row(apply, reload), conflictsHeading, conflicts })
            linked.Children.Add(e);

        var page = new StackPanel { Spacing = 16, Padding = new Thickness(24) };
        page.Children.Add(errorBar);
        page.Children.Add(linkForm);
        page.Children.Add(linked);
        Content = new ScrollViewer { Content = page };

        pauseItem.Command = Command(TogglePause);
        var menu = new MenuFlyout();
        menu.Items.Add(new MenuFlyoutItem { Text = "Open KyDrive", Command = Command(Reveal) });
        menu.Items.Add(new MenuFlyoutItem { Text = "Open KyDrive folder", Command = Command(OpenFolder) });
        menu.Items.Add(new MenuFlyoutItem { Text = "Open in browser", Command = Command(OpenInBrowser) });
        menu.Items.Add(pauseItem);
        menu.Items.Add(new MenuFlyoutSeparator());
        menu.Items.Add(new MenuFlyoutItem { Text = "Quit", Command = Command(Quit) });
        tray = new TaskbarIcon
        {
            ToolTipText = "KyDrive",
            Icon = new System.Drawing.Icon(Path.Join(AppContext.BaseDirectory, "Assets", "kydrive.ico")),
            ContextFlyout = menu,
            ContextMenuMode = ContextMenuMode.PopupMenu,
            LeftClickCommand = Command(Reveal),
            NoLeftClickDelay = true,
        };
        tray.ForceCreate(enablesEfficiencyMode: false);

        poll = ui.CreateTimer();
        poll.Interval = TimeSpan.FromSeconds(3);
        poll.Tick += (_, _) => _ = Refresh();
        poll.Start();
        Render();
        _ = Refresh();
    }

    // Safe from any thread.
    public void Reveal() => ui.TryEnqueue(() =>
    {
        AppWindow.Show();
        Activate();
    });

    async Task Refresh()
    {
        if (busy)
            return;
        busy = true;
        try
        {
            status = await core.Status();
            coreError = "";
            RenderConflicts(await core.Conflicts());
            foreach (var message in warnings.Next(await core.Activity(warnings.After)))
                Notify(message);
            if (status.Linked != wasLinked)
            {
                wasLinked = status.Linked;
                choices.Children.Clear();
                if (wasLinked)
                    LoadWorkspaces();
            }
            // Start in the tray once linked; show the window to link.
            if (!shown && !status.Linked)
                Reveal();
            shown = true;
        }
        catch (CoreNotRunningException e)
        {
            coreError = e.Message;
            // Nothing listening: start the bundled core once, then let the next poll retry.
            if (!started)
            {
                started = true;
                StartCore();
            }
            else if (!shown)
            {
                shown = true;
                Reveal();
            }
        }
        catch (CoreException e)
        {
            coreError = e.Message;
        }
        finally
        {
            busy = false;
            Render();
        }
    }

    void Render()
    {
        var s = status;
        var isLinked = s?.Linked == true;
        var error = coreError != "" ? coreError : actionError != "" ? actionError : s?.Error ?? "";
        errorBar.Message = error;
        errorBar.IsOpen = error != "";

        linkForm.Visibility = Show(s != null && !isLinked);
        server.IsEnabled = deviceName.IsEnabled = !linking;
        linkButton.Content = linking ? "Waiting for approval in your browser…" : "Link";
        linkButton.IsEnabled = !linking && server.Text.Trim() != "";

        linked.Visibility = Show(isLinked);
        tray.ToolTipText = isLinked ? "KyDrive: " + s!.State : "KyDrive: not linked";
        if (s == null)
            return;
        serverText.Text = s.Server ?? "";
        stateText.Text = "Status: " + s.State + (s.Paused ? ", paused" : "");
        pauseButton.Content = s.Paused ? "Resume" : "Pause";
        pauseItem.IsChecked = s.Paused;
        RenderHeld(s.Workspaces ?? []);
    }

    // The large-delete guard: deletes wait here until the user answers.
    void RenderHeld(WorkspaceStatus[] workspaces)
    {
        var waiting = workspaces.Where(w => w.HeldDeletes > 0).ToList();
        var key = string.Join("\n", waiting);
        if (key == heldKey)
            return;
        heldKey = key;
        held.Children.Clear();
        foreach (var w in waiting)
            held.Children.Add(new InfoBar
            {
                Severity = InfoBarSeverity.Warning,
                IsOpen = true,
                IsClosable = false,
                Message = $"{w.HeldDeletes} items were deleted from {w.Name} on this computer.",
                Content = Row(Act("Delete from KyDrive too", () => Run(c => c.ConfirmDeletes(w.ID, true))),
                              Act("Restore them", () => Run(c => c.ConfirmDeletes(w.ID, false)))),
            });
    }

    void RenderConflicts(Issue[] issues)
    {
        var key = string.Join("\n", issues);
        if (key == conflictKey)
            return;
        conflictKey = key;
        conflictsHeading.Visibility = Show(issues.Length > 0);
        conflicts.Children.Clear();
        foreach (var i in issues)
            conflicts.Children.Add(Plain($"{i.Path} — {i.Detail}"));
    }

    async void LoadWorkspaces()
    {
        try
        {
            var list = await core.Workspaces();
            actionError = "";
            choices.Children.Clear();
            // The name sits in its own TextBlock: plain text whatever it contains.
            foreach (var c in list)
                choices.Children.Add(new CheckBox { Content = Plain(c.Name), IsChecked = c.Chosen, Tag = c.ID });
        }
        catch (CoreException e)
        {
            actionError = e.Message;
        }
        Render();
    }

    IEnumerable<string> Chosen() =>
        choices.Children.OfType<CheckBox>().Where(b => b.IsChecked == true).Select(b => (string)b.Tag).ToList();

    async void Run(Func<CoreClient, Task> call, bool reload = false)
    {
        try
        {
            await call(core);
            actionError = "";
        }
        catch (CoreException e)
        {
            actionError = e.Message;
        }
        if (reload)
            LoadWorkspaces();
        await Refresh();
    }

    async void Link()
    {
        linking = true;
        Render();
        try
        {
            // The core opens the browser; this returns once the user approves there.
            await core.Link(server.Text, deviceName.Text);
            actionError = "";
        }
        catch (CoreException e)
        {
            actionError = e.Message;
        }
        linking = false;
        await Refresh();
    }

    void TogglePause() => Run(c => c.Pause(!(status?.Paused ?? false)));

    async void ConfirmUnlink()
    {
        var removeFiles = new CheckBox { Content = Plain("Also remove KyDrive files from this computer (unuploaded edits are kept in Unsynced)") };
        var body = new StackPanel { Spacing = 12 };
        body.Children.Add(Plain("KyDrive stops syncing and this computer is revoked on the server."));
        body.Children.Add(removeFiles);
        var dialog = new ContentDialog
        {
            Title = "Unlink this computer?",
            Content = body,
            PrimaryButtonText = "Unlink",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Close,
            XamlRoot = this.Content.XamlRoot,
        };
        if (await dialog.ShowAsync() == ContentDialogResult.Primary)
            Run(c => c.Unlink(removeFiles.IsChecked == true));
    }

    // Explorer by full path (never a search of the working directory), given only an existing
    // absolute directory, so a path from the core cannot run anything.
    void OpenFolder()
    {
        var root = status?.Root;
        if (string.IsNullOrEmpty(root) || !Path.IsPathFullyQualified(root) || !Directory.Exists(root))
            return;
        var explorer = new ProcessStartInfo(Path.Join(Environment.GetFolderPath(Environment.SpecialFolder.Windows), "explorer.exe"));
        explorer.ArgumentList.Add(root);
        Process.Start(explorer)?.Dispose();
    }

    async void OpenInBrowser()
    {
        if (Safe.WebUri(status?.Server) is { } uri)
            await Launcher.LaunchUriAsync(uri);
    }

    // The core ships beside the app and runs per user, in the background, after the app quits.
    static void StartCore()
    {
        var exe = Path.Join(AppContext.BaseDirectory, "kydrive-sync.exe");
        if (File.Exists(exe))
            Process.Start(new ProcessStartInfo(exe) { UseShellExecute = false, CreateNoWindow = true, WorkingDirectory = AppContext.BaseDirectory })?.Dispose();
    }

    void Notify(string message)
    {
        if (notifications)
            AppNotificationManager.Default.Show(new AppNotification(Safe.ToastXml("KyDrive", message)));
    }

    void Quit()
    {
        quitting = true;
        poll.Stop();
        tray.Dispose();
        Close();
        Application.Current.Exit();
    }

    // Tray menu commands may arrive off the UI thread.
    XamlUICommand Command(Action action)
    {
        var c = new XamlUICommand();
        c.ExecuteRequested += (_, _) => ui.TryEnqueue(() => action());
        return c;
    }

    static TextBlock Plain(string text) => new() { Text = text, TextWrapping = TextWrapping.Wrap };

    static TextBlock Heading(string text) => new() { Text = text, Style = (Style)Application.Current.Resources["SubtitleTextBlockStyle"] };

    static Button Act(string label, Action click)
    {
        var b = new Button { Content = label };
        b.Click += (_, _) => click();
        return b;
    }

    static StackPanel Row(params UIElement[] items)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        foreach (var i in items)
            row.Children.Add(i);
        return row;
    }

    static Visibility Show(bool visible) => visible ? Visibility.Visible : Visibility.Collapsed;
}
