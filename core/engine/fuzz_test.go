package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
)

// FuzzServerNames syncs a workspace whose name, folder name and file name come from the
// server, as untrusted input, and checks that no name leads a write outside the sync root
// and that an ordinary name lands where the server put it. CI runs it briefly on every PR;
// `go test ./engine -run '^$' -fuzz FuzzServerNames` runs it for longer.
func FuzzServerNames(f *testing.F) {
	for _, s := range [][3]string{
		{"Docs", "Reports", "q3.txt"},
		{"..", "..", ".."},
		{".", ".", "."},
		{"../escape", "a/../../b", `..\..\c`},
		{"Docs", "../../../escape", `..\..\..\escape`},
		{"", "", ""},
		{" Docs ", " x", "y "},
		{"CON", "NUL.txt", "REPORT~1.TXT"},
		{"Unsynced", "Unsynced", ".kydrive-x.tmp"},
		{"a\x00b", "\u202etxt.exe", "\ufeffname"},
		{"C:", `C:\Windows`, "/etc/passwd"},
		{strings.Repeat("w", 300), strings.Repeat("é", 128), strings.Repeat("f", 256)},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, wsName, folderName, fileName string) {
		r := newFake(t)
		ws := r.NewWorkspace(t, wsName)
		r.mu.Lock()
		dir := &fitem{id: r.id(), ws: ws, kind: "folder", name: folderName}
		file := &fitem{id: r.id(), ws: ws, kind: "file", parent: dir.id, name: fileName, rev: 1, data: []byte("payload"), mtime: "2026-01-02T03:04:05Z"}
		r.items[dir.id], r.items[file.id] = dir, file
		r.record(ws, "folder", dir.id)
		r.record(ws, "file", file.id)
		r.mu.Unlock()

		// Not t.TempDir(): a fuzz test's directory name holds '#', which Open's file: URI reads as
		// a fragment, so every input would share one database at the truncated path.
		dbDir, err := os.MkdirTemp("", "kydrive-fuzz-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dbDir)
		db, err := Open(filepath.Join(dbDir, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		// The root sits four levels down an otherwise empty tree, so a name that climbs out
		// of it lands somewhere this test can see.
		base := t.TempDir()
		root := filepath.Join(base, "a", "b", "c", "root")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		e := &Engine{Root: root, Device: "laptop", DB: db, API: &api.Client{Base: r.URL(), Token: r.Token(), HTTP: &http.Client{Transport: inMemory{r.srv.Config.Handler}}}, Now: func() time.Time { return today }}
		// Errors are allowed (a name the OS refuses fails the pass); writes outside the root are not.
		synced := e.Choose(context.Background(), []string{ws}) == nil && e.Sync(context.Background()) == nil

		filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if p == root {
				return filepath.SkipDir
			}
			if !strings.HasPrefix(root, p+string(filepath.Separator)) {
				t.Errorf("names %q / %q / %q wrote %s outside the sync root", wsName, folderName, fileName, p)
			}
			return nil
		})

		if !synced || !plain(wsName) || !plain(folderName) || !plain(fileName) {
			return
		}
		ws0, err := db.Workspaces()
		if err != nil || len(ws0) != 1 {
			t.Fatalf("workspaces %v, %v", ws0, err)
		}
		got, err := os.ReadFile(filepath.Join(root, ws0[0].Dir, folderName, fileName))
		if err != nil || string(got) != "payload" {
			t.Errorf("%s/%s in %q: %q, %v; want the server's bytes", folderName, fileName, ws0[0].Dir, got, err)
		}
	})
}

// plain reports a name every platform holds as written and the engine syncs, so its file
// must arrive.
func plain(name string) bool {
	if name == "" || len(name) > 64 || name == Unsynced || strings.HasPrefix(name, ".") || ignored(name) || shortName(name) {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return windowsName(name)
}

// inMemory serves requests straight from the fake's handler. Over TCP, tens of thousands of
// fuzz inputs each with fresh connections run the machine out of ephemeral ports.
type inMemory struct{ h http.Handler }

func (m inMemory) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		req = req.Clone(req.Context()) // a RoundTripper must not change the caller's request
		req.Body = http.NoBody
	}
	rec := httptest.NewRecorder()
	m.h.ServeHTTP(rec, req)
	return rec.Result(), nil
}
