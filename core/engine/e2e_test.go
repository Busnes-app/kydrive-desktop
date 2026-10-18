package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/link"
)

// With KYDRIVE_SERVER_BIN set, every engine test that uses setup runs against a real
// kydrive-server: one process per test, a fresh SQLite data directory, an admin session for the
// server-side actions and a device token linked through link.Link.
func init() {
	if bin := os.Getenv("KYDRIVE_SERVER_BIN"); bin != "" {
		newRemote = func(t *testing.T) remote { return startServer(t, bin) }
	}
}

type real struct {
	base, group, token string
	web                *http.Client // the admin's browser session
}

const scimToken = "e2e-scim-token-0123456789abcdef"

func startServer(t *testing.T, bin string) *real {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()
	dir := t.TempDir()
	logf, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "KY_ENV=development", "KY_CAPTCHA_PROVIDER=none", "KY_DB_DRIVER=sqlite",
		"KY_DATA_DIR="+filepath.Join(dir, "data"), "KY_BACKUP_DIR="+filepath.Join(dir, "backups"),
		"KY_HOST=127.0.0.1", "KY_PORT="+port, "KY_APP_URL=http://127.0.0.1:"+port,
		"KY_ADMIN_PASSWORD=E2eBootstrapPass123!", "KY_SCIM_ENABLED=true", "KY_SCIM_TOKEN="+scimToken)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		logf.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			t.Logf("server log:\n%s", b)
		}
	})
	jar, _ := cookiejar.New(nil)
	r := &real{base: "http://127.0.0.1:" + port, web: &http.Client{Jar: jar, Timeout: time.Minute}}
	for i := 0; ; i++ {
		res, err := http.Get(r.base + "/healthz")
		if err == nil && res.StatusCode == 200 {
			res.Body.Close()
			break
		}
		if i == 100 {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The bootstrap password must be replaced before the session is useful.
	r.must(t, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "E2eBootstrapPass123!"}, nil)
	r.must(t, "POST", "/api/auth/change-password", map[string]string{"current_password": "E2eBootstrapPass123!", "new_password": "E2eReplacementPass456!"}, nil)
	r.must(t, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "E2eReplacementPass456!"}, nil)
	var me struct{ User struct{ ID string } }
	r.must(t, "GET", "/api/auth/me", nil, &me)

	// One directory group holding the admin carries every workspace grant.
	body, _ := json.Marshal(map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "sync", "members": []map[string]string{{"value": me.User.ID}}})
	req, _ := http.NewRequest("POST", r.base+"/scim/v2/Groups", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+scimToken)
	req.Header.Set("Content-Type", "application/scim+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 201 {
		t.Fatalf("SCIM group: %v %v", res, err)
	}
	var g struct{ ID string }
	json.NewDecoder(res.Body).Decode(&g)
	res.Body.Close()
	r.group = g.ID

	// Link a device the way a user would, with this session approving in the "browser".
	approve := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil || u.Path != "/link" {
			return fmt.Errorf("bad link URL %s", raw)
		}
		q := u.Query()
		var out struct{ Redirect string }
		if err := r.call("POST", "/api/devices/approve", map[string]string{"redirect": q.Get("redirect"), "challenge": q.Get("challenge"), "state": q.Get("state"), "name": q.Get("name"), "platform": q.Get("platform")}, &out); err != nil {
			return err
		}
		go http.Get(out.Redirect)
		return nil
	}
	tok, err := link.Link(context.Background(), link.Request{Server: r.base, Name: "laptop", Platform: "linux", Open: approve, HTTP: http.DefaultClient})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	r.token = tok.Token
	return r
}

func (r *real) csrf() string {
	u, _ := url.Parse(r.base)
	for _, c := range r.web.Jar.Cookies(u) {
		if c.Name == "ky_csrf" {
			return c.Value
		}
	}
	return ""
}

// call sends a JSON request with the admin's session and CSRF token.
func (r *real) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	return r.raw(method, path, body, "application/json", out)
}

func (r *real) raw(method, path string, body io.Reader, ctype string, out any) error {
	req, _ := http.NewRequest(method, r.base+path, body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("X-CSRF-Token", r.csrf())
	res, err := r.web.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, b)
	}
	if out == nil {
		return nil
	}
	if p, ok := out.(*[]byte); ok {
		*p = b
		return nil
	}
	return json.Unmarshal(b, out)
}

func (r *real) must(t *testing.T, method, path string, in, out any) {
	t.Helper()
	if err := r.call(method, path, in, out); err != nil {
		t.Fatal(err)
	}
}

func (r *real) URL() string   { return r.base }
func (r *real) Token() string { return r.token }

func (r *real) NewWorkspace(t *testing.T, name string) string {
	var w struct{ ID string }
	r.must(t, "POST", "/api/drive/workspaces", map[string]any{"name": name, "quota": 1 << 30}, &w)
	r.must(t, "PUT", "/api/drive/workspaces/"+w.ID+"/grants/"+r.group, map[string]string{"role": "editor"}, nil)
	return w.ID
}

type rnode struct {
	ID, Parent, Name string
	Revision         int64
	folder           bool
}

// tree reads the workspace through the snapshot endpoint, keyed by slash path.
func (r *real) tree(t *testing.T, ws string) map[string]rnode {
	t.Helper()
	byID := map[string]rnode{}
	for after := ""; ; {
		var s struct {
			Folders []rnode
			Files   []rnode
			Next    string
		}
		r.must(t, "GET", "/api/drive/workspaces/"+ws+"/snapshot?after="+url.QueryEscape(after), nil, &s)
		for _, f := range s.Folders {
			f.folder = true
			byID[f.ID] = f
		}
		for _, f := range s.Files {
			byID[f.ID] = f
		}
		if s.Next == "" {
			break
		}
		after = s.Next
	}
	out := map[string]rnode{}
	for _, n := range byID {
		p := n.Name
		for d, ok := byID[n.Parent]; ok; d, ok = byID[d.Parent] {
			p = d.Name + "/" + p
		}
		out[p] = n
	}
	return out
}

func (r *real) Put(t *testing.T, ws, p string, data []byte) {
	t.Helper()
	tree := r.tree(t, ws)
	parent := ""
	parts := strings.Split(p, "/")
	for i := range parts[:len(parts)-1] {
		dir := strings.Join(parts[:i+1], "/")
		d, ok := tree[dir]
		if !ok {
			var f struct{ ID string }
			r.must(t, "POST", "/api/drive/workspaces/"+ws+"/folders", map[string]string{"parent": parent, "name": parts[i]}, &f)
			d = rnode{ID: f.ID, folder: true}
			tree[dir] = d
		}
		parent = d.ID
	}
	q := url.Values{"parent": {parent}, "name": {parts[len(parts)-1]}, "revision": {"0"}, "mtime": {"2026-01-02T03:04:05Z"}}
	if f, ok := tree[p]; ok {
		q.Set("file", f.ID)
		q.Set("revision", fmt.Sprint(f.Revision))
	}
	if err := r.raw("POST", "/api/drive/workspaces/"+ws+"/uploads?"+q.Encode(), bytes.NewReader(data), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
}

func (r *real) Read(t *testing.T, ws, p string) ([]byte, bool) {
	f, ok := r.tree(t, ws)[p]
	if !ok || f.folder {
		return nil, false
	}
	var b []byte
	if err := r.raw("GET", "/api/drive/files/"+f.ID+"/download", nil, "", &b); err != nil {
		t.Fatal(err)
	}
	return b, true
}

func (r *real) ID(t *testing.T, ws, p string) string { return r.tree(t, ws)[p].ID }

func (r *real) Paths(t *testing.T, ws string) []string {
	var out []string
	for p, n := range r.tree(t, ws) {
		if !n.folder {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

func (r *real) Remove(t *testing.T, ws, p string) {
	n := r.tree(t, ws)[p]
	if n.folder {
		r.must(t, "PUT", "/api/drive/folders/"+n.ID+"/trash", map[string]bool{"trashed": true}, nil)
		return
	}
	r.must(t, "PUT", "/api/drive/files/"+n.ID+"/trash", map[string]any{"revision": n.Revision, "trashed": true}, nil)
}

func (r *real) Rename(t *testing.T, ws, p, name string) {
	n := r.tree(t, ws)[p]
	if n.folder {
		r.must(t, "PUT", "/api/drive/folders/"+n.ID+"/location", map[string]string{"parent": n.Parent, "name": name}, nil)
		return
	}
	r.must(t, "PUT", "/api/drive/files/"+n.ID+"/location", map[string]any{"revision": n.Revision, "workspace": ws, "parent": n.Parent, "name": name}, nil)
}

func (r *real) DropAccess(t *testing.T, ws string) {
	r.must(t, "PUT", "/api/drive/workspaces/"+ws+"/grants/"+r.group, map[string]string{"role": ""}, nil)
}

func (r *real) Revoke(t *testing.T, wipe bool) {
	var devices []struct{ ID string }
	r.must(t, "GET", "/api/devices", nil, &devices)
	if len(devices) != 1 {
		t.Fatalf("devices: %+v", devices)
	}
	var out struct{ Wipe bool }
	r.must(t, "DELETE", fmt.Sprintf("/api/devices/%s?wipe=%v", devices[0].ID, wipe), nil, &out)
	if out.Wipe != wipe {
		t.Fatalf("server set wipe=%v", out.Wipe)
	}
}
