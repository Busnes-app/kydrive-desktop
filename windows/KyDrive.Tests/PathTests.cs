using System.Diagnostics;

namespace KyDrive.Tests;

// On Windows the core listens on filepath.Join(os.UserCacheDir(), "kydrive", "sync.sock"), and
// Go's UserCacheDir there is the %LocalAppData% variable (core/cmd/kydrive-sync/main.go).
public class PathTests
{
    static Func<string, string?> Env(string? appData) => name => name == "LOCALAPPDATA" ? appData : null;

    [Fact]
    public void SocketPathIsLocalAppDataKydriveSyncSock()
    {
        if (!OperatingSystem.IsWindows()) return;
        Assert.Equal(@"C:\Users\u\AppData\Local\kydrive\sync.sock", Paths.SocketPath(Env(@"C:\Users\u\AppData\Local")));
        Assert.Equal(@"C:\Users\u\AppData\Local\kydrive\sync.sock", Paths.SocketPath(Env(@"C:\Users\u\AppData\Local\")));
    }

    [Fact]
    public void DefaultReadsTheLocalAppDataVariable()
    {
        if (!OperatingSystem.IsWindows()) return;
        Assert.Equal(Path.Combine(Environment.GetEnvironmentVariable("LOCALAPPDATA")!, "kydrive", "sync.sock"), Paths.SocketPath());
    }

    // Never a relative path, which would resolve against whatever the working directory is.
    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData(@"AppData\Local")]
    public void RefusesMissingOrRelativeLocalAppData(string? appData)
    {
        Assert.Throws<InvalidOperationException>(() => Paths.SocketPath(Env(appData)));
    }

    // The real core, given its own %LocalAppData%, answers on the path the shell computes, and moves
// state an earlier release left in roaming %AppData% there.
    [Fact]
    public async Task RealCoreListensWhereTheShellLooks()
    {
        var exe = Environment.GetEnvironmentVariable("KYDRIVE_SYNC_EXE");
        if (string.IsNullOrEmpty(exe))
        {
            Assert.True(Environment.GetEnvironmentVariable("CI") != "true", "CI must set KYDRIVE_SYNC_EXE");
            return;
        }
        var home = Path.Combine(Path.GetTempPath(), "kyd-" + Guid.NewGuid().ToString("N")[..8]);
        Directory.CreateDirectory(home);
        var start = new ProcessStartInfo(exe) { UseShellExecute = false, CreateNoWindow = true };
        var local = Path.Combine(home, "Local");
        var roaming = Path.Combine(home, "Roaming");
        Directory.CreateDirectory(Path.Combine(roaming, "kydrive"));
        File.WriteAllBytes(Path.Combine(roaming, "kydrive", "state.db"), []); // an empty file is a new SQLite database
        start.Environment["LOCALAPPDATA"] = local;
        start.Environment["APPDATA"] = roaming;
        start.Environment["USERPROFILE"] = home;
        start.Environment.Remove("XDG_RUNTIME_DIR");
        using var core = Process.Start(start)!;
        try
        {
            var client = new CoreClient(Paths.SocketPath(Env(local)));
            Status? s = null;
            for (var i = 0; i < 100 && s == null; i++)
            {
                try { s = await client.Status(); }
                catch (CoreNotRunningException) { await Task.Delay(200); }
            }
            Assert.NotNull(s);
            Assert.False(s.Linked);
            Assert.Equal("unlinked", s.State);
            Assert.True(File.Exists(Path.Combine(local, "kydrive", "state.db")), "state not moved to %LocalAppData%");
            Assert.False(File.Exists(Path.Combine(roaming, "kydrive", "state.db")), "state left in roaming %AppData%");
        }
        finally
        {
            core.Kill(true);
            core.WaitForExit();
        }
    }
}
