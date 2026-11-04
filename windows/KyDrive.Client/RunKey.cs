using System.Runtime.Versioning;
using Microsoft.Win32;

namespace KyDrive;

// Start at login for the zip build: a value under HKCU ...\Run that starts this KyDrive.exe.
// Task Manager's Startup switch is a value under Explorer\StartupApproved\Run whose first byte
// is odd when the user turned it off.
[SupportedOSPlatform("windows")]
public sealed class RunKey(string exe,
    string run = @"Software\Microsoft\Windows\CurrentVersion\Run",
    string approved = @"Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run")
{
    const string Name = "KyDrive";
    string Command => $"\"{exe}\"";

    public bool Enabled
    {
        get
        {
            using (var key = Registry.CurrentUser.OpenSubKey(run))
                if (!string.Equals(key?.GetValue(Name) as string, Command, StringComparison.OrdinalIgnoreCase))
                    return false;
            using var ok = Registry.CurrentUser.OpenSubKey(approved);
            return ok?.GetValue(Name) is not byte[] b || b.Length == 0 || b[0] % 2 == 0;
        }
    }

    public void Set(bool on)
    {
        using (var key = Registry.CurrentUser.CreateSubKey(run))
        {
            if (on)
                key.SetValue(Name, Command);
            else
                key.DeleteValue(Name, false);
        }
        // Turning it on here also clears Task Manager's "disabled".
        using var ok = Registry.CurrentUser.OpenSubKey(approved, true);
        ok?.DeleteValue(Name, false);
    }
}
