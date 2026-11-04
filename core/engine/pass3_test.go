package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// Council pass 3 regressions.

// O-R1/S-R6: a snapshot that an unrelated new server folder forces does not undo local
// renames and deletes that the push has not sent yet.
func TestSnapshotKeepsLocalRenamesAndDeletes(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "F/x.txt": "x", "old.txt": "o"})
	if err := os.Rename(v.path("F"), v.path("G")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(v.path("a.txt"), v.path("b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(v.path("old.txt")); err != nil {
		t.Fatal(err)
	}
	v.r.Put(t, v.ws, "colleague/y.txt", []byte("y"))
	v.sync()
	v.sync()
	want := []string{"G/x.txt", "b.txt", "colleague/y.txt"}
	if got := tree(t, v.path("")); !slices.Equal(got, want) {
		t.Fatalf("local tree %v", got)
	}
	got := v.r.Paths(t, v.ws)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("server paths %v", got)
	}
}

// Refusing held deletes still restores what was deleted here.
func TestRefusedDeletesStillRestore(t *testing.T) {
	files := map[string]string{}
	for i := range 30 {
		files[filepath.ToSlash(filepath.Join("old", strings.Repeat("f", i+1)+".txt"))] = "x"
	}
	v := setup(t, files)
	os.RemoveAll(v.path("old"))
	v.sync()
	if err := v.e.ConfirmDeletes(v.ws, false); err != nil {
		t.Fatal(err)
	}
	v.sync()
	if n := len(tree(t, v.path(""))); n != 30 {
		t.Fatalf("%d files restored of 30", n)
	}
}

// O-R2/S-R7: losing access to a workspace with a file that cannot go does not stop the
// workspaces after it.
func TestStuckLostAccessDoesNotStopOthers(t *testing.T) {
	v := setup(t, map[string]string{"ro/a.txt": "1"})
	zed := second(t, v, "Zed") // synced after Docs
	readOnly(t, v.path("ro"))
	v.r.DropAccess(t, v.ws)
	v.r.Put(t, zed, "z.txt", []byte("z"))
	if err := v.e.Sync(context.Background()); err == nil {
		t.Fatal("a stuck evict passed as success")
	}
	if b, err := os.ReadFile(filepath.Join(v.root, "Zed", "z.txt")); err != nil || string(b) != "z" {
		t.Fatalf("the next workspace did not sync: %q %v", b, err)
	}
	os.Chmod(v.path("ro"), 0o755)
	v.sync()
	if v.exists("ro/a.txt") {
		t.Fatal("the evict was not retried")
	}
}

// O-R5/S-R9: a snapshot respects a failing download's backoff.
func TestSnapshotHonoursDownloadBackoff(t *testing.T) {
	f := newFake(t)
	f.corrupt = true
	v := setupOn(t, f, nil)
	v.e.Memory = NewMemory()
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	v.r.Put(t, v.ws, "big.bin", []byte("data"))
	big := "/" + v.r.ID(t, v.ws, "big.bin") + "/"
	var downloads atomic.Int32
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") && strings.Contains(r.URL.Path, big) {
			downloads.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	v.e.API.Base = front.URL
	v.sync()
	for _, d := range []string{"n1", "n2", "n3"} {
		v.r.Put(t, v.ws, d+"/x.txt", []byte(d)) // a new folder: a snapshot
		v.sync()
	}
	// Once in the feed, once as its first retry, then backed off.
	if n := downloads.Load(); n > 2 {
		t.Fatalf("big.bin downloaded %d times in 4 passes", n)
	}
}

// New in pass 3: a file moved from one chosen workspace to another never moves the
// destination's own file of the same name, or uploads it over the moved one.
func TestMoveBetweenWorkspacesLeavesSameNamedFile(t *testing.T) {
	f := newFake(t)
	v := setupOn(t, f, map[string]string{"notes.txt": "X"})
	archive := second(t, v, "Archive")
	v.r.Put(t, archive, "notes.txt", []byte("Y"))
	v.r.Put(t, archive, "sub/keep.txt", []byte("k"))
	v.sync()
	f.mu.Lock()
	it, sub := f.find(v.ws, "notes.txt"), f.find(archive, "sub")
	it.ws, it.parent = archive, sub.id
	f.record(v.ws, "file", it.id)
	f.record(archive, "file", it.id)
	f.mu.Unlock()
	v.sync()
	v.sync()
	if b, _ := os.ReadFile(filepath.Join(v.root, "Archive", "notes.txt")); string(b) != "Y" {
		t.Fatalf("Archive/notes.txt is %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(v.root, "Archive", "sub", "notes.txt")); string(b) != "X" {
		t.Fatalf("Archive/sub/notes.txt is %q", b)
	}
	if b, _ := v.r.Read(t, archive, "notes.txt"); string(b) != "Y" {
		t.Fatalf("server Archive/notes.txt is %q", b)
	}
	if b, _ := v.r.Read(t, archive, "sub/notes.txt"); string(b) != "X" {
		t.Fatalf("server Archive/sub/notes.txt is %q", b)
	}
}

// S-R4: a local symlink where the server has a folder is never taken for that folder, so the
// folder is not trashed on the server as deleted here.
func TestSymlinkInPlaceOfAFolderIsNotTrashed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	v := setup(t, nil)
	if err := os.Symlink(t.TempDir(), v.path("Shared")); err != nil {
		t.Fatal(err)
	}
	v.r.Put(t, v.ws, "Shared/report.txt", []byte("r"))
	v.e.Sync(context.Background())
	v.e.Sync(context.Background())
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"Shared/report.txt"}) {
		t.Fatalf("server paths %v", got)
	}
	os.Remove(v.path("Shared"))
	v.sync()
	if v.read("Shared/report.txt") != "r" {
		t.Fatal("the folder did not sync once freed")
	}
}

// S-R2/S-R10: a FIFO where the server has a file neither hangs the pass nor keeps the file
// away once it is gone.
func TestSpecialFileInPlaceOfAFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs on Windows")
	}
	v := setup(t, nil)
	if err := syscall.Mkfifo(v.path("channel"), 0o644); err != nil {
		t.Fatal(err)
	}
	v.r.Put(t, v.ws, "channel", []byte("data"))
	within(t, v.sync)
	os.Remove(v.path("channel"))
	v.sync()
	if v.read("channel") != "data" {
		t.Fatal("the file did not sync once freed")
	}
}

// S-R10: a server file blocked by a local folder syncs once the folder moves away.
func TestFileBlockedByAFolderSyncsOnceFreed(t *testing.T) {
	v := setup(t, map[string]string{"Reports": "r"})
	os.Remove(v.path("Reports"))
	v.sync() // the delete goes to the server
	v.r.Put(t, v.ws, "Reports", []byte("r2"))
	if err := os.Mkdir(v.path("Reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	v.e.Poll(context.Background())
	os.Remove(v.path("Reports"))
	v.e.Poll(context.Background())
	if v.read("Reports") != "r2" {
		t.Fatal("the file did not sync once freed")
	}
}
