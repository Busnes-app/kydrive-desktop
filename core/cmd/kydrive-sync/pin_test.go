package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

// pinFixture is a TLS server for 127.0.0.1 with an Ed25519 identity key that signs its leaf.
type pinFixture struct {
	srv   *httptest.Server
	roots *x509.CertPool
	leaf  string // the served leaf's pin
	id    ed25519.PrivateKey

	mu    sync.Mutex
	paths []string
	onPin func() // runs inside GET /api/devices/tls-pin, before it answers
}

func newPinFixture(t *testing.T) *pinFixture {
	t.Helper()
	cert, roots := tlsFixture(t, tlsKeyCase{leaf: "P256"})
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	_, id, _ := ed25519.GenerateKey(rand.Reader)
	f := &pinFixture{roots: roots, leaf: api.SPKIPin(leaf), id: id}
	idKey := base64.StdEncoding.EncodeToString(id.Public().(ed25519.PublicKey))
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		onPin := f.onPin
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/devices/tls-pin":
			if onPin != nil {
				onPin()
			}
			at := time.Now().Unix()
			msg := "kydrive-tls-pin/1\n127.0.0.1\n" + f.leaf + "\n" + strconv.FormatInt(at, 10)
			json.NewEncoder(w).Encode(map[string]any{"host": "127.0.0.1", "pin": f.leaf, "issued_at": at, "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(id, []byte(msg)))})
		case "/api/devices/token":
			json.NewEncoder(w).Encode(map[string]string{"token": "kyd_PINNED", "user_id": "u1", "user": "alice", "identity_key": idKey})
		case "/api/drive/device":
			json.NewEncoder(w).Encode(map[string]string{"user_id": "u1", "user": "alice", "identity_key": idKey})
		default:
			io.WriteString(w, "[]")
		}
	}))
	f.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *pinFixture) idKey() string {
	return base64.StdEncoding.EncodeToString(f.id.Public().(ed25519.PublicKey))
}

func (f *pinFixture) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.paths
	f.paths = nil
	return p
}

// linked sets d up as a computer linked to f holding pin.
func linked(t *testing.T, f *pinFixture, pin api.Pin) *daemon {
	t.Helper()
	keyring.MockInit()
	d := tlsDaemon(t, f.roots)
	if err := d.db.Set("server", f.srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := d.db.Set("user_id", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := storeCredential(f.srv.URL, "kyd_PINNED", nil, pin); err != nil {
		t.Fatal(err)
	}
	d.token, d.pin, d.state = "kyd_PINNED", pin, "idle"
	return d
}

const retiredLeaf = "sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// A signed renewal is saved in the keyring beside the token.
func TestDaemonSavesRolledPin(t *testing.T) {
	f := newPinFixture(t)
	d := linked(t, f, api.Pin{Leaf: retiredLeaf, IdentityKey: f.idKey(), Floor: time.Now().Unix() - 3600})
	d.pass(context.Background(), false)
	if d.state != "idle" {
		t.Fatalf("state %q: %s", d.state, d.lastErr)
	}
	tok, _, pin, err := loadCredential(f.srv.URL)
	if err != nil || tok != "kyd_PINNED" || pin.Leaf != f.leaf || pin.IdentityKey != f.idKey() || pin != d.pin {
		t.Fatalf("keyring: %q %+v %v; daemon %+v", tok, pin, err, d.pin)
	}
}

// With no identity key a changed leaf signs the computer out, keeping its credential and
// bindings, and nothing reaches the server.
func TestDaemonPinChangeWithoutIdentitySignsOut(t *testing.T) {
	f := newPinFixture(t)
	pin := api.Pin{Leaf: retiredLeaf, Floor: 1}
	d := linked(t, f, pin)
	binding := engine.Workspace{ID: "w", Name: "Docs", Dir: "Docs", Cursor: 3}
	if err := d.db.PutWorkspace(binding); err != nil {
		t.Fatal(err)
	}
	d.pass(context.Background(), false)
	if d.state != "signed_out" || !strings.Contains(d.lastErr, "sign in again") {
		t.Fatalf("state %q: %s", d.state, d.lastErr)
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("requests reached the server: %v", got)
	}
	if log, _ := d.db.Activity(0); len(log) == 0 || !strings.Contains(log[0].Message, "certificate changed") {
		t.Fatalf("activity %v", log)
	}
	tok, _, kept, err := loadCredential(f.srv.URL)
	ws, _ := d.db.Workspaces()
	if err != nil || tok != "kyd_PINNED" || kept != pin || len(ws) != 1 || ws[0] != binding {
		t.Fatalf("credential %q %+v %v, bindings %v", tok, kept, err, ws)
	}
}

// An unlink while the signed pin is being fetched is not undone by saving the new pin.
func TestDaemonUnlinkDuringRolloverNotUndone(t *testing.T) {
	f := newPinFixture(t)
	d := linked(t, f, api.Pin{Leaf: retiredLeaf, IdentityKey: f.idKey(), Floor: 1})
	f.onPin = func() { d.mu.Lock(); d.forget(); d.mu.Unlock() }
	e := d.engine()
	if _, err := e.API.Workspaces(context.Background()); !errors.Is(err, api.ErrPinChanged) {
		t.Fatalf("err = %v", err)
	}
	if _, err := keyring.Get(keyringService, f.srv.URL); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring item came back: %v", err)
	}
	if d.pin != (api.Pin{}) || d.token != "" {
		t.Fatalf("session came back: %q %+v", d.token, d.pin)
	}
	// The old session's client now sends nothing.
	f.took()
	if _, err := e.API.Workspaces(context.Background()); err == nil {
		t.Fatal("an unlinked session sent a request")
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("requests %v", got)
	}
}

// Linking over TLS stores the leaf it met and the identity key in the keyring item.
func TestLinkStoresPinAndIdentityKey(t *testing.T) {
	f := newPinFixture(t)
	d, ctl := start(t, f.srv)
	d.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: f.roots}
	d.open = func(raw string) error {
		u, _ := url.Parse(raw)
		q := u.Query()
		go http.Get(q.Get("redirect") + "?code=c1&state=" + url.QueryEscape(q.Get("state")))
		return nil
	}
	before := time.Now().Unix()
	var out struct{}
	if err := ctl("Link", map[string]string{"Server": f.srv.URL, "Name": "laptop"}, &out); err != nil {
		t.Fatal(err)
	}
	tok, key, pin, err := loadCredential(f.srv.URL)
	if err != nil || tok != "kyd_PINNED" || key == nil || pin.Leaf != f.leaf || pin.IdentityKey != f.idKey() || pin.Floor < before {
		t.Fatalf("keyring: %q key=%v %+v %v", tok, key != nil, pin, err)
	}
}

func TestCredentialFormatWithPin(t *testing.T) {
	keyring.MockInit()
	pin := api.Pin{Leaf: retiredLeaf, Floor: 1794844800}
	for _, p := range []api.Pin{pin, {Leaf: retiredLeaf, IdentityKey: "11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=", Floor: 7}} {
		if err := storeCredential("https://a", "kyd_A", nil, p); err != nil {
			t.Fatal(err)
		}
		tok, key, got, err := loadCredential("https://a")
		if err != nil || tok != "kyd_A" || key != nil || got != p {
			t.Fatalf("round trip %+v: %q %v %+v %v", p, tok, key, got, err)
		}
	}
	for _, bad := range []string{"kyd_X - sha256/x - notanumber", "kyd_X - nopin - 1", "kyd_X - sha256/x", ""} {
		keyring.Set(keyringService, "https://bad", bad)
		if _, _, _, err := loadCredential("https://bad"); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// A re-link during the fetch belongs to another session: the old one's rollover leaves it alone,
// even when the new session happens to hold the same pin.
func TestDaemonRelinkDuringRolloverNotTouched(t *testing.T) {
	f := newPinFixture(t)
	pin := api.Pin{Leaf: retiredLeaf, IdentityKey: f.idKey(), Floor: 1}
	d := linked(t, f, pin)
	f.onPin = func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.forget()
		d.token, d.pin = "kyd_NEW", pin
		storeCredential(f.srv.URL, "kyd_NEW", nil, pin)
	}
	if _, err := d.engine().API.Workspaces(context.Background()); !errors.Is(err, api.ErrPinChanged) {
		t.Fatalf("err = %v", err)
	}
	tok, _, got, err := loadCredential(f.srv.URL)
	if err != nil || tok != "kyd_NEW" || got != pin || d.pin != pin {
		t.Fatalf("new session changed: %q %+v %v", tok, got, err)
	}
}
