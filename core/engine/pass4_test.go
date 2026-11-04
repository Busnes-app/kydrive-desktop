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
// foldBoth runs f with exact names, with case twins folded (macOS), and with the Windows
// name rules too.
func foldBoth(t *testing.T, files map[string]string, f func(t *testing.T, v *env)) {
	for _, mode := range []string{"exact", "casefold", "windows"} {
		t.Run(mode, func(t *testing.T) {
			v := setup(t, files)
			v.e.CaseFold, v.e.WinNames = mode != "exact", mode == "windows"
			f(t, v)
		})
	}
}

// A real file with a short-shaped name is reused and renamed in case like any other.
func TestShortShapedNameReuseAndCaseRename(t *testing.T) {
	foldBoth(t, map[string]string{"REPORT~1.TXT": "old", "NOTES~2.TXT": "n"}, func(t *testing.T, v *env) {
		v.r.Remove(t, v.ws, "REPORT~1.TXT")
		v.r.Put(t, v.ws, "REPORT~1.TXT", []byte("new"))
		v.r.Rename(t, v.ws, "NOTES~2.TXT", "notes~2.txt")
		v.sync()
		v.sync()
		noConflicts(t, v)
		if v.read("REPORT~1.TXT") != "new" || v.read("notes~2.txt") != "n" {
			t.Fatalf("local %v", tree(t, v.path("")))
		}
		if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"REPORT~1.TXT", "notes~2.txt"}) {
			t.Fatalf("server paths %v", got)
		}
	})
}

// A folder moved onto a path still recorded for a folder deleted here and on the server waits
// for that record to go, instead of failing the pull with a UNIQUE error.
func TestFolderMoveOntoARecordedPathWaits(t *testing.T) {
	v := setup(t, map[string]string{"F/x.txt": "x", "G/y.txt": "y"})
	if err := os.RemoveAll(v.path("F")); err != nil {
		t.Fatal(err)
	}
	v.r.Remove(t, v.ws, "F")
	v.r.Rename(t, v.ws, "G", "F")
	v.sync()
	v.sync()
	if got := tree(t, v.path("")); !slices.Equal(got, []string{"F/y.txt"}) {
		t.Fatalf("local %v", got)
	}
}

func TestSnapshotRenameThenReuseIsNoConflict(t *testing.T) {
	foldBoth(t, map[string]string{"a.txt": "old"}, testSnapshotRenameThenReuse)
}

func testSnapshotRenameThenReuse(t *testing.T, v *env) {
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
	foldBoth(t, map[string]string{"a.txt": "old"}, testDeleteThenReuse)
}

func testDeleteThenReuse(t *testing.T, v *env) {
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
	foldBoth(t, map[string]string{"a.txt": "A", "b.txt": "B"}, testServerSwap)
}

func testServerSwap(t *testing.T, v *env) {
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
	inPlace := func(n string) {
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
	inPlace("doc.txt")
	v.r.Put(t, v.ws, "doc.txt", []byte("2222"))
	v.sync()
	inPlace("vault.bin") // not uploaded yet when the workspace is unchosen
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

// A swap where one copy has an edit not yet uploaded keeps the edit syncing: it uploads to
// the file it belongs to, never to Unsynced or a conflict copy.
func TestServerSwapKeepsAnUnuploadedEdit(t *testing.T) {
	foldBoth(t, map[string]string{"a.txt": "A", "b.txt": "B"}, func(t *testing.T, v *env) {
		v.write("a.txt", "A-edited")
		v.r.Rename(t, v.ws, "a.txt", "t.txt")
		v.r.Rename(t, v.ws, "b.txt", "a.txt")
		v.r.Rename(t, v.ws, "t.txt", "b.txt")
		for range 4 {
			v.sync()
		}
		noConflicts(t, v)
		if got := tree(t, v.root); slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, "Unsynced") }) {
			t.Fatalf("the edit left syncing: %v", got)
		}
		if v.remote("b.txt") != "A-edited" || v.read("b.txt") != "A-edited" || v.read("a.txt") != "B" {
			t.Fatalf("server b.txt %q; local a.txt %q b.txt %q", v.remote("b.txt"), v.read("a.txt"), v.read("b.txt"))
		}
	})
}

// R-1 (certification): on a case-insensitive disk, a file deleted and another saved under a
// case variant of its name is a delete and a new file, as before.
func TestCaseVariantReplacementIsADelete(t *testing.T) {
	v := setup(t, map[string]string{"Budget.txt": "old"})
	probe := filepath.Join(t.TempDir(), "Probe")
	os.WriteFile(probe, nil, 0o644)
	if _, err := os.Lstat(strings.ToLower(probe)); err != nil {
		t.Skip("case-sensitive file system")
	}
	os.Remove(v.path("Budget.txt"))
	v.write("budget.txt", "new")
	v.sync()
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"budget.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}

// O-R4: on Windows a new server name shaped like a short (8.3) name is not placed over the
// item that name already opens here. (Simulated: the alias is a file already at that path.)
func TestShortNameIsNotPlacedOverAnotherItem(t *testing.T) {
	v := setup(t, nil)
	v.e.WinNames, v.e.Settle = true, time.Hour
	v.r.Put(t, v.ws, "REPORT~1.TXT", []byte("theirs"))
	v.write("REPORT~1.TXT", "mine")
	v.sync()
	if v.read("REPORT~1.TXT") != "mine" || len(tree(t, v.path(""))) != 1 {
		t.Fatalf("local %v", tree(t, v.path("")))
	}
	for _, n := range []string{"REPORT~1.TXT", "AB~12.C", "X~1"} {
		if !shortName(n) {
			t.Errorf("%s is a short name", n)
		}
	}
	for _, n := range []string{"Report for 2026.txt", "a~b.txt", "LONGNAME~1.TXT", "~1.txt", "A~1.HTML"} {
		if shortName(n) {
			t.Errorf("%s is not a short name", n)
		}
	}
}

// S-R1 (certification): an in-place edit keeping size and mtime that lands while a new
// server version downloads is not replaced by it.
func TestSameMetadataEditDuringDownloadIsKept(t *testing.T) {
	v := setup(t, map[string]string{"doc.txt": "1111"})
	fi, err := os.Stat(v.path("doc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") && fi != nil {
			f, _ := os.OpenFile(v.path("doc.txt"), os.O_WRONLY, 0)
			f.WriteAt([]byte("BBBB"), 0)
			f.Close()
			os.Chtimes(v.path("doc.txt"), fi.ModTime(), fi.ModTime())
			fi = nil
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	v.e.API.Base = front.URL
	v.r.Put(t, v.ws, "doc.txt", []byte("2222"))
	v.sync()
	v.sync()
	var kept bool
	for _, p := range tree(t, v.path("")) {
		kept = kept || strings.HasPrefix(p, "doc (conflict") && v.read(p) == "BBBB"
	}
	if !kept || v.read("doc.txt") != "2222" {
		t.Fatalf("the edit was replaced: %v", tree(t, v.path("")))
	}
}

// Certification round 2: a swap where both copies have edits not yet uploaded converges too;
// both edits reach the files they belong to.
func TestServerSwapWithBothEdited(t *testing.T) {
	foldBoth(t, map[string]string{"a.txt": "A", "b.txt": "B"}, func(t *testing.T, v *env) {
		v.write("a.txt", "A-edited")
		v.write("b.txt", "B-edited")
		v.r.Rename(t, v.ws, "a.txt", "t.txt")
		v.r.Rename(t, v.ws, "b.txt", "a.txt")
		v.r.Rename(t, v.ws, "t.txt", "b.txt")
		for range 5 {
			v.sync()
		}
		noConflicts(t, v)
		if got := tree(t, v.root); slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, "Unsynced") }) {
			t.Fatalf("an edit left syncing: %v", got)
		}
		if v.remote("b.txt") != "A-edited" || v.remote("a.txt") != "B-edited" || v.read("b.txt") != "A-edited" || v.read("a.txt") != "B-edited" {
			t.Fatalf("server a %q b %q; local a %q b %q", v.remote("a.txt"), v.remote("b.txt"), v.read("a.txt"), v.read("b.txt"))
		}
	})
}
