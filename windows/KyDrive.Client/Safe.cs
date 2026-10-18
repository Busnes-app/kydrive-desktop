using System.Xml.Linq;

namespace KyDrive;

public static class Paths
{
    // The core's socket: filepath.Join(os.UserCacheDir(), "kydrive", "sync.sock"), where Go's
    // UserCacheDir on Windows is the %LocalAppData% variable, not the known-folder API. Never a
    // relative path, which would resolve against the working directory.
    public static string SocketPath(Func<string, string?>? env = null)
    {
        var local = (env ?? Environment.GetEnvironmentVariable)("LOCALAPPDATA");
        if (string.IsNullOrEmpty(local) || !Path.IsPathFullyQualified(local))
            throw new InvalidOperationException("%LOCALAPPDATA% is not an absolute path");
        return Path.GetFullPath(Path.Join(local, "kydrive", "sync.sock"));
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
