namespace KyDrive.Tests;

public class CoreClientTests
{
    [Fact]
    public async Task OlderCoreWithoutAccountsStillReachesUpgrade()
    {
        using var core = new FakeCore { AccountsError = "rpc: can't find method Core.Accounts" };
        core.Status["Version"] = "0.6.1";
        var client = new CoreClient(core.Path);
        // This is the refresh path used by MainWindow before its version handling.
        var accounts = await client.Accounts();
        client = client.ForAccounts(accounts);
        var status = await client.Status();
        Assert.Empty(accounts);
        Assert.True(Upgrade.Older(status.Version, new Version(0, 7, 0)));
        await client.Quit();
        Assert.Contains(core.Requests, r => (string?)r["method"] == "Core.Quit");
    }

    [Fact]
    public async Task AccountsDoesNotHideOtherRpcErrors()
    {
        using var core = new FakeCore { AccountsError = "account state does not match its identity" };
        var error = await Assert.ThrowsAsync<CoreException>(() => new CoreClient(core.Path).Accounts());
        Assert.Equal(core.AccountsError, error.Message);
    }

    [Fact]
    public async Task RemovingLastAccountReturnsLinkToPrimaryWithoutRestart()
    {
        using var core = new FakeCore();
        core.Accounts.Add(new System.Text.Json.Nodes.JsonObject { ["ID"] = "one", ["Server"] = "https://drive.example.com", ["User"] = "person", ["OrgID"] = "one" });
        var client = new CoreClient(core.Path).ForAccount("one");
        await client.Unlink(false);
        client = client.ForAccounts(await client.Accounts());
        Assert.Equal("", client.AccountID);
        core.Release();
        await client.Link("https://drive.example.com", "pc");
        Assert.Contains(core.Requests, r => (string?)r["method"] == "Core.Link" && (string?)r["params"]![0]!["AccountID"] == "");
    }

    [Fact]
    public async Task RemovingOneAccountKeepsAnotherSelected()
    {
        using var core = new FakeCore();
        foreach (var id in new[] { "one", "two" })
            core.Accounts.Add(new System.Text.Json.Nodes.JsonObject { ["ID"] = id, ["Server"] = "https://drive.example.com", ["User"] = "person", ["OrgID"] = id });
        var selected = new CoreClient(core.Path).ForAccount("two");
        await selected.ForAccount("one").Unlink(false);
        var accounts = await selected.Accounts();
        Assert.Equal("two", Assert.Single(accounts).ID);
        Assert.Same(selected, selected.ForAccounts(accounts));
        Assert.Equal("two", selected.AccountID);
    }

    [Theory]
    [InlineData("legacy", false)]
    [InlineData("one", false)]
    [InlineData("legacy", true)]
    [InlineData("one", true)]
    public async Task RevokedLinkUsesSelectionAndExplicitAddIntent(string id, bool adding)
    {
        using var core = new FakeCore();
        core.Status["Linked"] = false; core.Status["State"] = "revoked";
        core.Accounts.Add(new System.Text.Json.Nodes.JsonObject { ["ID"] = id, ["Server"] = "https://drive.example.com", ["User"] = "person", ["OrgID"] = id });
        core.Release();
        var client = new CoreClient(core.Path).ForAccount(id);
        var selected = await client.SubmitLink("https://drive.example.com", "computer", adding);
        Assert.Equal(adding ? "new" : id, selected.AccountID);
        var request = Assert.Single(core.Requests);
        Assert.Equal(adding ? "Core.AddAccount" : "Core.Link", (string?)request["method"]);
        if (adding) Assert.Null(request["params"]![0]!["AccountID"]);
        else Assert.Equal(id, (string?)request["params"]![0]!["AccountID"]);
    }

    // Q8: the process answering on the socket is known, as SO_PEERCRED gives on Linux.
    [Fact]
    public async Task PeerProcessIsTheAnsweringOne()
    {
        if (!OperatingSystem.IsWindows()) return;
        using var core = new FakeCore();
        Assert.Equal(Environment.ProcessId, await new CoreClient(core.Path).PeerProcessId());
    }

    [Fact]
    public async Task StatusOverUnixSocket()
    {
        using var core = new FakeCore();
        var s = await new CoreClient(core.Path).Status();
        Assert.True(s.Linked);
        Assert.Equal("idle", s.State);
        Assert.Equal(3, Assert.Single(s.Workspaces!).HeldDeletes);
    }

    [Fact]
    public async Task RequestsKeepTheirAccount()
    {
        using var core = new FakeCore();
        var client = new CoreClient(core.Path);
        await client.ForAccount("one").Pause(true);
        await client.ForAccount("two").Pause(false);
        Assert.Equal(new[] {"one", "two"},core.Requests.Select(r => (string?)r["params"]![0]!["AccountID"]));
    }

    [Fact]
    public async Task RequestIsOneJsonRpc10Line()
    {
        using var core = new FakeCore();
        await new CoreClient(core.Path).Pause(true);
        Assert.True(core.Requests.TryDequeue(out var req));
        Assert.Equal("Core.Pause", (string?)req["method"]);
        Assert.True((bool)req["params"]![0]!["Paused"]!);
        Assert.Single(req["params"]!.AsArray());
        Assert.NotNull(req["id"]);
    }

    [Fact]
    public async Task HeldLinkDoesNotBlockOtherCalls()
    {
        using var core = new FakeCore();
        var client = new CoreClient(core.Path);
        var link = client.Link("https://drive.example.com", "pc");
        var status = await client.Status().WaitAsync(TimeSpan.FromSeconds(10));
        Assert.True(status.Linked);
        Assert.False(link.IsCompleted);
        core.Release();
        await link.WaitAsync(TimeSpan.FromSeconds(10));
    }

    [Fact]
    public async Task CoreErrorIsReported()
    {
        using var core = new FakeCore();
        var e = await Assert.ThrowsAsync<CoreException>(() => new CoreClient(core.Path).Choose(["w1"]));
        Assert.Equal("workspace not found", e.Message);
        Assert.IsNotType<CoreNotRunningException>(e);
    }

    [Fact]
    public async Task ConflictsParse()
    {
        using var core = new FakeCore();
        var c = Assert.Single(await new CoreClient(core.Path).Conflicts());
        Assert.Equal("a (conflict).txt", c.Path);
        Assert.Equal("edited on both", c.Detail);
    }

    [Fact]
    public async Task MissingCoreIsNotRunning()
    {
        var path = Path.Combine(Path.GetTempPath(), "kyd-none-" + Guid.NewGuid().ToString("N")[..8], "sync.sock");
        await Assert.ThrowsAsync<CoreNotRunningException>(() => new CoreClient(path).Status());
    }

    // The first read only sets the baseline; later warnings are announced once, oldest first.
    [Fact]
    public void WarnsOnNewActivityOnly()
    {
        var w = new Warnings();
        Assert.Empty(w.Next([new Activity(7, 0, "warn", "old")]));
        Assert.Equal(new[] { "eight", "nine" }, w.Next([new Activity(9, 0, "warn", "nine"), new Activity(8, 0, "warn", "eight"), new Activity(7, 0, "warn", "old")]));
        Assert.Empty(w.Next([new Activity(10, 0, "info", "synced")]));
        Assert.Equal(10, w.After);
    }
}
