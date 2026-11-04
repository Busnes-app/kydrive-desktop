//go:build !windows

package engine

import (
	"context"
	"os"
	"path/filepath"
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
