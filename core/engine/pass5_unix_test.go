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
