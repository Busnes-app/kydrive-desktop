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

	mu        sync.Mutex
	paths     []string
	creds     []string // requests carrying a token, signature or link code verifier
	onPin     func()   // runs inside GET /api/devices/tls-pin, before it answers
	pinStatus int      // nonzero: GET /api/devices/tls-pin answers only this status
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
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "" || r.Header.Get("KyDrive-Signature") != "" || strings.Contains(string(body), "verifier") {
			f.creds = append(f.creds, r.URL.Path)
		}
		onPin, pinStatus := f.onPin, f.pinStatus
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/devices/tls-pin":
			if onPin != nil {
				onPin()
			}
			if pinStatus != 0 {
				w.WriteHeader(pinStatus)
				return
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
	tok, _, pin, err := loadCredential(d.credentialID())
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
	tok, _, kept, err := loadCredential(d.credentialID())
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
	tok, key, pin, err := loadCredential(d.credentialID())
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

func TestRevokedCredentialFormatRetainsOnlyPin(t *testing.T) {
	keyring.MockInit()
	for _, pin := range []api.Pin{{}, {Leaf: retiredLeaf, IdentityKey: "11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=", Floor: 123}} {
		if err := storeCredential("revoked", "", nil, pin); err != nil {
			t.Fatal(err)
		}
		token, key, got, err := loadCredential("revoked")
		if err != nil || token != "" || key != nil || got != pin {
			t.Fatal("revoked credential format revived a token/key or lost the pin")
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
	tok, _, got, err := loadCredential(d.credentialID())
	if err != nil || tok != "kyd_NEW" || got != pin || d.pin != pin {
		t.Fatalf("new session changed: %q %+v %v", tok, got, err)
	}
}

// Sign in again keeps the identity key this computer holds and refuses a reply with another.
func TestSignInKeepsIdentityKey(t *testing.T) {
	f := newPinFixture(t)
	d, ctl := start(t, f.srv)
	d.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: f.roots}
	var out struct{}
	if err := ctl("Link", map[string]string{"Server": f.srv.URL, "Name": "laptop"}, &out); err != nil {
		t.Fatal(err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	held := base64.StdEncoding.EncodeToString(other.Public().(ed25519.PublicKey))
	d.mu.Lock()
	d.state, d.pin.IdentityKey = "signed_out", held
	d.mu.Unlock()
	if err := ctl("SignIn", struct{}{}, &out); err == nil || !strings.Contains(err.Error(), "different identity key") {
		t.Fatalf("sign-in with another identity key: %v", err)
	}
	if d.pin.IdentityKey != held || d.state != "signed_out" {
		t.Fatalf("identity key replaced: %+v %q", d.pin, d.state)
	}
	d.mu.Lock()
	d.pin.IdentityKey = f.idKey()
	d.mu.Unlock()
	if err := ctl("SignIn", struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
	if _, _, pin, err := loadCredential(d.credentialID()); err != nil || pin.IdentityKey != f.idKey() || pin.Leaf != f.leaf {
		t.Fatalf("after sign-in %+v %v", pin, err)
	}
}

func (f *pinFixture) credentials() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creds
}

// pinnedSignedOut links d to f over TLS, then signs it out holding pin.
func pinnedSignedOut(t *testing.T, f *pinFixture, pin api.Pin) (*daemon, func(string, any, any) error, *int) {
	t.Helper()
	d, ctl := start(t, f.srv)
	d.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: f.roots}
	var out struct{}
	if err := ctl("Link", map[string]string{"Server": f.srv.URL, "Name": "laptop"}, &out); err != nil {
		t.Fatal(err)
	}
	approve, opened := d.open, new(int)
	d.open = func(raw string) error { *opened++; return approve(raw) }
	d.mu.Lock()
	d.state, d.pin = "signed_out", pin
	storeCredential(d.credentialID(), d.token, d.key, pin)
	d.mu.Unlock()
	f.mu.Lock()
	f.paths, f.creds = nil, nil
	f.mu.Unlock()
	return d, ctl, opened
}

// Sign in again over the leaf already pinned keeps that pin, floor included.
func TestSignInOverStoredLeaf(t *testing.T) {
	f := newPinFixture(t)
	pin := api.Pin{Leaf: f.leaf, IdentityKey: f.idKey(), Floor: 7}
	d, ctl, _ := pinnedSignedOut(t, f, pin)
	var out struct{}
	if err := ctl("SignIn", struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
	if _, _, got, err := loadCredential(d.credentialID()); err != nil || got != pin || d.pin != pin {
		t.Fatalf("pin %+v %v", got, err)
	}
}

// Sign in again over a renewed leaf the identity key signed repins, then redeems over it.
func TestSignInOverSignedRenewal(t *testing.T) {
	f := newPinFixture(t)
	d, ctl, _ := pinnedSignedOut(t, f, api.Pin{Leaf: retiredLeaf, IdentityKey: f.idKey(), Floor: 7})
	var out struct{}
	if err := ctl("SignIn", struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
	tok, _, got, err := loadCredential(d.credentialID())
	if err != nil || tok != "kyd_PINNED" || got.Leaf != f.leaf || got.IdentityKey != f.idKey() || got.Floor <= 7 || d.state == "signed_out" {
		t.Fatalf("keyring %q %+v %v, state %q", tok, got, err, d.state)
	}
}

// Sign in again over a leaf the identity key did not sign (an interceptor's, a server that
// cannot sign, a computer with no identity key) is refused before any credential is sent.
func TestSignInOverUnvouchedLeafRefused(t *testing.T) {
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherKey := base64.StdEncoding.EncodeToString(other.Public().(ed25519.PublicKey))
	for name, c := range map[string]struct {
		identity  func(*pinFixture) string
		pinStatus int
	}{
		"interceptor":     {identity: func(*pinFixture) string { return otherKey }},
		"503":             {identity: (*pinFixture).idKey, pinStatus: http.StatusServiceUnavailable},
		"no identity key": {identity: func(*pinFixture) string { return "" }},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPinFixture(t)
			pin := api.Pin{Leaf: retiredLeaf, IdentityKey: c.identity(f), Floor: 7}
			d, ctl, opened := pinnedSignedOut(t, f, pin)
			f.pinStatus = c.pinStatus
			var out struct{}
			err := ctl("SignIn", struct{}{}, &out)
			if err == nil || !strings.Contains(err.Error(), "Unlink, then link again") || !strings.Contains(err.Error(), "files stay") {
				t.Fatalf("sign-in: %v", err)
			}
			if got := f.credentials(); len(got) != 0 {
				t.Fatalf("credentials reached the server: %v", got)
			}
			for _, p := range f.took() {
				if p != "/api/devices/tls-pin" {
					t.Fatalf("request to %s", p)
				}
			}
			if *opened != 0 {
				t.Fatal("the browser was opened")
			}
			if _, _, got, err := loadCredential(d.credentialID()); err != nil || got != pin || d.state != "signed_out" {
				t.Fatalf("pin %+v %v, state %q", got, err, d.state)
			}
		})
	}
}

// After Unlink a fresh Link trusts the leaf it meets, whatever was pinned before.
func TestLinkAfterUnlinkTrustsOnFirstUse(t *testing.T) {
	f := newPinFixture(t)
	d, ctl, _ := pinnedSignedOut(t, f, api.Pin{Leaf: retiredLeaf, Floor: 7})
	var out struct{}
	if err := ctl("Unlink", map[string]bool{"RemoveFiles": false}, &out); err != nil {
		t.Fatal(err)
	}
	if err := ctl("Link", map[string]string{"Server": f.srv.URL, "Name": "laptop"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, _, got, err := loadCredential(d.credentialID()); err != nil || got.Leaf != f.leaf || got.IdentityKey != f.idKey() || d.pin != got {
		t.Fatalf("pin %+v %v", got, err)
	}
}

func TestCredentialHybridIdentityKeyIDRoundTrip(t *testing.T) {
	keyring.MockInit()
	hybridID := "mldsa65+ed25519:" + strings.Repeat("ab", 32)
	want := api.Pin{
		Leaf:                "sha256/leaf123",
		IdentityKey:         "idkey",
		IdentityKeyHybridID: hybridID,
		Floor:               42,
	}
	if err := storeCredential("acct-hybrid", "kyd_tok", nil, want); err != nil {
		t.Fatalf("storeCredential: %v", err)
	}
	raw, err := keyring.Get(keyringService, "acct-hybrid")
	if err != nil {
		t.Fatalf("keyring.Get: %v", err)
	}
	if len(raw) >= 1024 {
		t.Fatalf("keyring payload is %d bytes; must stay compact for OS keyrings", len(raw))
	}
	tok, key, got, err := loadCredential("acct-hybrid")
	if err != nil || tok != "kyd_tok" || key != nil || got != want {
		t.Fatalf("loadCredential = (%q, %v, %+v, %v), want %+v", tok, key, got, err, want)
	}
	// Malformed 6th field must be rejected as damaged.
	if err := keyring.Set(keyringService, "acct-bad", "kyd_tok - sha256/leaf123 idkey 42 bad-hybrid-id"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadCredential("acct-bad"); !errors.Is(err, errDamaged) {
		t.Fatalf("malformed hybrid ID error = %v, want errDamaged", err)
	}
}
