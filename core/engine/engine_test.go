package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
)

// remote is the server side of a test: the in-process fake, or a real kydrive-server.
type remote interface {
	URL() string
	Token() string
	NewWorkspace(t *testing.T, name string) string
	Put(t *testing.T, ws, path string, data []byte) // create or add a version, making folders
	Read(t *testing.T, ws, path string) ([]byte, bool)
	ID(t *testing.T, ws, path string) string
	Paths(t *testing.T, ws string) []string // live file paths
	Remove(t *testing.T, ws, path string)   // move to trash
	Rename(t *testing.T, ws, path, name string)
	DropAccess(t *testing.T, ws string)
	Revoke(t *testing.T, wipe bool)
}

// newRemote is replaced by e2e_test.go's real-server harness when KYDRIVE_SERVER_BIN is set.
var newRemote = func(t *testing.T) remote { return newFake(t) }

type env struct {
	t    *testing.T
	r    remote
	e    *Engine
	ws   string
	root string
}

var today = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T, files map[string]string) *env {
	t.Helper()
	r := newRemote(t)
	ws := r.NewWorkspace(t, "Docs")
	for p, data := range files {
		r.Put(t, ws, p, []byte(data))
	}
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	v := &env{t: t, r: r, ws: ws, root: t.TempDir()}
	v.e = &Engine{Root: v.root, Device: "laptop", DB: db, API: &api.Client{Base: r.URL(), Token: r.Token(), HTTP: http.DefaultClient}, Now: func() time.Time { return today }}
	if err := v.e.Choose(context.Background(), []string{ws}); err != nil {
		t.Fatal(err)
	}
	v.sync()
	return v
}

func (v *env) sync() {
	v.t.Helper()
	if err := v.e.Sync(context.Background()); err != nil {
		v.t.Fatal(err)
	}
}

func (v *env) path(rel string) string { return filepath.Join(v.root, "Docs", filepath.FromSlash(rel)) }

// write changes a local file and backdates it so it counts as settled.
func (v *env) write(rel, data string) {
	v.t.Helper()
	p := v.path(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		v.t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute - time.Duration(len(data))*time.Second)
	os.Chtimes(p, old, old)
}

func (v *env) read(rel string) string {
	v.t.Helper()
	b, err := os.ReadFile(v.path(rel))
	if err != nil {
		v.t.Fatal(err)
	}
	return string(b)
}

func (v *env) exists(rel string) bool {
	_, err := os.Lstat(v.path(rel))
	return err == nil
}

func (v *env) remote(rel string) string {
	v.t.Helper()
	b, ok := v.r.Read(v.t, v.ws, rel)
	if !ok {
		v.t.Fatalf("%s is not on the server", rel)
	}
	return string(b)
}

// tree lists every regular file under dir, relative and slash-separated.
func tree(t *testing.T, dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func TestInitialSync(t *testing.T) {
	v := setup(t, map[string]string{"a/b.txt": "bee", "c.txt": "sea"})
	if v.read("a/b.txt") != "bee" || v.read("c.txt") != "sea" {
		t.Fatal("initial sync did not download")
	}
	fi, _ := os.Stat(v.path("c.txt"))
	if !fi.ModTime().Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("mtime %v, want the server's", fi.ModTime())
	}
	if got := tree(t, v.root); !slices.Equal(got, []string{"Docs/a/b.txt", "Docs/c.txt"}) {
		t.Fatalf("stray files: %v", got)
	}
}

func TestDownloadRejectsWrongDigest(t *testing.T) {
	f := newFake(t)
	newRemote = func(*testing.T) remote { return f }
	defer func() { newRemote = func(t *testing.T) remote { return newFake(t) } }()
	f.corrupt = true
	v := setup(t, nil)
	v.r.Put(t, v.ws, "x.txt", []byte("hello"))
	v.e.Sync(context.Background())
	if got := tree(t, v.root); len(got) != 0 {
		t.Fatalf("corrupt download left %v", got)
	}
	f.corrupt = false
	v.sync()
	if v.read("x.txt") != "hello" {
		t.Fatal("retry did not download")
	}
}

func TestRemoteChanges(t *testing.T) {
	v := setup(t, map[string]string{"a/b.txt": "bee", "c.txt": "sea", "d.txt": "dee"})
	v.r.Put(t, v.ws, "c.txt", []byte("sea 2"))
	v.r.Rename(t, v.ws, "a", "z")
	v.r.Remove(t, v.ws, "d.txt")
	v.r.Put(t, v.ws, "new/e.txt", []byte("eee"))
	v.sync()
	if got := tree(t, v.path("")); !slices.Equal(got, []string{"c.txt", "new/e.txt", "z/b.txt"}) {
		t.Fatalf("local tree %v", got)
	}
	if v.read("c.txt") != "sea 2" {
		t.Fatal("remote edit not applied")
	}
}

func TestLocalChanges(t *testing.T) {
	v := setup(t, map[string]string{"keep.txt": "k", "edit.txt": "e", "move.txt": "m", "gone.txt": "g"})
	id := v.r.ID(t, v.ws, "move.txt")
	v.write("edit.txt", "edited")
	v.write("dir/new.txt", "new")
	os.Rename(v.path("move.txt"), v.path("dir/moved.txt"))
	os.Remove(v.path("gone.txt"))
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"dir/moved.txt", "dir/new.txt", "edit.txt", "keep.txt"}) {
		t.Fatalf("server paths %v", got)
	}
	if v.remote("edit.txt") != "edited" || v.remote("dir/new.txt") != "new" {
		t.Fatal("upload content wrong")
	}
	if v.r.ID(t, v.ws, "dir/moved.txt") != id {
		t.Fatal("a local move was not sent as a move")
	}
	v.sync()
	if got := v.r.Paths(t, v.ws); len(got) != 4 {
		t.Fatalf("second pass changed the server: %v", got)
	}
}

func TestUnsettledFileWaits(t *testing.T) {
	v := setup(t, nil)
	v.e.Settle = time.Hour
	os.WriteFile(v.path("busy.txt"), []byte("still writing"), 0o644)
	v.sync()
	if len(v.r.Paths(t, v.ws)) != 0 {
		t.Fatal("a file being written was uploaded")
	}
}

func TestIgnoreRules(t *testing.T) {
	v := setup(t, nil)
	for _, n := range []string{"~$doc.docx", ".~lock.a.odt#", "x.tmp", ".DS_Store", "Thumbs.db", "desktop.ini"} {
		v.write(n, "junk")
	}
	v.write("real.txt", "r")
	v.sync()
	if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"real.txt"}) {
		t.Fatalf("server paths %v", got)
	}
}

func conflictName(base, ext string) string {
	return base + " (conflict from laptop 2026-10-18)" + ext
}

// TestConflictKeepsBoth: a stale local edit never overwrites the server's newer version, and
// the server's version never overwrites the local edit.
func TestConflictKeepsBoth(t *testing.T) {
	t.Run("remote change to a locally edited file", func(t *testing.T) {
		v := setup(t, map[string]string{"doc.txt": "v1"})
		v.r.Put(t, v.ws, "doc.txt", []byte("theirs"))
		v.write("doc.txt", "mine")
		v.sync()
		v.sync()
		checkBoth(t, v)
	})
	t.Run("refused upload", func(t *testing.T) {
		v := setup(t, map[string]string{"doc.txt": "v1"})
		v.write("doc.txt", "mine")
		v.r.Put(t, v.ws, "doc.txt", []byte("theirs"))
		// Push alone, as if the remote change landed after this pass read the feed.
		ws, _ := v.e.DB.Workspaces()
		if err := v.e.push(context.Background(), ws[0]); err != nil {
			t.Fatal(err)
		}
		v.sync()
		v.sync()
		checkBoth(t, v)
	})
}

func checkBoth(t *testing.T, v *env) {
	t.Helper()
	c := conflictName("doc", ".txt")
	if v.read("doc.txt") != "theirs" || v.read(c) != "mine" {
		t.Fatalf("local: %v", tree(t, v.path("")))
	}
	if v.remote("doc.txt") != "theirs" || v.remote(c) != "mine" {
		t.Fatalf("server: %v", v.r.Paths(t, v.ws))
	}
	issues, _ := v.e.DB.Issues()
	if len(issues) == 0 || issues[0].Kind != "conflict" || issues[0].Path != c {
		t.Fatalf("conflict not listed: %+v", issues)
	}
}

func TestRemoteRemovalKeepsEdit(t *testing.T) {
	v := setup(t, map[string]string{"a/doc.txt": "v1", "a/other.txt": "o"})
	v.write("a/doc.txt", "mine")
	v.write("a/new.txt", "unuploaded")
	v.r.Remove(t, v.ws, "a")
	v.e.Settle = time.Hour // the edits stay unuploaded while the removal arrives
	v.sync()
	if v.exists("a") {
		t.Fatal("removed folder still here")
	}
	if got := tree(t, filepath.Join(v.root, "Unsynced")); !slices.Equal(got, []string{"Docs/a/doc.txt", "Docs/a/new.txt"}) {
		t.Fatalf("Unsynced holds %v", got)
	}
}

// TestLostAccessKeepsUnsynced: losing a workspace removes its synced copies but moves every
// unuploaded edit to Unsynced/.
func TestLostAccessKeepsUnsynced(t *testing.T) {
	v := setup(t, map[string]string{"a/synced.txt": "s", "edited.txt": "e"})
	v.e.Settle = time.Hour
	v.write("edited.txt", "my edit")
	v.write("a/new.txt", "new work")
	v.r.DropAccess(t, v.ws)
	v.sync()
	if v.exists("") {
		t.Fatalf("workspace folder still here: %v", tree(t, v.path("")))
	}
	unsynced := filepath.Join(v.root, "Unsynced", "Docs")
	if got := tree(t, unsynced); !slices.Equal(got, []string{"a/new.txt", "edited.txt"}) {
		t.Fatalf("Unsynced holds %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(unsynced, "edited.txt")); string(b) != "my edit" {
		t.Fatal("edit lost")
	}
	if ws, _ := v.e.DB.Workspaces(); len(ws) != 0 {
		t.Fatal("workspace still chosen")
	}
}

// TestWipeKeepsUnsynced: a revoke with wipe stops syncing, removes synced copies and keeps
// every unuploaded edit.
func TestWipeKeepsUnsynced(t *testing.T) {
	v := setup(t, map[string]string{"synced.txt": "s", "edited.txt": "e"})
	v.e.Settle = time.Hour
	v.write("edited.txt", "my edit")
	v.write("new.txt", "new work")
	v.r.Revoke(t, true)
	err := v.e.Sync(context.Background())
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("got %v, want ErrRevoked", err)
	}
	if got := tree(t, v.root); !slices.Equal(got, []string{"Unsynced/Docs/edited.txt", "Unsynced/Docs/new.txt"}) {
		t.Fatalf("after wipe: %v", got)
	}
}

func TestRevokeWithoutWipeKeepsFiles(t *testing.T) {
	v := setup(t, map[string]string{"synced.txt": "s"})
	v.r.Revoke(t, false)
	if err := v.e.Sync(context.Background()); !errors.Is(err, ErrRevoked) {
		t.Fatalf("got %v", err)
	}
	if v.read("synced.txt") != "s" {
		t.Fatal("files removed without a wipe")
	}
}

// TestLargeDeletePauses: deleting most of a workspace locally waits for the user.
func TestLargeDeletePauses(t *testing.T) {
	files := map[string]string{}
	for i := range 30 {
		files[fmt.Sprintf("f%02d.txt", i)] = "x"
	}
	v := setup(t, files)
	for i := range 25 {
		os.Remove(v.path(fmt.Sprintf("f%02d.txt", i)))
	}
	v.sync()
	if n := len(v.r.Paths(t, v.ws)); n != 30 {
		t.Fatalf("server has %d files; the guard did not hold", n)
	}
	ws, _ := v.e.DB.Workspaces()
	if ws[0].Deletes != 25 {
		t.Fatalf("held deletes %d", ws[0].Deletes)
	}
	t.Run("refuse restores", func(t *testing.T) {
		v.e.ConfirmDeletes(v.ws, false)
		v.sync()
		if got := tree(t, v.path("")); len(got) != 30 {
			t.Fatalf("local has %d files", len(got))
		}
	})
	t.Run("confirm deletes", func(t *testing.T) {
		for i := range 25 {
			os.Remove(v.path(fmt.Sprintf("f%02d.txt", i)))
		}
		v.sync()
		v.e.ConfirmDeletes(v.ws, true)
		v.sync()
		if n := len(v.r.Paths(t, v.ws)); n != 5 {
			t.Fatalf("server has %d files", n)
		}
	})
}

func TestSmallDeleteDoesNotPause(t *testing.T) {
	files := map[string]string{}
	for i := range 20 {
		files[fmt.Sprintf("f%02d.txt", i)] = "x"
	}
	v := setup(t, files)
	for i := range 20 {
		os.Remove(v.path(fmt.Sprintf("f%02d.txt", i)))
	}
	v.sync()
	if n := len(v.r.Paths(t, v.ws)); n != 0 {
		t.Fatalf("20 deletes were held: %d left", n)
	}
}

func TestNameHazards(t *testing.T) {
	v := setup(t, nil)
	v.e.CaseFold, v.e.WinNames = true, true
	for _, n := range []string{"Same.txt", "same.txt", "CON", "aux.txt", "trail.", "a<b", "ok.txt"} {
		v.r.Put(t, v.ws, n, []byte(n))
	}
	v.sync()
	if got := tree(t, v.path("")); len(got) != 2 || got[1] != "ok.txt" {
		t.Fatalf("local %v", got)
	}
	issues, _ := v.e.DB.Issues()
	var skipped []string
	for _, i := range issues {
		if i.Kind == "skipped" {
			skipped = append(skipped, i.Path)
		}
	}
	slices.Sort(skipped)
	if len(skipped) != 5 || !slices.Contains(skipped, "CON") || !slices.Contains(skipped, "a<b") {
		t.Fatalf("skipped %v", skipped)
	}
	v.sync()
	if n := len(v.r.Paths(t, v.ws)); n != 7 {
		t.Fatalf("a skipped item was removed from the server: %d left", n)
	}
}

func TestResnapshotAfterGone(t *testing.T) {
	f := newFake(t)
	newRemote = func(*testing.T) remote { return f }
	defer func() { newRemote = func(t *testing.T) remote { return newFake(t) } }()
	v := setup(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	v.r.Put(t, v.ws, "c.txt", []byte("c"))
	v.r.Remove(t, v.ws, "a.txt")
	f.floor = int64(len(f.log))
	v.sync()
	if got := tree(t, v.path("")); !slices.Equal(got, []string{"b.txt", "c.txt"}) {
		t.Fatalf("local %v", got)
	}
}

func TestWorkspaceFolders(t *testing.T) {
	v := setup(t, nil)
	second := v.r.NewWorkspace(t, "Docs")
	third := v.r.NewWorkspace(t, "Unsynced")
	if err := v.e.Choose(context.Background(), []string{v.ws, second, third}); err != nil {
		t.Fatal(err)
	}
	ws, _ := v.e.DB.Workspaces()
	var dirs []string
	for _, w := range ws {
		dirs = append(dirs, w.Dir)
	}
	if !slices.Equal(dirs, []string{"Docs", "Docs (2)", "Unsynced (2)"}) {
		t.Fatalf("dirs %v", dirs)
	}
	v.r.Put(t, second, "x.txt", []byte("x"))
	v.sync()
	if b, _ := os.ReadFile(filepath.Join(v.root, "Docs (2)", "x.txt")); string(b) != "x" {
		t.Fatal("second workspace not synced")
	}
}

func TestUnchooseKeepsEdits(t *testing.T) {
	v := setup(t, map[string]string{"s.txt": "s"})
	v.e.Settle = time.Hour
	v.write("new.txt", "work")
	if err := v.e.Choose(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, v.root); !slices.Equal(got, []string{"Unsynced/Docs/new.txt"}) {
		t.Fatalf("after unchoose: %v", got)
	}
	if !strings.Contains(fmt.Sprint(v.r.Paths(t, v.ws)), "s.txt") {
		t.Fatal("unchoose touched the server")
	}
}
