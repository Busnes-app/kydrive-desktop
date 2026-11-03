package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// readOnly makes dir unwritable for the test, or skips where permission bits do not bind.
func readOnly(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permission bits do not stop this user")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
}

// The feed returns this computer's own upload. A second save made before the next pull is a
// pending upload, not a conflict.
func TestSecondSaveAfterUploadIsNotAConflict(t *testing.T) {
	v := setup(t, map[string]string{"doc.txt": "v1"})
	v.write("doc.txt", "first save")
	v.sync()
	v.write("doc.txt", "second, longer save")
	v.sync()
	if got := tree(t, v.path("")); !slices.Equal(got, []string{"doc.txt"}) {
		t.Fatalf("local tree %v", got)
	}
	if v.read("doc.txt") != "second, longer save" || v.remote("doc.txt") != "second, longer save" {
		t.Fatalf("local %q, server %q", v.read("doc.txt"), v.remote("doc.txt"))
	}
}

// One file the pull cannot write must not stop the rest of the pass; it is retried.
func TestUnwritableFileDoesNotStopPull(t *testing.T) {
	v := setup(t, map[string]string{"ro/a.txt": "1", "b.txt": "1"})
	v.r.Put(t, v.ws, "ro/a.txt", []byte("2"))
	v.r.Put(t, v.ws, "b.txt", []byte("2"))
	readOnly(t, v.path("ro"))
	v.sync()
	if v.read("b.txt") != "2" {
		t.Fatal("a locked file stopped the other downloads")
	}
	os.Chmod(v.path("ro"), 0o755)
	v.sync()
	if v.read("ro/a.txt") != "2" {
		t.Fatal("the failed download was not retried")
	}
}

// A wipe removes what it can past one stuck file, and an unfinished wipe resumes later.
func TestWipeResumesAfterAStuckFile(t *testing.T) {
	v := setup(t, map[string]string{"a/x.txt": "x", "z.txt": "z"})
	readOnly(t, v.path("a"))
	v.r.Revoke(t, true)
	if err := v.e.Sync(context.Background()); !errors.Is(err, ErrRevoked) {
		t.Fatalf("got %v, want ErrRevoked", err)
	}
	if v.exists("z.txt") {
		t.Fatal("the wipe stopped at the first file it could not remove")
	}
	os.Chmod(v.path("a"), 0o755)
	if err := v.e.ResumeWipe(); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, v.root); len(got) != 0 {
		t.Fatalf("left after the resumed wipe: %v", got)
	}
	if err := v.e.ResumeWipe(); err != nil || v.e.DB.Get(wipeKey) != "" {
		t.Fatalf("wipe still pending: %v", err)
	}
}

// A workspace folder deleted locally waits for confirmation even when the server changed a
// file meanwhile; the pull must not recreate the folder and let the push trash the rest.
func TestDeletedWorkspaceFolderWaitsDespiteRemoteChange(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c"})
	os.RemoveAll(v.path(""))
	v.r.Put(t, v.ws, "a.txt", []byte("a2"))
	v.sync()
	if got := v.r.Paths(t, v.ws); len(got) != 3 {
		t.Fatalf("server paths %v; the deletes did not wait", got)
	}
	ws, _ := v.e.DB.Workspaces()
	if ws[0].Deletes != 3 {
		t.Fatalf("held deletes %d, want 3", ws[0].Deletes)
	}
	if err := v.e.ConfirmDeletes(v.ws, false); err != nil {
		t.Fatal(err)
	}
	v.sync()
	if v.read("a.txt") != "a2" || v.read("c.txt") != "c" {
		t.Fatal("refusing did not restore the files")
	}
}

// A new file stamped in the future (an archive from another time zone) uploads.
func TestFutureMtimeUploads(t *testing.T) {
	v := setup(t, nil)
	v.e.Settle = 3 * time.Second
	v.write("zip.txt", "from an archive")
	later := time.Now().Add(2 * time.Hour)
	os.Chtimes(v.path("zip.txt"), later, later)
	v.sync()
	if v.remote("zip.txt") != "from an archive" || v.e.Unsettled {
		t.Fatalf("not uploaded; unsettled %v", v.e.Unsettled)
	}
}

// A 404 that is not KyDrive's (a proxy's page) is a failure, not lost access.
func TestForeign404IsNotLostAccess(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a"})
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/changes") {
			http.Error(w, "<html>Not Found</html>", 404)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	v.e.API.Base = front.URL
	v.r.Put(t, v.ws, "b.txt", []byte("b"))
	if err := v.e.Sync(context.Background()); err == nil {
		t.Fatal("a proxy 404 passed as success")
	}
	if ws, _ := v.e.DB.Workspaces(); len(ws) != 1 || !v.exists("a.txt") {
		t.Fatal("a proxy 404 evicted the workspace")
	}
}

// A download that keeps failing is announced once, not on every poll.
func TestFailedDownloadWarnsOnce(t *testing.T) {
	f := newFake(t)
	f.corrupt = true
	v := setupOn(t, f, nil)
	v.r.Put(t, v.ws, "x.txt", []byte("hello"))
	v.e.Sync(context.Background())
	v.e.Sync(context.Background())
	a, _ := v.e.DB.Activity(0)
	n := 0
	for _, x := range a {
		if strings.Contains(x.Message, "x.txt") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d warnings about x.txt, want 1", n)
	}
}

// Confirmed deletes that came back before the pass do not leave the guard switched off.
func TestConfirmedDeletesResetWhenNothingIsDeleted(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a"})
	ws, _ := v.e.DB.Workspaces()
	ws[0].Deletes, ws[0].AllowDeletes = 25, true
	v.e.DB.PutWorkspace(ws[0])
	v.sync()
	ws, _ = v.e.DB.Workspaces()
	if ws[0].Deletes != 0 || ws[0].AllowDeletes {
		t.Fatalf("guard left at %+v", ws[0])
	}
}

func TestWindowsReservesSuperscriptAndConsoleNames(t *testing.T) {
	for _, n := range []string{"COM¹.txt", "lpt³", "CONIN$", "conout$.log"} {
		if windowsName(n) {
			t.Errorf("windowsName(%q) = true", n)
		}
	}
}

// A device name Windows cannot hold in a file name still yields a valid conflict copy.
func TestConflictNameFromAnyDeviceName(t *testing.T) {
	v := setup(t, map[string]string{"doc.txt": "v1"})
	v.e.Device = `Office?Laptop: "x"`
	v.r.Put(t, v.ws, "doc.txt", []byte("theirs"))
	v.write("doc.txt", "mine")
	v.sync()
	got := tree(t, v.path(""))
	if len(got) != 2 {
		t.Fatalf("local tree %v", got)
	}
	for _, n := range got {
		if !windowsName(n) {
			t.Errorf("conflict copy %q is not a valid Windows name", n)
		}
	}
}

// A numbered folder for a long workspace name still fits the file system.
func TestLongWorkspaceNameGetsAFittingFolder(t *testing.T) {
	f := newFake(t)
	v := setupOn(t, f, nil)
	long := strings.Repeat("a", 255)
	if err := os.Mkdir(filepath.Join(v.root, long), 0o755); err != nil {
		t.Skip("the file system refuses 255-byte names:", err)
	}
	id := f.NewWorkspace(t, long)
	if err := v.e.Choose(context.Background(), []string{v.ws, id}); err != nil {
		t.Fatal(err)
	}
	ws, _ := v.e.DB.Workspaces()
	for _, w := range ws {
		if len(w.Dir) > maxName {
			t.Fatalf("folder name is %d bytes", len(w.Dir))
		}
	}
	v.sync()
}

// A server rename blocked by a local file is retried once the way is clear.
func TestBlockedRemoteRenameRetries(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "1"})
	if err := os.WriteFile(v.path("b.txt"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	v.r.Rename(t, v.ws, "a.txt", "b.txt")
	if err := v.e.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	os.Remove(v.path("b.txt"))
	if err := v.e.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v.exists("a.txt") || v.read("b.txt") != "1" {
		t.Fatalf("local tree %v", tree(t, v.path("")))
	}
}

// An unchoose that cannot remove one file keeps the workspace, but the files it did remove
// must not read as local deletes on the next pass.
func TestPartialEvictDoesNotTrash(t *testing.T) {
	v := setup(t, map[string]string{"a/x.txt": "x", "z.txt": "z"})
	readOnly(t, v.path("a"))
	if err := v.e.Choose(context.Background(), nil); err == nil {
		t.Fatal("unchoose reported success with a file left behind")
	}
	if v.exists("z.txt") {
		t.Fatal("the evict stopped at the first file it could not remove")
	}
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"a/x.txt", "z.txt"}) {
		t.Fatalf("server paths %v; an evicted copy was trashed", got)
	}
	if v.read("z.txt") != "z" {
		t.Fatal("the still-chosen workspace did not get its removed file back")
	}
}

// Confirmed deletes of a missing workspace folder pull first, so a file another device just
// added to a folder about to be trashed survives.
func TestConfirmedFolderDeleteKeepsUnseenRemoteFile(t *testing.T) {
	v := setup(t, map[string]string{"a/old.txt": "old", "b.txt": "b"})
	os.RemoveAll(v.path(""))
	v.sync() // held
	v.r.Put(t, v.ws, "a/new.txt", []byte("new"))
	if err := v.e.ConfirmDeletes(v.ws, true); err != nil {
		t.Fatal(err)
	}
	v.sync()
	if b, ok := v.r.Read(t, v.ws, "a/new.txt"); !ok || string(b) != "new" {
		t.Fatalf("a file the user never saw was trashed: %q, %v", b, ok)
	}
	if _, ok := v.r.Read(t, v.ws, "b.txt"); ok {
		t.Fatal("the confirmed delete did not happen")
	}
}

// A server folder rename that cannot be applied locally must not stop the pass either.
func TestBlockedFolderRenameDoesNotStopPull(t *testing.T) {
	v := setup(t, map[string]string{"ro/sub/a.txt": "a", "z.txt": "1"})
	v.r.Rename(t, v.ws, "ro/sub", "sub2")
	v.r.Put(t, v.ws, "z.txt", []byte("2"))
	readOnly(t, v.path("ro"))
	v.sync()
	if v.read("z.txt") != "2" {
		t.Fatal("a folder that could not move stopped the pass")
	}
	os.Chmod(v.path("ro"), 0o755)
	if err := v.e.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v.exists("ro/sub") || v.read("ro/sub2/a.txt") != "a" {
		t.Fatalf("the refused folder move was not retried: %v", tree(t, v.path("")))
	}
}

// A folder move waiting in retries is dropped once its folder is gone, never replayed as a file.
func TestFolderRetryEndsWithItsFolder(t *testing.T) {
	v := setup(t, map[string]string{"sub/a.txt": "a"})
	if err := os.Mkdir(v.path("sub2"), 0o755); err != nil {
		t.Fatal(err)
	}
	v.r.Rename(t, v.ws, "sub", "sub2")
	if err := v.e.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(v.path("sub"))
	v.sync()
	v.sync()
	issues, _ := v.e.DB.Issues()
	for _, i := range issues {
		if i.Kind == "skipped" {
			t.Errorf("spurious issue %+v", i)
		}
	}
	if r, _ := v.e.DB.Retries(v.ws); len(r) != 0 {
		t.Fatalf("retries left: %+v", r)
	}
}
