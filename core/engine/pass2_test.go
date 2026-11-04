package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// Council pass 2 regressions.

// O-R1: a file renamed here before the server's echo of its upload arrives stays renamed;
// the echo must not bring the old name back.
func TestRenameBeforeEchoStaysRenamed(t *testing.T) {
	v := setup(t, nil)
	v.write("New Text Document.txt", "notes")
	v.sync()
	if err := os.Rename(v.path("New Text Document.txt"), v.path("Plan.txt")); err != nil {
		t.Fatal(err)
	}
	v.sync()
	v.sync()
	if got := tree(t, v.path("")); !slices.Equal(got, []string{"Plan.txt"}) {
		t.Fatalf("local tree %v", got)
	}
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"Plan.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}

// O-R1: a folder renamed before the echo of its creation is not created again.
func TestFolderRenameBeforeEchoStaysRenamed(t *testing.T) {
	v := setup(t, nil)
	if err := os.Mkdir(v.path("New folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	v.sync()
	if err := os.Rename(v.path("New folder"), v.path("Projects")); err != nil {
		t.Fatal(err)
	}
	v.write("Projects/a.txt", "a")
	v.sync()
	v.sync()
	if v.exists("New folder") || !v.exists("Projects/a.txt") {
		t.Fatalf("local tree %v", tree(t, v.path("")))
	}
}

// O-R3: a server removal that cannot finish here (a file open elsewhere, no permission) does
// not stop the pass; it is retried.
func TestStuckRemovalDoesNotStopPull(t *testing.T) {
	v := setup(t, map[string]string{"ro/a.txt": "1", "b.txt": "1"})
	v.r.Remove(t, v.ws, "ro/a.txt")
	v.r.Put(t, v.ws, "b.txt", []byte("2"))
	readOnly(t, v.path("ro"))
	v.sync()
	if v.read("b.txt") != "2" {
		t.Fatal("a stuck removal stopped the other changes")
	}
	os.Chmod(v.path("ro"), 0o755)
	v.sync()
	if v.exists("ro/a.txt") {
		t.Fatal("the stuck removal was not retried")
	}
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"b.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}

// S-R5: a proxy's HTML 404 on a download is not KyDrive saying the file is gone.
func TestForeign404DownloadIsRetried(t *testing.T) {
	v := setup(t, nil)
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	var broken atomic.Bool
	broken.Store(true)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken.Load() && strings.HasSuffix(r.URL.Path, "/download") {
			http.Error(w, "<html>Not Found</html>", 404)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	v.e.API.Base = front.URL
	v.r.Put(t, v.ws, "new.txt", []byte("hello"))
	v.e.Sync(context.Background())
	broken.Store(false)
	v.sync()
	if !v.exists("new.txt") || v.read("new.txt") != "hello" {
		t.Fatal("a file whose download met a proxy 404 never arrived")
	}
}

// S-R4: one unreadable folder does not keep a wipe from removing its readable siblings.
func TestWipeContinuesPastAnUnreadableFolder(t *testing.T) {
	v := setup(t, map[string]string{"a/private/x.txt": "x", "z.txt": "z"})
	readOnly(t, v.path("a"))
	os.Chmod(v.path("a/private"), 0)
	t.Cleanup(func() { os.Chmod(v.path("a/private"), 0o755) })
	if err := v.e.Wipe(); err == nil {
		t.Fatal("the wipe reported success with a folder it could not read")
	}
	if v.exists("z.txt") {
		t.Fatal("an unreadable folder kept the wipe from its siblings")
	}
}

// S-R6: a server folder blocked by a local file syncs once the file is moved away.
func TestFolderBlockedByAFileSyncsOnceFreed(t *testing.T) {
	v := setup(t, nil)
	v.write("Reports", "a local file")
	v.r.Put(t, v.ws, "Reports/q1.txt", []byte("q1"))
	v.sync()
	if err := os.Rename(v.path("Reports"), v.path("Reports.txt")); err != nil {
		t.Fatal(err)
	}
	v.sync()
	v.sync()
	if !v.exists("Reports/q1.txt") || v.read("Reports/q1.txt") != "q1" {
		t.Fatalf("the freed folder never synced: %v", tree(t, v.path("")))
	}
}

// O-R7: OS junk is not kept in Unsynced as unuploaded work when a workspace is removed.
func TestEvictDropsOSJunk(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a"})
	v.write(".DS_Store", "junk")
	v.write("Thumbs.db", "junk")
	if err := v.e.Choose(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(v.root, Unsynced)); !os.IsNotExist(err) {
		t.Fatalf("junk kept in Unsynced: %v", tree(t, filepath.Join(v.root, Unsynced)))
	}
}

// O-R16: a workspace name with characters Windows refuses keeps the rest of its name.
func TestWorkspaceFolderKeepsItsName(t *testing.T) {
	for in, want := range map[string]string{"Sales: EMEA": "Sales_ EMEA", "a/b": "a_b", "CON": "Workspace", "": "Workspace"} {
		if got := folderName(in); got != want {
			t.Errorf("folderName(%q) = %q, want %q", in, got, want)
		}
	}
}

// second chooses another workspace beside Docs and returns its id.
func second(t *testing.T, v *env, name string) string {
	t.Helper()
	ws := v.r.NewWorkspace(t, name)
	if err := v.e.Choose(context.Background(), []string{v.ws, ws}); err != nil {
		t.Fatal(err)
	}
	v.sync()
	return ws
}

// O-R9: a download that keeps failing is not fetched again on every pass.
func TestFailingDownloadBacksOff(t *testing.T) {
	f := newFake(t)
	f.corrupt = true
	v := setupOn(t, f, nil)
	v.e.Memory = NewMemory()
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	var downloads atomic.Int32
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			downloads.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	v.e.API.Base = front.URL
	v.r.Put(t, v.ws, "big.bin", []byte("data"))
	for range 4 {
		v.sync()
	}
	if n := downloads.Load(); n > 2 {
		t.Fatalf("downloaded %d times in 4 passes", n)
	}
}

// O-R11: an upload that keeps failing is announced once.
func TestFailingUploadWarnsOnce(t *testing.T) {
	v := setup(t, nil)
	v.e.Memory = NewMemory()
	v.write("locked.txt", "x")
	readOnly(t, v.path("")) // so the cleanup restores the folder
	os.Chmod(v.path(""), 0o755)
	os.Chmod(v.path("locked.txt"), 0)
	t.Cleanup(func() { os.Chmod(v.path("locked.txt"), 0o644) })
	v.sync()
	v.sync()
	a, _ := v.e.DB.Activity(0)
	if n := len(slices.DeleteFunc(a, func(x Activity) bool { return x.Level != "warn" })); n != 1 {
		t.Fatalf("%d warnings for one failing upload", n)
	}
}

// O-R12: while deletes wait for confirmation, the workspace's other changes still sync.
func TestHeldDeletesKeepOtherChangesSyncing(t *testing.T) {
	files := map[string]string{"keep.txt": "v1"}
	for i := range 30 {
		files[filepath.ToSlash(filepath.Join("old", strings.Repeat("f", i+1)+".txt"))] = "x"
	}
	v := setup(t, files)
	os.RemoveAll(v.path("old"))
	v.sync()
	if ws, _ := v.e.DB.Workspaces(); ws[0].Deletes == 0 {
		t.Fatal("the guard did not hold")
	}
	v.write("keep.txt", "v2")
	v.r.Put(t, v.ws, "new.txt", []byte("new"))
	v.sync()
	if v.remote("keep.txt") != "v2" || !v.exists("new.txt") {
		t.Fatal("a held workspace stopped syncing its other changes")
	}
	if len(v.r.Paths(t, v.ws)) != 32 {
		t.Fatalf("held deletes went through: %d server files", len(v.r.Paths(t, v.ws)))
	}
}

// O-R13: an unreadable folder in one workspace does not stop the others.
func TestUnreadableFolderStopsOnlyItsWorkspace(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a"})
	archive := second(t, v, "Archive") // synced before Docs
	private := filepath.Join(v.root, "Archive", "private")
	if err := os.Mkdir(private, 0o755); err != nil {
		t.Fatal(err)
	}
	readOnly(t, private)
	os.Chmod(private, 0)
	v.write("new.txt", "n")
	if err := v.e.Sync(context.Background()); err == nil {
		t.Fatal("an unreadable folder passed as success")
	}
	if v.remote("new.txt") != "n" {
		t.Fatal("the other workspace did not sync")
	}
	if got := v.r.Paths(t, archive); len(got) != 0 {
		t.Fatalf("Archive on the server %v", got)
	}
}

// Certification round 1: a file the server cannot serve (503) waits; it never stops the rest.
func TestUnservableFileDoesNotStopSync(t *testing.T) {
	v := setup(t, map[string]string{"keep.txt": "v1"})
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			http.Error(w, `{"error":"Document bytes unavailable"}`, 503)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	v.e.API.Base = front.URL
	v.r.Put(t, v.ws, "bad.bin", []byte("x"))
	v.write("keep.txt", "v2")
	v.sync()
	if v.remote("keep.txt") != "v2" {
		t.Fatal("an unservable file stopped the upload")
	}
}

// Certification round 1: an unreadable folder in a reader workspace does not stop the others.
func TestUnreadableReaderFolderStopsOnlyItsWorkspace(t *testing.T) {
	f := newFake(t)
	v := setupOn(t, f, map[string]string{"a.txt": "a"})
	second(t, v, "Archive") // synced before Docs
	private := filepath.Join(v.root, "Archive", "private")
	if err := os.Mkdir(private, 0o755); err != nil {
		t.Fatal(err)
	}
	readOnly(t, private)
	os.Chmod(private, 0)
	f.mu.Lock()
	f.reader = true
	f.mu.Unlock()
	v.r.Put(t, v.ws, "new.txt", []byte("n"))
	v.e.Sync(context.Background())
	if !v.exists("new.txt") {
		t.Fatal("a reader workspace's unreadable folder stopped the others")
	}
}

// Certification round 1: held deletes stay held when the server grows past the threshold.
func TestHeldDeletesStayHeldWhenTheServerGrows(t *testing.T) {
	files := map[string]string{}
	for i := range 40 {
		files[strings.Repeat("f", i+1)+".txt"] = "x"
	}
	v := setup(t, files)
	for i := range 30 {
		os.Remove(v.path(strings.Repeat("f", i+1) + ".txt"))
	}
	v.sync()
	for i := range 40 {
		v.r.Put(t, v.ws, strings.Repeat("g", i+1)+".txt", []byte("n")) // no new folder: no snapshot
	}
	v.sync()
	if n := len(v.r.Paths(t, v.ws)); n != 80 {
		t.Fatalf("held deletes went through without confirmation: %d server files", n)
	}
}

// Certification rounds 1-2: a file whose body is always cut mid-transfer never holds up the
// rest.
func TestBodyCutFileDoesNotStopSync(t *testing.T) {
	v := setup(t, map[string]string{"keep.txt": "v1"})
	v.e.Memory = NewMemory()
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			w.Header().Set("Content-Length", "10")
			w.Write([]byte("01234"))
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	v.e.API.Base = front.URL
	v.r.Put(t, v.ws, "a.bin", []byte("0123456789"))
	v.r.Put(t, v.ws, "b.bin", []byte("0123456789"))
	v.write("keep.txt", "v2")
	v.sync()
	v.sync()
	if v.remote("keep.txt") != "v2" {
		t.Fatal("a cut download held up the upload")
	}
}
