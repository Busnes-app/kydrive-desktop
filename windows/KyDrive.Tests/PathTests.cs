using System.Diagnostics;

namespace KyDrive.Tests;

// The core listens on filepath.Join(os.UserConfigDir(), "kydrive", "sync.sock"), and Go's
// UserConfigDir on Windows is the %AppData% variable (core/cmd/kydrive-sync/main.go).
public class PathTests
{
    static Func<string, string?> Env(string? appData) => name => name == "APPDATA" ? appData : null;

    [Fact]
    public void SocketPathIsAppDataKydriveSyncSock()
    {
        if (!OperatingSystem.IsWindows()) return;
        Assert.Equal(@"C:\Users\u\AppData\Roaming\kydrive\sync.sock", Paths.SocketPath(Env(@"C:\Users\u\AppData\Roaming")));
        Assert.Equal(@"C:\Users\u\AppData\Roaming\kydrive\sync.sock", Paths.SocketPath(Env(@"C:\Users\u\AppData\Roaming\")));
    }

    [Fact]
    public void DefaultReadsTheAppDataVariable()
    {
        if (!OperatingSystem.IsWindows()) return;
        Assert.Equal(Path.Combine(Environment.GetEnvironmentVariable("APPDATA")!, "kydrive", "sync.sock"), Paths.SocketPath());
    }

    // Never a relative path, which would resolve against whatever the working directory is.
    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData(@"AppData\Roaming")]
    public void RefusesMissingOrRelativeAppData(string? appData)
    {
        Assert.Throws<InvalidOperationException>(() => Paths.SocketPath(Env(appData)));
    }

    // The real core, given its own %AppData%, answers on the path the shell computes.
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
        start.Environment["APPDATA"] = home;
        start.Environment["USERPROFILE"] = home;
        start.Environment.Remove("XDG_RUNTIME_DIR");
        using var core = Process.Start(start)!;
        try
        {
            var client = new CoreClient(Paths.SocketPath(Env(home)));
            Status? s = null;
            for (var i = 0; i < 100 && s == null; i++)
            {
                try { s = await client.Status(); }
                catch (CoreNotRunningException) { await Task.Delay(200); }
            }
            Assert.NotNull(s);
            Assert.False(s.Linked);
            Assert.Equal("unlinked", s.State);
        }
        finally
        {
            core.Kill(true);
            core.WaitForExit();
        }
    }
}
