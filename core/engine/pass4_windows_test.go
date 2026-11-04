package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// Certification round 2: a synced folder moved to another disk with a junction left in its
// place (mklink /J, no administrator needed) is not trashed on the server, nor is anything in it.
func TestJunctionInPlaceOfSyncedFolderIsNotTrashed(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "Shared/x.txt": "x", "Shared/sub/y.txt": "y"})
	away := filepath.Join(t.TempDir(), "Shared")
	if err := os.Rename(v.path("Shared"), away); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", v.path("Shared"), away).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	v.sync()
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"Shared/sub/y.txt", "Shared/x.txt", "a.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}
