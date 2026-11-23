package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

func TestSignInWaitsForMigrationPinRecovery(t *testing.T) {
	keyring.MockInit()
	f := newPinFixture(t)
	// The served leaf is system-trusted here, but the saved pin has not vouched for it.
	held := api.Pin{Leaf: retiredLeaf, Floor: 7}
	dir := t.TempDir()
	db, err := engine.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConfig(map[string]string{"server": f.srv.URL, "user_id": "u1", "user": "alice", "device": "computer"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := storeCredential(f.srv.URL, "kyd_PINNED", nil, held); err != nil {
		t.Fatal(err)
	}
	original, _ := keyring.Get(keyringService, f.srv.URL)
	del := migrationDelete
	defer func() { migrationDelete = del }()
	migrationDelete = func(service, user string) error { return errors.New("keyring temporarily locked") }
	run, err := os.MkdirTemp("", "kyd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	d, err := newDaemon(dir, filepath.Join(dir, "KyDrive"), filepath.Join(run, "sync.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.db.Close()
	d.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: f.roots}
	if err := (&api.Client{Base: f.srv.URL, HTTP: d.http}).Reach(t.Context()); err != nil {
		t.Fatal("unvouched leaf is not trusted by the test system roots")
	}
	f.took() // discard the credential-free trust control before testing SignIn
	opened := 0
	d.open = func(string) error { opened++; return errors.New("browser must not open") }
	core := &Core{d: d, ctx: context.Background()}
	if err := core.SignIn(struct{}{}, &struct{}{}); err == nil || opened != 0 {
		t.Fatal("sign-in reached browser before saved pin recovery")
	}
	if len(f.credentials()) != 0 || len(f.took()) != 0 {
		t.Fatal("unrecovered sign-in reached server")
	}
	if _, _, pin, err := loadCredential(d.credentialID()); err != nil || pin != held {
		t.Fatal("sign-in changed stored pin while keyring unavailable")
	}
	if d.state != "signed_out" || d.token != "" {
		t.Fatal("failed reauthentication activated the account")
	}
	migrationDelete = del
	d.mu.Lock()
	d.retryAt = time.Time{}
	d.mu.Unlock()
	if !d.retry() {
		t.Fatal("credential did not recover after keyring unlock")
	}
	d.mu.Lock()
	d.state = "signed_out"
	d.mu.Unlock() // server still requires reauthentication
	if err := core.SignIn(struct{}{}, &struct{}{}); err == nil || !strings.Contains(err.Error(), "certificate changed") || opened != 0 {
		t.Fatal("recovered sign-in did not enforce the existing pin")
	}
	if len(f.credentials()) != 0 {
		t.Fatal("credentials reached unvouched leaf after recovery")
	}
	saved, err := keyring.Get(keyringService, d.credentialID())
	if err != nil || saved != original || d.pin != held {
		t.Fatal("recovery changed held credential bytes or pin")
	}
	if _, err := keyring.Get(keyringService, f.srv.URL); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("migration cleanup did not complete")
	}
}

func TestSignInWaitsForUnreadCredential(t *testing.T) {
	keyring.MockInitWithError(errors.New("keyring temporarily locked"))
	defer keyring.MockInit()
	dir := t.TempDir()
	db, err := engine.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	a := api.Account{UserID: "u1"}
	if err := db.SetConfig(map[string]string{"server": "https://drive.example", "user_id": "u1", "user": "alice", "account_id": accountID("https://drive.example", a)}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	d, err := newDaemon(dir, filepath.Join(dir, "root"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.db.Close()
	opened := false
	d.open = func(string) error { opened = true; return errors.New("browser must not open") }
	if err := (&Core{d: d, ctx: t.Context()}).SignIn(struct{}{}, &struct{}{}); err == nil || opened {
		t.Fatal("unread credential was treated as never pinned")
	}
}

// A successfully read legacy credential with no pin is known absence, not unavailable state.
func TestSignInCanPinReadLegacyCredential(t *testing.T) {
	keyring.MockInit()
	f := newPinFixture(t)
	dir := t.TempDir()
	db, err := engine.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConfig(map[string]string{"server": f.srv.URL, "user_id": "u1", "user": "alice", "device": "computer"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := storeCredential(f.srv.URL, "kyd_PINNED", nil, api.Pin{}); err != nil {
		t.Fatal(err)
	}
	d, err := newDaemon(dir, filepath.Join(dir, "root"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.db.Close()
	d.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: f.roots}
	d.state = "signed_out"
	d.open = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		go func() {
			res, err := http.Get(q.Get("redirect") + "?code=c1&state=" + url.QueryEscape(q.Get("state")))
			if err == nil {
				res.Body.Close()
			}
		}()
		return nil
	}
	if err := (&Core{d: d, ctx: t.Context()}).SignIn(struct{}{}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if d.state != "idle" || d.pin.Leaf != f.leaf {
		t.Fatal("read legacy credential could not establish its first pin")
	}
}
