using System.Xml.Linq;

namespace KyDrive;

public static class Paths
{
    // The core's socket: filepath.Join(os.UserConfigDir(), "kydrive", "sync.sock"), where Go's
    // UserConfigDir on Windows is the %AppData% variable, not the known-folder API. Never a
    // relative path, which would resolve against the working directory.
    public static string SocketPath(Func<string, string?>? env = null)
    {
        var appData = (env ?? Environment.GetEnvironmentVariable)("APPDATA");
        if (string.IsNullOrEmpty(appData) || !Path.IsPathFullyQualified(appData))
            throw new InvalidOperationException("%APPDATA% is not an absolute path");
        return Path.GetFullPath(Path.Join(appData, "kydrive", "sync.sock"));
    }
}

public static class Safe
{
    // Only web addresses reach the launcher: no files, shares or other protocol handlers.
    public static Uri? WebUri(string? s) =>
        Uri.TryCreate(s, UriKind.Absolute, out var u) && (u.Scheme == Uri.UriSchemeHttps || u.Scheme == Uri.UriSchemeHttp) ? u : null;

    // Toast XML built as a tree, so title and body are always text nodes.
    public static string ToastXml(string title, string body) =>
        new XElement("toast",
            new XElement("visual",
                new XElement("binding", new XAttribute("template", "ToastGeneric"),
                    new XElement("text", title),
                    new XElement("text", body)))).ToString(SaveOptions.DisableFormatting);
}
