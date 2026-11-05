package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// B1: even skipped destinations must account for the source before replacing its identity.
func TestCarryoverMovedToSkippedDestination(t *testing.T) {
	for _, mode := range []string{"ignored", "windows-invalid", "case-twin"} {
		for _, edited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/edited=%v", mode, edited), func(t *testing.T) {
				v := setup(t, map[string]string{"move.txt": "original", "keep.txt": "keep"})
				dest := v.r.NewWorkspace(t, "Archive") // destination sorts before Docs
				name := "moved.tmp"
				switch mode {
				case "windows-invalid":
					v.e.WinNames = true
					name = "moved?.txt"
				case "case-twin":
					v.e.CaseFold = true
					name = "taken.txt"
					v.r.Put(t, dest, "Taken.txt", []byte("incumbent"))
				}
				if err := v.e.Choose(context.Background(), []string{v.ws, dest}); err != nil {
					t.Fatal(err)
				}
				v.sync()
				moveAcrossWorkspaces(t, v, "move.txt", dest, name)
				if edited {
					v.write("move.txt", "local pending edit")
				}
				v.sync()
				v.sync()
				v.sync()
				if v.exists("move.txt") {
					t.Error("skipped destination resurrected the source copy")
				}
				if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"keep.txt"}) {
					t.Errorf("source server paths %v", got)
				}
				if b, ok := v.r.Read(t, dest, name); !ok || string(b) != "original" {
					t.Errorf("moved destination %q present=%v", b, ok)
				}
				if edited {
					p := filepath.Join(v.root, Unsynced, "Docs", "move.txt")
					if b, err := os.ReadFile(p); err != nil || string(b) != "local pending edit" {
						t.Errorf("preserved edit %q %v", b, err)
					}
				}
				if mode == "case-twin" {
					if b, err := os.ReadFile(filepath.Join(v.root, "Archive", "Taken.txt")); err != nil || string(b) != "incumbent" {
						t.Errorf("incumbent %q %v", b, err)
					}
				}
			})
		}
	}
}

// Cancel at an observable phase boundary, after real filesystem/DB progress, never on entry.
// A loop that checks only before its phase cannot pass these controls.
type cancelAfterProgress struct {
	context.Context
	cancel     context.CancelFunc
	progressed func() bool
	triggered  bool
}

func (c *cancelAfterProgress) Err() error {
	if c.progressed() {
		c.triggered = true
		c.cancel()
	}
	return c.Context.Err()
}

func TestCarryoverCancelDuringDirectorySweep(t *testing.T) {
	v := setup(t, map[string]string{"Top/a/x.txt": "a", "Top/b/x.txt": "b", "Top/c/x.txt": "c"})
	ws, err := v.e.DB.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	folderID := v.r.ID(t, v.ws, "Top")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := &cancelAfterProgress{Context: ctx, cancel: cancel, progressed: func() bool { return missing(v.path("Top/c")) }}
	v.e.Context = progress
	err = v.e.evict(ws[0], "Top")
	if !progress.triggered || !errors.Is(err, context.Canceled) {
		t.Errorf("sweep ignored cancellation after first directory: triggered=%v err=%v", progress.triggered, err)
	}
	if v.exists("Top/c") {
		t.Error("directory sweep never began")
	}
	if !v.exists("Top/b") || !v.exists("Top/a") || !v.exists("Top") {
		t.Error("sweep continued over remaining directories")
	}
	if it, err := v.e.DB.Item(folderID); err != nil || it == nil {
		t.Errorf("interrupted sweep committed whole-tree completion: %+v %v", it, err)
	}
}

func TestCarryoverCancelDuringDeleteClassification(t *testing.T) {
	// Synthetic Finder bookmark controls already used by the existing alias regressions.
	aliases = true
	defer func() { aliases = runtime.GOOS == "darwin" }()
	v := setup(t, map[string]string{"a/x.txt": "a", "b/x.txt": "b"})
	for _, name := range []string{"a", "b"} {
		if err := os.Rename(v.path(name), filepath.Join(t.TempDir(), name)); err != nil {
			t.Fatal(err)
		}
		v.write(name, "book\x00\x00\x00\x00mark\x00\x00\x00\x00alias data")
	}
	ws, err := v.e.DB.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := v.e.scan(ws[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := &cancelAfterProgress{Context: ctx, cancel: cancel, progressed: func() bool {
		issues, err := v.e.DB.Issues()
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(issues, func(i Issue) bool { return i.Path == "a" && i.Kind == "skipped" })
	}}
	v.e.Context = progress
	err = v.e.deletes(progress, ws[0], entries, false)
	if !progress.triggered || !errors.Is(err, context.Canceled) {
		t.Errorf("classification ignored cancellation after first item: triggered=%v err=%v", progress.triggered, err)
	}
	issues, err := v.e.DB.Issues()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Path != "a" {
		t.Errorf("classification continued over remaining items: %+v", issues)
	}
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"a/x.txt", "b/x.txt"}) {
		t.Errorf("classification changed server %v", got)
	}
}

func TestCarryoverSkippedMoveWaitsForSourceCleanup(t *testing.T) {
	v := setup(t, map[string]string{"ro/move.txt": "original", "keep.txt": "keep"})
	dest := v.r.NewWorkspace(t, "Archive")
	if err := v.e.Choose(context.Background(), []string{v.ws, dest}); err != nil {
		t.Fatal(err)
	}
	v.sync()
	id := v.r.ID(t, v.ws, "ro/move.txt")
	readOnly(t, v.path("ro"))
	moveAcrossWorkspaces(t, v, "ro/move.txt", dest, "moved.tmp")
	v.sync()
	if it, err := v.e.DB.Item(id); err != nil || it == nil || it.Workspace != v.ws {
		t.Fatalf("blocked source identity replaced: %+v %v", it, err)
	}
	source, err := v.e.DB.Retries(v.ws)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := v.e.DB.Retries(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(source) != 1 || len(destination) != 1 {
		t.Fatalf("independent cleanup/placement retries: source=%+v destination=%+v", source, destination)
	}
	if err := v.e.Choose(context.Background(), []string{v.ws}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(v.path("ro"), 0755); err != nil {
		t.Fatal(err)
	}
	v.sync()
	v.sync()
	if v.exists("ro/move.txt") {
		t.Fatal("source cleanup was lost when skipped destination was unchosen")
	}
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"keep.txt"}) {
		t.Fatalf("source server %v", got)
	}
	if b, ok := v.r.Read(t, dest, "moved.tmp"); !ok || string(b) != "original" {
		t.Fatalf("destination %q present=%v", b, ok)
	}
}
