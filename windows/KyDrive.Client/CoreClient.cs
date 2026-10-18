namespace KyDrive;

public sealed record Status(bool Linked, string? Server, string? DevicesURL, string? Root, string? State, string? Error,
    bool Paused, long LastSync, WorkspaceStatus[]? Workspaces);

public sealed record WorkspaceStatus(string ID, string Name, string Dir, int HeldDeletes);

public sealed record Choice(string ID, string Name, string Kind, string Role, bool Chosen);

public sealed record Issue(long ID, string Workspace, string Path, string Kind, string Detail, long Created);

public sealed record Activity(long ID, long Created, string Level, string Message);

public class CoreException(string message) : Exception(message);

public sealed class CoreNotRunningException(string message) : CoreException(message);

public sealed class CoreClient(string socketPath)
{
    public Task<Status> Status(CancellationToken ct = default) => throw new NotImplementedException();
    public Task<Choice[]> Workspaces(CancellationToken ct = default) => throw new NotImplementedException();
    public Task<Issue[]> Conflicts(CancellationToken ct = default) => throw new NotImplementedException();
    public Task<Activity[]> Activity(long after, CancellationToken ct = default) => throw new NotImplementedException();
    public Task Link(string server, string name, CancellationToken ct = default) => throw new NotImplementedException();
    public Task Unlink(bool removeFiles, CancellationToken ct = default) => throw new NotImplementedException();
    public Task Choose(IEnumerable<string> ids, CancellationToken ct = default) => throw new NotImplementedException();
    public Task Pause(bool paused, CancellationToken ct = default) => throw new NotImplementedException();
    public Task ConfirmDeletes(string workspace, bool allow, CancellationToken ct = default) => throw new NotImplementedException();
}

public sealed class Warnings
{
    public long After { get; }
    public IReadOnlyList<string> Next(IEnumerable<Activity> newestFirst) => throw new NotImplementedException();
}

public static class Paths
{
    public static string SocketPath(Func<string, string?>? env = null) => throw new NotImplementedException();
}

public static class Safe
{
    public static Uri? WebUri(string? s) => throw new NotImplementedException();
    public static string ToastXml(string title, string body) => throw new NotImplementedException();
}
