//go:build !windows

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCarryoverWipeWaitsForLinkedFolder(t *testing.T) {
	for _, rel := range []string{"Shared", ""} {
		t.Run("folder="+rel, func(t *testing.T) {
			v := setup(t, map[string]string{"Shared/a.txt": "synced", "Shared/edit.txt": "AAAA", "z.txt": "z"})
			away := filepath.Join(t.TempDir(), "moved")
			if err := os.Rename(v.path(rel), away); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(away, v.path(rel)); err != nil {
				t.Fatal(err)
			}
			if rel == "" {
				rewriteKeepingMetadata(t, filepath.Join(away, "Shared", "edit.txt"), "BBBB")
			} else {
				rewriteKeepingMetadata(t, filepath.Join(away, "edit.txt"), "BBBB")
			}
			if err := v.e.StartWipe(); err == nil || !strings.Contains(err.Error(), "link") {
				t.Fatalf("wipe %v", err)
			}
			if v.e.DB.Get(wipeKey) == "" {
				t.Fatal("wipe falsely completed")
			}
			if fi, err := os.Lstat(v.path(rel)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("link lost: %v", err)
			}
			items, _ := v.e.DB.Items(v.ws)
			if len(items) == 0 {
				t.Fatal("linked records lost")
			}
			moveBack(t, v, rel, away)
			if err := v.e.ResumeWipe(); err != nil {
				t.Fatal(err)
			}
			if v.e.DB.Get(wipeKey) != "" {
				t.Fatal("wipe did not complete after folder restored")
			}
			kept := filepath.Join(v.root, Unsynced, "Docs", "Shared", "edit.txt")
			if b, err := os.ReadFile(kept); err != nil || string(b) != "BBBB" {
				t.Fatalf("edit %q %v", b, err)
			}
		})
	}
}

func TestCarryoverWipeDoesNotTouchArbitraryLinkTarget(t *testing.T) {
	v := setup(t, map[string]string{"Shared/a.txt": "same bytes"})
	away := t.TempDir()
	os.WriteFile(filepath.Join(away, "a.txt"), []byte("same bytes"), 0600)
	os.RemoveAll(v.path("Shared"))
	if err := os.Symlink(away, v.path("Shared")); err != nil {
		t.Fatal(err)
	}
	if err := v.e.StartWipe(); err == nil {
		t.Fatal("wipe reported success through link")
	}
	if b, err := os.ReadFile(filepath.Join(away, "a.txt")); err != nil || string(b) != "same bytes" {
		t.Fatalf("unrelated target %q %v", b, err)
	}
}
