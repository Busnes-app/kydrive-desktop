package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is an in-process KyDrive with the REST surface the engine uses, modelled on the M7
// plan. Tests run against it unless KYDRIVE_SERVER_BIN names a real server binary.
type fake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	names    map[string]string // workspace id -> name
	access   map[string]bool
	items    map[string]*fitem
	log      []fchange // seq is index+1
	floor    int64     // a cursor below this gets 410
	sessions map[string]*fsession
	n        int
	revoked  bool
	wipe     bool
	corrupt  bool // serve bytes that do not match the digest
}

type fitem struct {
	id, ws, kind, parent, name string
	rev                        int64
	data                       []byte
	mtime                      string
	trashed                    bool
}

type fchange struct{ ws, kind, id string }

type fsession struct {
	ws, parent, name, file string
	rev, size              int64
	mtime                  string
	data                   []byte
}

func newFake(t *testing.T) *fake {
	f := &fake{names: map[string]string{}, access: map[string]bool{}, items: map[string]*fitem{}, sessions: map[string]*fsession{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/drive/workspaces", f.workspaces)
	mux.HandleFunc("GET /api/drive/workspaces/{id}/changes", f.changes)
	mux.HandleFunc("GET /api/drive/workspaces/{id}/snapshot", f.snapshot)
	mux.HandleFunc("GET /api/drive/files/{id}/download", f.download)
	mux.HandleFunc("POST /api/drive/workspaces/{id}/folders", f.addFolder)
	mux.HandleFunc("POST /api/drive/workspaces/{id}/upload-sessions", f.startUpload)
	mux.HandleFunc("GET /api/drive/upload-sessions/{sid}", f.uploadStatus)
	mux.HandleFunc("PUT /api/drive/upload-sessions/{sid}", f.appendUpload)
	mux.HandleFunc("POST /api/drive/upload-sessions/{sid}/complete", f.completeUpload)
	mux.HandleFunc("PUT /api/drive/files/{id}/location", f.moveFile)
	mux.HandleFunc("PUT /api/drive/folders/{id}/location", f.moveFolder)
	mux.HandleFunc("PUT /api/drive/files/{id}/trash", f.trash)
	mux.HandleFunc("PUT /api/drive/folders/{id}/trash", f.trash)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case f.revoked:
			reply(w, 401, map[string]any{"error": "device_revoked", "wipe": f.wipe})
		case r.Header.Get("Authorization") != "Bearer kyd_test":
			reply(w, 401, map[string]string{"error": "Authentication required"})
		default:
			mux.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (f *fake) id() string { f.n++; return fmt.Sprintf("id%04d", f.n) }

func (f *fake) record(ws, kind, id string) { f.log = append(f.log, fchange{ws, kind, id}) }

func (f *fake) head(ws string) int64 {
	for i := len(f.log) - 1; i >= 0; i-- {
		if f.log[i].ws == ws {
			return int64(i + 1)
		}
	}
	return 0
}

// live reports whether it and every ancestor are untrashed.
func (f *fake) live(it *fitem) bool {
	for ; it != nil; it = f.items[it.parent] {
		if it.trashed {
			return false
		}
	}
	return true
}

func (f *fake) taken(ws, parent, name, except string) bool {
	for _, it := range f.items {
		if it.ws == ws && it.parent == parent && it.name == name && it.id != except && f.live(it) {
			return true
		}
	}
	return false
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func (f *fake) view(it *fitem) map[string]any {
	if it.kind == "folder" {
		return map[string]any{"id": it.id, "workspace": it.ws, "parent": it.parent, "name": it.name}
	}
	return map[string]any{"id": it.id, "workspace": it.ws, "parent": it.parent, "name": it.name, "revision": it.rev, "size": len(it.data), "digest": digest(it.data), "mtime": it.mtime}
}

func (f *fake) readable(w http.ResponseWriter, ws string) bool {
	if !f.access[ws] {
		reply(w, 403, map[string]string{"error": "Document access denied"})
	}
	return f.access[ws]
}

func (f *fake) workspaces(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for id, name := range f.names {
		if f.access[id] {
			out = append(out, map[string]any{"id": id, "name": name, "kind": "shared", "role": "editor", "head": f.head(id)})
		}
	}
	reply(w, 200, out)
}

func (f *fake) changes(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("id")
	if !f.readable(w, ws) {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after < f.floor {
		reply(w, 410, map[string]string{"error": "Gone"})
		return
	}
	out, seen, next := []map[string]any{}, map[string]bool{}, after
	for seq := after + 1; seq <= int64(len(f.log)); seq++ {
		c := f.log[seq-1]
		if c.ws != ws {
			continue
		}
		next = seq
		if seen[c.id] {
			continue
		}
		seen[c.id] = true
		it := f.items[c.id]
		ch := map[string]any{"kind": c.kind, "id": c.id, "removed": it.ws != ws || !f.live(it)}
		if !ch["removed"].(bool) {
			ch[c.kind] = f.view(it)
		}
		out = append(out, ch)
	}
	reply(w, 200, map[string]any{"changes": out, "next": next, "more": false})
}

func (f *fake) snapshot(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("id")
	if !f.readable(w, ws) {
		return
	}
	folders, files := []map[string]any{}, []map[string]any{}
	ids := []string{}
	for id, it := range f.items {
		if it.ws == ws && f.live(it) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	// Two items per page, so the client must follow next.
	after, next := r.URL.Query().Get("after"), ""
	for _, id := range ids {
		if id <= after {
			continue
		}
		if len(folders)+len(files) == 2 {
			break
		}
		next = id
		if f.items[id].kind == "folder" {
			folders = append(folders, f.view(f.items[id]))
		} else {
			files = append(files, f.view(f.items[id]))
		}
	}
	if len(ids) == 0 || next == ids[len(ids)-1] {
		next = ""
	}
	reply(w, 200, map[string]any{"folders": folders, "files": files, "head": f.head(ws), "next": next})
}

func (f *fake) file(w http.ResponseWriter, id string) *fitem {
	it := f.items[id]
	if it == nil || !f.live(it) || !f.access[it.ws] {
		reply(w, 403, map[string]string{"error": "Document access denied"})
		return nil
	}
	return it
}

func (f *fake) download(w http.ResponseWriter, r *http.Request) {
	if it := f.file(w, r.PathValue("id")); it != nil {
		if f.corrupt {
			w.Write(append([]byte("x"), it.data[1:]...))
			return
		}
		w.Write(it.data)
	}
}

func decode(r *http.Request, v any) { json.NewDecoder(r.Body).Decode(v) }

func (f *fake) addFolder(w http.ResponseWriter, r *http.Request) {
	var in struct{ Parent, Name string }
	decode(r, &in)
	ws := r.PathValue("id")
	if !f.readable(w, ws) {
		return
	}
	if f.taken(ws, in.Parent, in.Name, "") {
		reply(w, 409, map[string]string{"error": "name exists"})
		return
	}
	it := &fitem{id: f.id(), ws: ws, kind: "folder", parent: in.Parent, name: in.Name}
	f.items[it.id] = it
	f.record(ws, "folder", it.id)
	reply(w, 201, f.view(it))
}

func (f *fake) startUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Parent, Name, File, Mtime string
		Revision, Size            int64
	}
	decode(r, &in)
	ws := r.PathValue("id")
	if !f.readable(w, ws) {
		return
	}
	if _, err := time.Parse(time.RFC3339, in.Mtime); err != nil {
		reply(w, 400, map[string]string{"error": "Invalid drive input"})
		return
	}
	if (in.File != "" && (f.items[in.File] == nil || f.items[in.File].rev != in.Revision)) || (in.File == "" && f.taken(ws, in.Parent, in.Name, "")) {
		reply(w, 409, map[string]string{"error": "revision conflict"})
		return
	}
	sid := f.id()
	f.sessions[sid] = &fsession{ws: ws, parent: in.Parent, name: in.Name, file: in.File, rev: in.Revision, size: in.Size, mtime: in.Mtime}
	reply(w, 201, map[string]any{"id": sid, "size": in.Size, "received": 0})
}

func (f *fake) uploadStatus(w http.ResponseWriter, r *http.Request) {
	s := f.sessions[r.PathValue("sid")]
	if s == nil {
		reply(w, 404, map[string]string{"error": "Not found"})
		return
	}
	reply(w, 200, map[string]any{"id": r.PathValue("sid"), "size": s.size, "received": len(s.data)})
}

func (f *fake) appendUpload(w http.ResponseWriter, r *http.Request) {
	s := f.sessions[r.PathValue("sid")]
	var start, end, total int64
	fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total)
	if s == nil || start != int64(len(s.data)) || total != s.size {
		reply(w, 409, map[string]string{"error": "offset"})
		return
	}
	b, _ := io.ReadAll(r.Body)
	s.data = append(s.data, b...)
	w.WriteHeader(204)
}

func (f *fake) completeUpload(w http.ResponseWriter, r *http.Request) {
	s := f.sessions[r.PathValue("sid")]
	if s == nil || int64(len(s.data)) != s.size {
		reply(w, 409, map[string]string{"error": "incomplete"})
		return
	}
	delete(f.sessions, r.PathValue("sid"))
	it := f.items[s.file]
	switch {
	case s.file != "" && (it == nil || it.rev != s.rev || !f.live(it)):
		reply(w, 409, map[string]string{"error": "revision conflict"})
		return
	case s.file == "" && f.taken(s.ws, s.parent, s.name, ""):
		reply(w, 409, map[string]string{"error": "name or file exists"})
		return
	case s.file == "":
		it = &fitem{id: f.id(), ws: s.ws, kind: "file", parent: s.parent, name: s.name}
		f.items[it.id] = it
	}
	it.rev++
	it.data, it.mtime = s.data, s.mtime
	f.record(it.ws, "file", it.id)
	reply(w, 201, f.view(it))
}

func (f *fake) moveFile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision                int64
		Workspace, Parent, Name string
	}
	decode(r, &in)
	it := f.file(w, r.PathValue("id"))
	if it == nil {
		return
	}
	if it.rev != in.Revision || f.taken(in.Workspace, in.Parent, in.Name, it.id) {
		reply(w, 409, map[string]string{"error": "revision conflict"})
		return
	}
	from := it.ws
	it.ws, it.parent, it.name = in.Workspace, in.Parent, in.Name
	f.record(from, "file", it.id)
	if from != it.ws {
		f.record(it.ws, "file", it.id)
	}
	reply(w, 200, f.view(it))
}

func (f *fake) moveFolder(w http.ResponseWriter, r *http.Request) {
	var in struct{ Parent, Name string }
	decode(r, &in)
	it := f.file(w, r.PathValue("id"))
	if it == nil {
		return
	}
	if f.taken(it.ws, in.Parent, in.Name, it.id) {
		reply(w, 409, map[string]string{"error": "name exists"})
		return
	}
	it.parent, it.name = in.Parent, in.Name
	f.record(it.ws, "folder", it.id)
	reply(w, 200, f.view(it))
}

func (f *fake) trash(w http.ResponseWriter, r *http.Request) {
	var in struct{ Revision int64 }
	decode(r, &in)
	it := f.file(w, r.PathValue("id"))
	if it == nil {
		return
	}
	if it.kind == "file" && it.rev != in.Revision {
		reply(w, 409, map[string]string{"error": "revision conflict"})
		return
	}
	it.trashed = true
	f.record(it.ws, it.kind, it.id)
	reply(w, 200, map[string]bool{"applied": true})
}

// remote implementation, for the tests.

func (f *fake) URL() string   { return f.srv.URL }
func (f *fake) Token() string { return "kyd_test" }

func (f *fake) NewWorkspace(t *testing.T, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.id()
	f.names[id], f.access[id] = name, true
	return id
}

// find resolves a slash path to a live item.
func (f *fake) find(ws, path string) *fitem {
	parent := ""
	var cur *fitem
	for _, name := range strings.Split(path, "/") {
		cur = nil
		for _, it := range f.items {
			if it.ws == ws && it.parent == parent && it.name == name && f.live(it) {
				cur = it
			}
		}
		if cur == nil {
			return nil
		}
		parent = cur.id
	}
	return cur
}

func (f *fake) Put(t *testing.T, ws, path string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(path, "/")
	parent := ""
	for i := range parts[:len(parts)-1] {
		d := f.find(ws, strings.Join(parts[:i+1], "/"))
		if d == nil {
			d = &fitem{id: f.id(), ws: ws, kind: "folder", parent: parent, name: parts[i]}
			f.items[d.id] = d
			f.record(ws, "folder", d.id)
		}
		parent = d.id
	}
	it := f.find(ws, path)
	if it == nil {
		it = &fitem{id: f.id(), ws: ws, kind: "file", parent: parent, name: parts[len(parts)-1]}
		f.items[it.id] = it
	}
	it.rev++
	it.data, it.mtime = data, "2026-01-02T03:04:05Z"
	f.record(ws, "file", it.id)
}

func (f *fake) Read(t *testing.T, ws, path string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it := f.find(ws, path)
	if it == nil || it.kind != "file" {
		return nil, false
	}
	return it.data, true
}

func (f *fake) ID(t *testing.T, ws, path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if it := f.find(ws, path); it != nil {
		return it.id
	}
	return ""
}

func (f *fake) Paths(t *testing.T, ws string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, it := range f.items {
		if it.ws == ws && it.kind == "file" && f.live(it) {
			p := it.name
			for d := f.items[it.parent]; d != nil; d = f.items[d.parent] {
				p = d.name + "/" + p
			}
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

func (f *fake) Remove(t *testing.T, ws, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it := f.find(ws, path)
	it.trashed = true
	f.record(ws, it.kind, it.id)
}

func (f *fake) Rename(t *testing.T, ws, path, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it := f.find(ws, path)
	it.name = name
	f.record(ws, it.kind, it.id)
}

func (f *fake) DropAccess(t *testing.T, ws string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access[ws] = false
}

func (f *fake) Revoke(t *testing.T, wipe bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked, f.wipe = true, wipe
}
