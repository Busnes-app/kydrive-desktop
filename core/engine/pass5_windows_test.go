package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Certification round 2: a remote wipe reaches the synced copies behind a junction left in
// place of a synced folder moved to another disk (EvalSymlinks does not follow junctions).
func TestWipeReachesBehindJunction(t *testing.T) {
	f := newFake(t)
	v := setupOn(t, f, map[string]string{"a.txt": "a", "S/plan.txt": "p"})
	away := filepath.Join(t.TempDir(), "S")
	if err := os.Rename(v.path("S"), away); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", v.path("S"), away).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	f.Revoke(t, true)
	v.e.Sync(context.Background())
	if got := tree(t, away); len(got) != 0 {
		t.Fatalf("left behind the junction: %v", got)
	}
}
