//go:build !windows

package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Pass-4 C3-H1: a workspace folder moved to another disk with a symlink left in its place is
// not synced through the link, and none of its files are trashed on the server.
func TestLinkedWorkspaceFolderIsNotTrashed(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "Shared/x.txt": "x"})
	away := filepath.Join(t.TempDir(), "Docs")
	if err := os.Rename(v.path(""), away); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(away, v.path("")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := v.e.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "link") {
			t.Fatalf("sync error %v, want the link named", err)
		}
	}
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"Shared/x.txt", "a.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}

// Opus R2: a synced folder moved away with a link left, changed on the server meanwhile, then
// moved back, takes the server's versions without false conflicts.
func TestLinkedFolderMovedBackTakesServerChanges(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "Shared/x.txt": "x1", "Shared/y.txt": "y"})
	away := filepath.Join(t.TempDir(), "Shared")
	if err := os.Rename(v.path("Shared"), away); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(away, v.path("Shared")); err != nil {
		t.Fatal(err)
	}
	v.sync()
	v.r.Put(t, v.ws, "Shared/x.txt", []byte("x2-server"))
	v.r.Put(t, v.ws, "Shared/new.txt", []byte("n"))
	v.sync()
	if err := os.Remove(v.path("Shared")); err != nil {
		t.Fatal(err)
	}
	// A copy back, as from another disk: every file gets a new inode.
	if err := os.CopyFS(v.path("Shared"), os.DirFS(away)); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"x.txt", "y.txt"} {
		fi, _ := os.Stat(filepath.Join(away, n))
		os.Chtimes(v.path("Shared/"+n), fi.ModTime(), fi.ModTime())
	}
	for range 3 {
		v.sync()
	}
	noConflicts(t, v)
	if v.read("Shared/x.txt") != "x2-server" || v.read("Shared/new.txt") != "n" || v.read("Shared/y.txt") != "y" {
		t.Fatalf("local %v", tree(t, v.path("")))
	}
}

func linkAway(t *testing.T, v *env, rel string) string {
	t.Helper()
	away := filepath.Join(t.TempDir(), filepath.Base(rel))
	if err := os.Rename(v.path(rel), away); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(away, v.path(rel)); err != nil {
		t.Fatal(err)
	}
	return away
}

func moveBack(t *testing.T, v *env, rel, away string) {
	t.Helper()
	if err := os.Remove(v.path(rel)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(away, v.path(rel)); err != nil {
		t.Fatal(err)
	}
}

// Certification round 1: a snapshot while the folder is linked away (a new server folder
// forces one), and a new server folder under the link, still come right once it is back.
func TestLinkedFolderThroughSnapshot(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "Shared/x.txt": "x1", "Shared/y.txt": "y"})
	away := linkAway(t, v, "Shared")
	v.sync()
	v.r.Put(t, v.ws, "Shared/x.txt", []byte("x2-server"))
	v.r.Put(t, v.ws, "Shared/Sub/s.txt", []byte("s"))
	v.r.Put(t, v.ws, "Other/o.txt", []byte("o"))
	v.sync()
	moveBack(t, v, "Shared", away)
	for range 3 {
		v.sync()
	}
	noConflicts(t, v)
	if v.read("Shared/x.txt") != "x2-server" || v.read("Shared/Sub/s.txt") != "s" || v.read("Shared/y.txt") != "y" {
		t.Fatalf("local %v", tree(t, v.path("")))
	}
}

// Opus R9: a Finder alias left in place of a folder moved to another disk is not a delete.
func TestFinderAliasInPlaceOfFolder(t *testing.T) {
	aliases = true
	t.Cleanup(func() { aliases = runtime.GOOS == "darwin" })
	v := setup(t, map[string]string{"Photos/p.jpg": "p", "a.txt": "a"})
	linkAway(t, v, "Photos")
	if err := os.Remove(v.path("Photos")); err != nil {
		t.Fatal(err)
	}
	v.write("Photos", "book\x00\x00\x00\x00mark\x00\x00\x00\x00alias data")
	v.sync()
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"Photos/p.jpg", "a.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}

// Certification round 2: a folder with a Finder alias in its place that the server then
// renames keeps its files on the server.
func TestFinderAliasThenServerRename(t *testing.T) {
	aliases = true
	t.Cleanup(func() { aliases = runtime.GOOS == "darwin" })
	v := setup(t, map[string]string{"Photos/p.jpg": "p", "Photos/q.jpg": "q", "a.txt": "a", "b.txt": "b", "c.txt": "c"})
	linkAway(t, v, "Photos")
	if err := os.Remove(v.path("Photos")); err != nil {
		t.Fatal(err)
	}
	v.write("Photos", "book\x00\x00\x00\x00mark\x00\x00\x00\x00alias data")
	v.sync()
	v.r.Rename(t, v.ws, "Photos", "Pics")
	v.sync()
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"Pics/p.jpg", "Pics/q.jpg", "a.txt", "b.txt", "c.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}
