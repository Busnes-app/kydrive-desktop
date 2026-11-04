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
