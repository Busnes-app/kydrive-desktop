package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCarryoverWipeWaitsForJunction(t *testing.T) {
	for _, rel := range []string{"Shared", ""} {
		t.Run("folder="+rel, func(t *testing.T) {
			v := setup(t, map[string]string{"Shared/a.txt": "synced", "Shared/edit.txt": "AAAA", "z.txt": "z"})
			away := filepath.Join(t.TempDir(), "moved")
			if err := os.Rename(v.path(rel), away); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", v.path(rel), away).CombinedOutput(); err != nil {
				t.Fatalf("junction: %v %s", err, out)
			}
			if err := v.e.StartWipe(); err == nil || !strings.Contains(err.Error(), "link") {
				t.Fatalf("wipe %v", err)
			}
			if v.e.DB.Get(wipeKey) == "" {
				t.Fatal("wipe falsely completed")
			}
			target := filepath.Join(away, "a.txt")
			if rel == "" {
				target = filepath.Join(away, "Shared", "a.txt")
			}
			if b, err := os.ReadFile(target); err != nil || string(b) != "synced" {
				t.Fatalf("target %q %v", b, err)
			}
			if err := os.Remove(v.path(rel)); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(away, v.path(rel)); err != nil {
				t.Fatal(err)
			}
			if err := v.e.ResumeWipe(); err != nil {
				t.Fatal(err)
			}
			if v.e.DB.Get(wipeKey) != "" {
				t.Fatal("wipe did not resume")
			}
		})
	}
}
