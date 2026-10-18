using System.Xml.Linq;

namespace KyDrive.Tests;

public class SafetyTests
{
    [Theory]
    [InlineData("https://drive.example.com")]
    [InlineData("http://127.0.0.1:8080")]
    public void OpensWebUrls(string url) => Assert.NotNull(Safe.WebUri(url));

    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData("file:///C:/Windows/System32/calc.exe")]
    [InlineData(@"C:\Windows\System32\calc.exe")]
    [InlineData(@"\\attacker\share\x.exe")]
    [InlineData("javascript:alert(1)")]
    [InlineData("ms-settings:privacy")]
    [InlineData("search-ms:query=x")]
    [InlineData("drive.example.com")]
    public void RefusesEverythingElse(string? url) => Assert.Null(Safe.WebUri(url));

    // Core text in a toast stays text: no element, attribute or entity it carries takes effect.
    [Fact]
    public void ToastTextIsEscaped()
    {
        const string body = "</text></binding></visual><actions><action content=\"x\" arguments=\"y\" activationType=\"protocol\"/></actions><visual><binding template=\"ToastGeneric\"><text>&amp;&lt;";
        var doc = XDocument.Parse(Safe.ToastXml("KyDrive <b>", body));
        Assert.Equal("toast", doc.Root!.Name.LocalName);
        Assert.Empty(doc.Descendants("actions"));
        Assert.Equal(new[] { "KyDrive <b>", body }, doc.Descendants("text").Select(t => t.Value));
        Assert.Equal(new[] { "toast", "visual", "binding", "text", "text" }, doc.Descendants().Select(e => e.Name.LocalName));
    }

    // Server and core strings reach the screen only as TextBlock.Text (or other plain string
    // properties): nothing in the app may parse or render markup.
    [Fact]
    public void AppRendersPlainTextOnly()
    {
        var app = AppSources();
        var all = string.Join("\n", app.Select(File.ReadAllText));
        foreach (var banned in new[] { "RichTextBlock", "RichEditBox", "XamlReader", "Inlines", "Hyperlink", "WebView", "AppNotificationBuilder", "TextReadingOrder", "UseShellExecute = true" })
            Assert.DoesNotContain(banned, all);
        // The guard reads the right files: the app uses the safe helpers.
        Assert.Contains("Safe.ToastXml(", all);
        Assert.Contains("Safe.WebUri(", all);
        Assert.Contains("new AppNotification(", all);
    }

    static string[] AppSources()
    {
        for (var d = new DirectoryInfo(AppContext.BaseDirectory); d != null; d = d.Parent)
        {
            var app = Path.Combine(d.FullName, "KyDrive");
            if (File.Exists(Path.Combine(app, "KyDrive.csproj")))
                return Directory.GetFiles(app, "*.cs").Concat(Directory.GetFiles(app, "*.xaml")).ToArray();
        }
        throw new DirectoryNotFoundException("windows/KyDrive not found above " + AppContext.BaseDirectory);
    }
}
