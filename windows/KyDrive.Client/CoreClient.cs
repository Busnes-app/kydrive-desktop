using System.Net.Sockets;
using System.Text;
using System.Text.Json;

namespace KyDrive;

// Wire shapes of core/cmd/kydrive-sync/rpc.go and core/engine/db.go.
public sealed record Status(bool Linked, string? Server, string? DevicesURL, string? Root, string? State, string? Error,
    bool Paused, long LastSync, WorkspaceStatus[]? Workspaces);

public sealed record WorkspaceStatus(string ID, string Name, string Dir, int HeldDeletes);

public sealed record Choice(string ID, string Name, string Kind, string Role, bool Chosen);

public sealed record Issue(long ID, string Workspace, string Path, string Kind, string Detail, long Created);

public sealed record Activity(long ID, long Created, string Level, string Message);

// The core answered with an error.
public class CoreException(string message) : Exception(message);

// Nothing answers on the socket.
public sealed class CoreNotRunningException(string message) : CoreException(message);

// The shell's only link to kydrive-sync: JSON-RPC 1.0 ("Core.<Method>", one JSON object per
// line) over the core's AF_UNIX socket, one connection per call so a held Link never blocks
// the others. No sync logic: it returns what the core reports.
public sealed class CoreClient(string socketPath)
{
    public Task<Status> Status(CancellationToken ct = default) => Call<Status>("Status", null, ct);
    public async Task<Choice[]> Workspaces(CancellationToken ct = default) => await Call<Choice[]?>("Workspaces", null, ct) ?? [];
    public async Task<Issue[]> Conflicts(CancellationToken ct = default) => await Call<Issue[]?>("Conflicts", null, ct) ?? [];
    public async Task<Activity[]> Activity(long after, CancellationToken ct = default) => await Call<Activity[]?>("Activity", new { After = after }, ct) ?? [];
    // Returns once the user approves in the browser, or after the core's ten-minute timeout.
    public Task Link(string server, string name, CancellationToken ct = default) => Call<object>("Link", new { Server = server, Name = name }, ct);
    public Task Unlink(bool removeFiles, CancellationToken ct = default) => Call<object>("Unlink", new { RemoveFiles = removeFiles }, ct);
    public Task Choose(IEnumerable<string> ids, CancellationToken ct = default) => Call<object>("Choose", new { IDs = ids.ToArray() }, ct);
    public Task Pause(bool paused, CancellationToken ct = default) => Call<object>("Pause", new { Paused = paused }, ct);
    public Task ConfirmDeletes(string workspace, bool allow, CancellationToken ct = default) => Call<object>("ConfirmDeletes", new { Workspace = workspace, Allow = allow }, ct);

    async Task<T> Call<T>(string method, object? args, CancellationToken ct)
    {
        using var socket = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
        try
        {
            await socket.ConnectAsync(new UnixDomainSocketEndPoint(socketPath), ct);
        }
        catch (SocketException e)
        {
            throw new CoreNotRunningException("KyDrive sync is not running: " + e.Message);
        }
        try
        {
            await using var stream = new NetworkStream(socket);
            var request = JsonSerializer.SerializeToUtf8Bytes(new { method = "Core." + method, @params = new[] { args ?? new object() }, id = 1 });
            await stream.WriteAsync(request.Append((byte)'\n').ToArray(), ct);
            using var reader = new StreamReader(stream, Encoding.UTF8);
            using var reply = JsonDocument.Parse(await reader.ReadLineAsync(ct) ?? throw new IOException("connection closed"));
            if (reply.RootElement.TryGetProperty("error", out var error) && error.ValueKind != JsonValueKind.Null)
                throw new CoreException(error.ValueKind == JsonValueKind.String ? error.GetString()! : error.GetRawText());
            return reply.RootElement.GetProperty("result").Deserialize<T>()!;
        }
        catch (Exception e) when (e is IOException or JsonException or KeyNotFoundException)
        {
            throw new CoreException("KyDrive sync stopped: " + e.Message);
        }
    }
}

// Picks the core's new warnings for notifications. The first read only sets the baseline.
public sealed class Warnings
{
    bool started;

    // The highest activity ID seen; pass it as Activity's After.
    public long After { get; private set; }

    public IReadOnlyList<string> Next(IEnumerable<Activity> newestFirst)
    {
        var fresh = newestFirst.Where(a => a.ID > After).OrderBy(a => a.ID).ToList();
        if (fresh.Count > 0)
            After = fresh[^1].ID;
        var announce = started ? fresh.Where(a => a.Level == "warn").Select(a => a.Message).ToList() : [];
        started = true;
        return announce;
    }
}
