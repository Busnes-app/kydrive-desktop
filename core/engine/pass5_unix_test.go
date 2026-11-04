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

// Opus R3: a remote wipe removes the synced copies behind a linked workspace folder and a
// linked synced folder, and keeps an edit there.
func TestWipeReachesBehindLinks(t *testing.T) {
	f := newFake(t)
	v := setupOn(t, f, map[string]string{"secret.txt": "s", "S/plan.txt": "p", "T/edit.txt": "e"})
	moved := t.TempDir()
	for _, n := range []string{"S", "T"} {
		if err := os.Rename(v.path(n), filepath.Join(moved, n)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(moved, n), v.path(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(moved, "T", "edit.txt"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	away := filepath.Join(t.TempDir(), "Docs")
	if err := os.Rename(v.path(""), away); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(away, v.path("")); err != nil {
		t.Fatal(err)
	}
	f.Revoke(t, true)
	v.e.Sync(context.Background())
	if got := append(tree(t, away), tree(t, moved)...); !slices.Equal(got, []string{"T/edit.txt"}) {
		t.Fatalf("left on disk: %v", got)
	}
}

// Opus R8: an edit written in place that keeps the size and mtime (VeraCrypt's default for
// its containers) uploads.
func TestSameMetadataEditUploads(t *testing.T) {
	v := setup(t, map[string]string{"vault.bin": "AAAA"})
	fi, err := os.Stat(v.path("vault.bin"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(v.path("vault.bin"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte("BBBB"), 0)
	f.Close()
	os.Chtimes(v.path("vault.bin"), fi.ModTime(), fi.ModTime())
	v.sync()
	if v.remote("vault.bin") != "BBBB" {
		t.Fatalf("server has %q", v.remote("vault.bin"))
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

// Certification round 1: a link to another synced folder is not followed by an evict; the
// folder it reaches keeps its files, here and on the server.
func TestEvictDoesNotFollowLinkIntoSyncRoot(t *testing.T) {
	v := setup(t, map[string]string{"Old/a.txt": "same", "New/a.txt": "same", "keep.txt": "k"})
	if err := os.RemoveAll(v.path("Old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("New", v.path("Old")); err != nil {
		t.Fatal(err)
	}
	v.sync()
	v.r.Remove(t, v.ws, "Old")
	v.sync()
	v.sync()
	if !slices.Contains(v.r.Paths(t, v.ws), "New/a.txt") || !v.exists("New/a.txt") {
		t.Fatalf("New/a.txt lost: local %v server %v", tree(t, v.path("")), v.r.Paths(t, v.ws))
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
