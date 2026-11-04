package engine

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// Council pass 4 regressions.

// noConflicts fails if any local file or server path is a conflict copy.
func noConflicts(t *testing.T, v *env) {
	t.Helper()
	for _, p := range append(tree(t, v.path("")), v.r.Paths(t, v.ws)...) {
		if strings.Contains(p, "(conflict") {
			t.Fatalf("false conflict: local %v, server %v", tree(t, v.path("")), v.r.Paths(t, v.ws))
		}
	}
}

// O-R1: a server rename that frees a name a new file then takes, seen through a snapshot
// (any new server folder forces one), is not a conflict.
func TestSnapshotRenameThenReuseIsNoConflict(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "old"})
	v.r.Rename(t, v.ws, "a.txt", "b.txt")
	v.r.Put(t, v.ws, "a.txt", []byte("new"))
	v.r.Put(t, v.ws, "n/x.txt", []byte("x")) // a new folder: a snapshot
	v.sync()
	v.sync()
	noConflicts(t, v)
	if v.read("a.txt") != "new" || v.read("b.txt") != "old" {
		t.Fatalf("a.txt %q, b.txt %q", v.read("a.txt"), v.read("b.txt"))
	}
}

// The same through the feed: a file deleted and a new one saved under its name.
func TestDeleteThenReuseIsNoConflict(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "old"})
	v.r.Remove(t, v.ws, "a.txt")
	v.r.Put(t, v.ws, "a.txt", []byte("new"))
	v.sync()
	v.sync()
	noConflicts(t, v)
	if v.read("a.txt") != "new" {
		t.Fatalf("a.txt %q", v.read("a.txt"))
	}
}

// O-R3: two files that swap names on the server swap here too.
func TestServerSwapConverges(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "A", "b.txt": "B"})
	v.r.Rename(t, v.ws, "a.txt", "t.txt")
	v.r.Rename(t, v.ws, "b.txt", "a.txt")
	v.r.Rename(t, v.ws, "t.txt", "b.txt")
	for range 3 {
		v.sync()
	}
	noConflicts(t, v)
	if v.read("a.txt") != "B" || v.read("b.txt") != "A" {
		t.Fatalf("a.txt %q, b.txt %q", v.read("a.txt"), v.read("b.txt"))
	}
}

// S-R6: a folder renamed onto the name of a case twin deleted in the same batch does not
// stop the workspace's sync.
func TestCaseOnlyRenameOverSkippedTwin(t *testing.T) {
	v := setup(t, nil)
	v.e.CaseFold = true
	v.r.Put(t, v.ws, "A/x.txt", []byte("x"))
	v.r.Put(t, v.ws, "a/y.txt", []byte("y"))
	v.sync()
	v.r.Remove(t, v.ws, "a")
	v.r.Rename(t, v.ws, "A", "a")
	v.r.Put(t, v.ws, "z.txt", []byte("z"))
	v.sync()
	v.sync()
	if !v.exists("z.txt") || v.read("a/x.txt") != "x" {
		t.Fatalf("local tree %v", tree(t, v.path("")))
	}
}

// S-R1: an edit that keeps the file's size and mtime (a container written in place with its
// timestamp preserved) is not deleted when the workspace is unchosen, or replaced by a
// server version.
func TestSameMetadataEditIsKept(t *testing.T) {
	v := setup(t, map[string]string{"vault.bin": "AAAA", "doc.txt": "1111"})
	for _, n := range []string{"vault.bin", "doc.txt"} {
		fi, err := os.Stat(v.path(n))
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(v.path(n), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteAt([]byte("BBBB"), 0)
		f.Close()
		os.Chtimes(v.path(n), fi.ModTime(), fi.ModTime())
	}
	v.r.Put(t, v.ws, "doc.txt", []byte("2222"))
	v.sync()
	if v.read("doc.txt") != "2222" || !slices.ContainsFunc(tree(t, v.path("")), func(p string) bool {
		return strings.HasPrefix(p, "doc (conflict")
	}) {
		t.Fatalf("the edit was replaced by the server version: %v", tree(t, v.path("")))
	}
	v.e.Settle = time.Hour // nothing more uploads
	if err := v.e.Choose(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// Only the edit is kept; the synced copies (the conflict copy this computer uploaded too)
	// match their digests and go.
	if got := tree(t, v.root); !slices.Equal(got, []string{"Unsynced/Docs/vault.bin"}) {
		t.Fatalf("after unchoose: %v", got)
	}
}
