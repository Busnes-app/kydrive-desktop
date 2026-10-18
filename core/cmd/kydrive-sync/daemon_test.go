package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"
)

// start runs a daemon against srv in a temp home and returns it with a ctl caller.
func start(t *testing.T, srv *httptest.Server) (*daemon, func(method string, params, out any) error) {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	// macOS caps socket paths at 104 bytes; t.TempDir names can exceed that.
	run, err := os.MkdirTemp("", "kyd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	d, err := newDaemon(filepath.Join(dir, "config"), filepath.Join(dir, "KyDrive"), filepath.Join(run, "run", "sync.sock"))
	if err != nil {
		t.Fatal(err)
	}
	d.open = func(raw string) error {
		u, _ := url.Parse(raw)
		q := u.Query()
		go http.Get(q.Get("redirect") + "?code=c1&state=" + url.QueryEscape(q.Get("state")))
		return nil
	}
	ln, err := d.listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.serve(ctx, ln)
	t.Cleanup(func() { cancel(); ln.Close(); d.db.Close() })
	return d, func(method string, params, out any) error {
		b, _ := json.Marshal(params)
		var raw json.RawMessage
		if err := call(d.sock, method, b, &raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
}

// selfRevoked counts DELETE /api/devices/self calls.
var selfRevoked atomic.Int32

// server answers the token exchange, self-revocation and a one-workspace list.
func server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/devices/token":
			sum := sha256.Sum256([]byte(r.FormValue("verifier")))
			if r.FormValue("code") != "c1" || len(base64.RawURLEncoding.EncodeToString(sum[:])) != 43 {
				http.Error(w, "{}", 400)
				return
			}
			io.WriteString(w, `{"token":"kyd_SECRETTOKEN","devices_url":"`+"http://"+r.Host+`/account"}`)
		case "/api/devices/self":
			if r.Method != "DELETE" || r.Header.Get("Authorization") != "Bearer kyd_SECRETTOKEN" {
				http.Error(w, "{}", 401)
				return
			}
			selfRevoked.Add(1)
			w.WriteHeader(204)
		case "/api/drive/workspaces":
			if r.Header.Get("Authorization") != "Bearer kyd_SECRETTOKEN" {
				http.Error(w, `{"error":"Authentication required"}`, 401)
				return
			}
			io.WriteString(w, `[{"id":"w1","name":"Docs","kind":"shared","role":"editor","head":0}]`)
		default:
			http.Error(w, "{}", 404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSocketIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits; Windows relies on the per-user profile ACL")
	}
	d, _ := start(t, server(t))
	for _, p := range []string{d.sock, filepath.Dir(d.sock)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v", p, fi.Mode().Perm())
		}
	}
}

// TestTokenOnlyInKeyring links through the daemon and checks the token reached the keyring
// and no file under the config directory, sync root or socket directory.
func TestTokenOnlyInKeyring(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	var out struct{}
	if err := ctl("Link", map[string]string{"Server": srv.URL, "Name": "laptop"}, &out); err != nil {
		t.Fatal(err)
	}
	if tok, err := keyring.Get(keyringService, srv.URL); err != nil || tok != "kyd_SECRETTOKEN" {
		t.Fatalf("keyring holds %q, %v", tok, err)
	}
	var st Status
	if err := ctl("Status", struct{}{}, &st); err != nil || !st.Linked || st.Server != srv.URL {
		t.Fatalf("status %+v, %v", st, err)
	}
	d.db.Close()
	for _, dir := range []string{d.dir, d.root, filepath.Dir(d.sock)} {
		filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err == nil && e.Type().IsRegular() {
				if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("SECRETTOKEN")) {
					t.Errorf("token written to %s", p)
				}
			}
			return nil
		})
	}
}

func TestLinkRefusesPlainHTTP(t *testing.T) {
	_, ctl := start(t, server(t))
	var out struct{}
	err := ctl("Link", map[string]string{"Server": "http://drive.example.com"}, &out)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("got %v", err)
	}
}

func TestUnlinkDeletesKeyringItem(t *testing.T) {
	selfRevoked.Store(0)
	srv := server(t)
	_, ctl := start(t, srv)
	var out struct{}
	if err := ctl("Link", map[string]string{"Server": srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if err := ctl("Unlink", map[string]bool{"RemoveFiles": false}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, srv.URL); err != keyring.ErrNotFound {
		t.Fatalf("keyring item survived unlink: %v", err)
	}
	if selfRevoked.Load() != 1 {
		t.Fatalf("unlink revoked the device %d times", selfRevoked.Load())
	}
	var st Status
	ctl("Status", struct{}{}, &st)
	if st.Linked {
		t.Fatal("still linked")
	}
}

func TestPauseAndWorkspaces(t *testing.T) {
	srv := server(t)
	_, ctl := start(t, srv)
	var out struct{}
	ctl("Link", map[string]string{"Server": srv.URL}, &out)
	var ws []Choice
	if err := ctl("Workspaces", struct{}{}, &ws); err != nil || len(ws) != 1 || ws[0].Name != "Docs" || ws[0].Chosen {
		t.Fatalf("workspaces %+v, %v", ws, err)
	}
	if err := ctl("Pause", map[string]bool{"Paused": true}, &out); err != nil {
		t.Fatal(err)
	}
	var st Status
	ctl("Status", struct{}{}, &st)
	if !st.Paused {
		t.Fatal("not paused")
	}
}

func TestSecondDaemonRefused(t *testing.T) {
	d, _ := start(t, server(t))
	other, err := newDaemon(d.dir, d.root, d.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer other.db.Close()
	if ln, err := other.listen(); err == nil {
		ln.Close()
		t.Fatal("a second daemon took over the socket")
	}
}

func TestPlatformAndDeviceName(t *testing.T) {
	if p := platform(); p != "windows" && p != "macos" && p != "linux" {
		t.Fatalf("platform %q is not one the server accepts", p)
	}
	for in, want := range map[string]string{
		"laptop":                "laptop",
		"  a/b\\c\x00d  ":       "a-b-cd",
		strings.Repeat("é", 80): strings.Repeat("é", 50),
		"":                      "computer",
	} {
		if got := deviceName(in); got != want {
			t.Errorf("deviceName(%q) = %q, want %q", in, got, want)
		}
	}
}
