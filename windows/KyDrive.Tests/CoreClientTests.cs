namespace KyDrive.Tests;

public class CoreClientTests
{
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
