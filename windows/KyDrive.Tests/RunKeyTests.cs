using Microsoft.Win32;

namespace KyDrive.Tests;

// Start at login for the zip build, against private keys under HKCU\Software\KyDriveTests.
public class RunKeyTests
{
    [Fact]
    public void FollowsTheSettingAndTaskManager()
    {
        if (!OperatingSystem.IsWindows()) return;
        var root = @"Software\KyDriveTests\" + Guid.NewGuid().ToString("N");
        try
        {
            var key = new RunKey(@"C:\Apps\Ky Drive\KyDrive.exe", root + @"\Run", root + @"\Approved");
            Assert.False(key.Enabled);
            key.Set(true);
            Assert.True(key.Enabled);
            using (var run = Registry.CurrentUser.OpenSubKey(root + @"\Run"))
                Assert.Equal("\"C:\\Apps\\Ky Drive\\KyDrive.exe\"", run!.GetValue("KyDrive"));
            // Task Manager turned it off.
            using (var approved = Registry.CurrentUser.CreateSubKey(root + @"\Approved"))
                approved.SetValue("KyDrive", new byte[] { 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0 }, RegistryValueKind.Binary);
            Assert.False(key.Enabled);
            key.Set(true);
            Assert.True(key.Enabled);
            // Another copy of KyDrive registered elsewhere is not this one until it adopts the value
            // (a release unzipped to a new folder), keeping Task Manager's switch.
            var moved = new RunKey(@"D:\new\KyDrive.exe", root + @"\Run", root + @"\Approved");
            Assert.False(moved.Enabled);
            using (var approved = Registry.CurrentUser.CreateSubKey(root + @"\Approved"))
                approved.SetValue("KyDrive", new byte[] { 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0 }, RegistryValueKind.Binary);
            moved.Adopt();
            Assert.False(moved.Enabled);
            using (var run = Registry.CurrentUser.OpenSubKey(root + @"\Run"))
                Assert.Equal("\"D:\\new\\KyDrive.exe\"", run!.GetValue("KyDrive"));
            moved.Set(true);
            Assert.True(moved.Enabled);
            key.Adopt();
            Assert.True(key.Enabled);
            key.Set(false);
            key.Adopt();
            Assert.False(key.Enabled);
        }
        finally
        {
            Registry.CurrentUser.DeleteSubKeyTree(root, false);
        }
    }
}
