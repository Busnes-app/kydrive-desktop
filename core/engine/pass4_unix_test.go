//go:build !windows

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// R2 (both reviewers): a synced folder or file replaced by a symlink (moved to another disk,
// a link left behind) is not trashed on the server.
func TestLinkInPlaceOfSyncedItemIsNotTrashed(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "Shared/x.txt": "x", "f.txt": "f"})
	away := t.TempDir()
	if err := os.Rename(v.path("Shared"), filepath.Join(away, "Shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(away, "Shared"), v.path("Shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(v.path("f.txt"), filepath.Join(away, "f.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(away, "f.txt"), v.path("f.txt")); err != nil {
		t.Fatal(err)
	}
	v.sync()
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"Shared/x.txt", "a.txt", "f.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}
