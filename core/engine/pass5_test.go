package engine

import (
	"context"
	"os"
	"slices"
	"testing"
)

// Q9 B: a folder deleted here is not trashed on the server with a file a colleague added to
// it during the push; the folder delete waits for the next pass, whose pull brings the file.
func TestFolderTrashWaitsForChangesSincePull(t *testing.T) {
	v := setup(t, map[string]string{"X/a.txt": "a", "b.txt": "b"})
	ctx := context.Background()
	if err := os.RemoveAll(v.path("X")); err != nil {
		t.Fatal(err)
	}
	list, err := v.e.API.Workspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := v.e.workspace(v.ws)
	if err := v.e.pull(ctx, w, list[0].Head); err != nil {
		t.Fatal(err)
	}
	v.r.Put(t, v.ws, "X/new.txt", []byte("colleague")) // lands during the push
	w, _ = v.e.workspace(v.ws)
	if err := v.e.push(ctx, w); err != nil {
		t.Fatal(err)
	}
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"X/a.txt", "X/new.txt", "b.txt"}) {
		t.Fatalf("after the push, server paths %v", got)
	}
	v.sync()
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"X/new.txt", "b.txt"}) {
		t.Fatalf("server paths %v", got)
	}
	if v.read("X/new.txt") != "colleague" || v.exists("X/a.txt") {
		t.Fatal("local copies wrong")
	}
}

// Sol R1 / Opus R5: the server replaces a file with a folder of the same name, or the reverse,
// within one pull. The new item's record must not erase the old one's, whose removal frees
// the name.
func TestServerReplacesItemWithOtherKind(t *testing.T) {
	v := setup(t, map[string]string{"Reports": "old", "X/a.txt": "a"})
	v.r.Remove(t, v.ws, "Reports")
	v.r.Put(t, v.ws, "Reports/q1.txt", []byte("q1"))
	v.r.Remove(t, v.ws, "X")
	v.r.Put(t, v.ws, "X", []byte("now a file"))
	for range 3 {
		v.sync()
	}
	if !v.exists("Reports/q1.txt") || v.read("X") != "now a file" {
		t.Fatalf("local %v, server %v", tree(t, v.path("")), v.r.Paths(t, v.ws))
	}
}

// Sol R2 / Opus R6: two folders that swap names on the server converge.
func TestServerFolderSwapConverges(t *testing.T) {
	v := setup(t, map[string]string{"A/a.txt": "a", "B/b.txt": "b"})
	v.r.Rename(t, v.ws, "A", "T")
	v.r.Rename(t, v.ws, "B", "A")
	v.r.Rename(t, v.ws, "T", "B")
	for range 3 {
		v.sync()
	}
	if v.read("A/b.txt") != "b" || v.read("B/a.txt") != "a" || len(tree(t, v.path(""))) != 2 {
		t.Fatalf("local %v, server %v", tree(t, v.path("")), v.r.Paths(t, v.ws))
	}
}

// Opus R1: an item renamed on the server into a name this computer skips (Windows-invalid, a
// case twin) is not uploaded again from its old place as a new item.
func TestRenameIntoSkippedNameIsNotReuploaded(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "P/x.txt": "x", "Notes.txt": "n", "old.txt": "o"})
	v.e.WinNames, v.e.CaseFold = true, true
	v.r.Rename(t, v.ws, "a.txt", "a?.txt")
	v.r.Rename(t, v.ws, "P", "P?")
	v.r.Rename(t, v.ws, "old.txt", "notes.txt")
	for range 3 {
		v.sync()
	}
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"Notes.txt", "P?/x.txt", "a?.txt", "notes.txt"}) {
		t.Fatalf("server paths %v", got)
	}
	// A name this computer can hold again brings the file back.
	v.r.Rename(t, v.ws, "a?.txt", "b.txt")
	v.r.Rename(t, v.ws, "P?", "Q")
	v.sync()
	v.sync()
	if v.read("b.txt") != "a" || v.read("Q/x.txt") != "x" {
		t.Fatalf("local %v", tree(t, v.path("")))
	}
}

// Opus R4: on a case-insensitive disk, a file deleted on the server and saved again under a
// case variant of its name arrives.
func TestDeleteThenReuseCaseVariant(t *testing.T) {
	v := setup(t, map[string]string{"report.txt": "old"})
	v.e.CaseFold = true
	v.r.Remove(t, v.ws, "report.txt")
	v.r.Put(t, v.ws, "Report.txt", []byte("new"))
	for range 3 {
		v.sync()
	}
	if v.read("Report.txt") != "new" {
		t.Fatalf("local %v", tree(t, v.path("")))
	}
}

// Opus R7: a file deleted locally in a reader workspace comes back, so it is not trashed on
// the server once the user becomes an editor.
func TestReaderDeleteIsRestored(t *testing.T) {
	f := newFake(t)
	v := setupOn(t, f, map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c"})
	f.mu.Lock()
	f.reader = true
	f.mu.Unlock()
	if err := os.Remove(v.path("a.txt")); err != nil {
		t.Fatal(err)
	}
	v.sync()
	v.sync()
	if v.read("a.txt") != "a" {
		t.Fatal("a.txt not restored")
	}
	f.mu.Lock()
	f.reader = false
	f.mu.Unlock()
	v.sync()
	if got := v.r.Paths(t, v.ws); len(got) != 3 {
		t.Fatalf("server paths %v", got)
	}
}

// Certification round 1: a pending edit of a file the server renames into a case twin that
// then goes away still reaches the server, not Unsynced.
func TestCaseTwinRenameKeepsPendingEdit(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	v.e.CaseFold = true
	v.write("b.txt", "b-edited")
	v.r.Rename(t, v.ws, "b.txt", "A.txt")
	v.r.Rename(t, v.ws, "a.txt", "z.txt")
	for range 3 {
		v.sync()
	}
	if v.remote("A.txt") != "b-edited" || v.read("A.txt") != "b-edited" {
		t.Fatalf("local %v, server %v", tree(t, v.root), v.r.Paths(t, v.ws))
	}
}
