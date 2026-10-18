using System.Collections.Concurrent;
using System.Net.Sockets;
using System.Text;
using System.Text.Json.Nodes;

namespace KyDrive.Tests;

// A stand-in for kydrive-sync on a real AF_UNIX socket: answers each request line by method,
// holds Core.Link until Release, and fails Core.Choose.
sealed class FakeCore : IDisposable
{
    readonly Socket listener = new(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
    readonly string dir = System.IO.Path.Combine(System.IO.Path.GetTempPath(), "kyd-" + Guid.NewGuid().ToString("N")[..8]);
    readonly TaskCompletionSource release = new(TaskCreationOptions.RunContinuationsAsynchronously);

    public string Path { get; }
    public ConcurrentQueue<JsonObject> Requests { get; } = new();
    public JsonNode Status { get; set; } = JsonNode.Parse("""{"Linked":true,"Server":"https://drive.example.com","State":"idle","Workspaces":[{"ID":"w1","Name":"Docs","Dir":"Docs","HeldDeletes":3}]}""")!;

    public FakeCore()
    {
        Directory.CreateDirectory(dir);
        Path = System.IO.Path.Combine(dir, "sync.sock");
        listener.Bind(new UnixDomainSocketEndPoint(Path));
        listener.Listen();
        _ = Accept();
    }

    public void Release() => release.TrySetResult();

    async Task Accept()
    {
        while (true)
        {
            Socket s;
            try { s = await listener.AcceptAsync(); } catch { return; }
            _ = Serve(s);
        }
    }

    async Task Serve(Socket s)
    {
        using var stream = new NetworkStream(s, ownsSocket: true);
        using var reader = new StreamReader(stream, Encoding.UTF8);
        while (await reader.ReadLineAsync() is { } line)
        {
            var req = JsonNode.Parse(line)!.AsObject();
            Requests.Enqueue(req);
            JsonNode? result = null;
            string? error = null;
            switch ((string?)req["method"])
            {
                case "Core.Status": result = Status.DeepClone(); break;
                case "Core.Link": await release.Task; result = new JsonObject(); break;
                case "Core.Choose": error = "workspace not found"; break;
                case "Core.Conflicts": result = JsonNode.Parse("""[{"ID":1,"Workspace":"w1","Path":"a (conflict).txt","Kind":"conflict","Detail":"edited on both","Created":5}]"""); break;
                default: result = null; break;
            }
            var reply = new JsonObject { ["id"] = req["id"]?.DeepClone(), ["result"] = result, ["error"] = error };
            await stream.WriteAsync(Encoding.UTF8.GetBytes(reply.ToJsonString() + "\n"));
        }
    }

    public void Dispose()
    {
        listener.Dispose();
        try { Directory.Delete(dir, true); } catch (IOException) { }
    }
}
