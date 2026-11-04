using System.Diagnostics;

namespace KyDrive.Tests;

// Q5: only a core reporting an older release than the shell is replaced.
public class UpgradeTests
{
    [Theory]
    [InlineData("0.5.9", true)]
    [InlineData("0.6.0", false)]
    [InlineData("0.10.0", false)]
    [InlineData("", false)]
    [InlineData(null, false)]
    public void OnlyAnOlderCoreIsReplaced(string? core, bool older) =>
        Assert.Equal(older, Upgrade.Older(core, new Version(0, 6, 0, 0)));

    // Q8: the answering core is ended only if it runs from another folder under its own name.
    [Fact]
    public void EndsOnlyACoreFromAnotherFolder()
    {
        if (!OperatingSystem.IsWindows()) return;
        var dir = Directory.CreateTempSubdirectory("kyd-upgrade").FullName;
        var exe = Path.Join(dir, "kyd-test-sync.exe");
        File.Copy(Path.Join(Environment.GetFolderPath(Environment.SpecialFolder.System), "PING.EXE"), exe);
        using var old = Process.Start(new ProcessStartInfo(exe, "-n 120 127.0.0.1") { CreateNoWindow = true, UseShellExecute = false })!;
        try
        {
            var elsewhere = Path.Join(dir, "new", "kydrive-sync.exe");
            Assert.False(Upgrade.EndCore(old.Id, elsewhere)); // not named kydrive-sync
            Assert.False(Upgrade.EndCore(old.Id, exe, "kyd-test-sync")); // the bundled one
            Assert.False(old.HasExited);
            Assert.True(Upgrade.EndCore(old.Id, elsewhere, "kyd-test-sync"));
            Assert.True(old.HasExited);
        }
        finally
        {
            if (!old.HasExited) old.Kill();
        }
    }
}
