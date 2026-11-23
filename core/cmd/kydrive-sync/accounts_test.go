package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"runtime"
	"testing"
)

func TestAccountIdentityIncludesTenantAndIssuer(t *testing.T) {
	a := api.Account{Issuer: "https://identity.example", Sub: "person", OrgID: "one"}
	b := a
	b.OrgID = "two"
	c := a
	c.Issuer = "https://other.example"
	if accountID("https://drive.example", a) == accountID("https://drive.example", b) || accountID("https://drive.example", a) == accountID("https://drive.example", c) {
		t.Fatal("accounts alias")
	}
}

func TestOriginCredentialMigrationIsIdempotent(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	db, err := engine.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetConfig(map[string]string{"server": "https://drive.example", "issuer": "https://identity.example", "sub": "person", "org_id": "one", "user_id": "local"})
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pin := api.Pin{Leaf: "sha256/old", IdentityKey: "identity", Floor: 123}
	if err := storeCredential("https://drive.example", "kyd_original", key, pin); err != nil {
		t.Fatal(err)
	}
	db.Close()
	d, err := newDaemon(dir, filepath.Join(t.TempDir(), "KyDrive"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.db.Close()
	id := d.credentialID()
	saved, err := keyring.Get(keyringService, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, "https://drive.example"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("origin entry survived migration")
	}
	if err := d.migrateCredential(); err != nil {
		t.Fatal(err)
	}
	again, _ := keyring.Get(keyringService, id)
	if saved != again || d.token != "kyd_original" || !d.key.Equal(key) || d.pin != pin {
		t.Fatal("migration changed the link")
	}
	// Simulate restart after the pointer commit and before deleting the old item.
	keyring.Set(keyringService, "https://drive.example", saved)
	d.db.Set("credential_migration", "https://drive.example")
	if err := d.migrateCredential(); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, "https://drive.example"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("interrupted migration not finished")
	}
}

func TestAccountsOnOneOriginAreIsolatedAndRemovalKeepsOthers(t *testing.T) {
	keyring.MockInit()
	var approved atomic.Value
	approved.Store(api.Account{Issuer: "https://identity.example", Sub: "same-person", OrgID: "one", UserID: "local", User: "person"})
	var serial atomic.Int32
	var identities sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/devices/token":
			a := approved.Load().(api.Account)
			token := fmt.Sprintf("kyd_%d", serial.Add(1))
			identities.Store(token, a)
			json.NewEncoder(w).Encode(map[string]string{"token": token, "user_id": a.UserID, "user": a.User, "issuer": a.Issuer, "sub": a.Sub, "org_id": a.OrgID})
		case "/api/drive/device":
			a, ok := identities.Load(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if !ok {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(a)
		case "/api/devices/self":
			w.WriteHeader(204)
		case "/api/drive/workspaces":
			json.NewEncoder(w).Encode([]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	primary, err := newDaemon(filepath.Join(dir, "state"), filepath.Join(dir, "KyDrive"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer primary.db.Close()
	primary.open = func(raw string) error {
		u, _ := url.Parse(raw)
		q := u.Query()
		go func() {
			res, err := http.Get(q.Get("redirect") + "?code=approved&state=" + url.QueryEscape(q.Get("state")))
			if err == nil {
				res.Body.Close()
			}
		}()
		return nil
	}
	m, err := newAccountManager(t.Context(), primary)
	if err != nil {
		t.Fatal(err)
	}
	service := &AccountsCore{m: m}
	var one, two AccountStatus
	if err := service.AddAccount(LinkArgs{Server: srv.URL}, &one); err != nil {
		t.Fatal(err)
	}
	a := approved.Load().(api.Account)
	a.OrgID = "two"
	approved.Store(a)
	if err := service.AddAccount(LinkArgs{Server: srv.URL}, &two); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, d := range m.accounts {
			d.db.Close()
		}
	}()
	d1, d2 := m.accounts[one.ID], m.accounts[two.ID]
	if one.ID == two.ID || d1.dir == d2.dir || d1.root == d2.root || d1.credentialID() == d2.credentialID() || d1.key.Equal(d2.key) {
		t.Fatal("accounts share storage or keys")
	}
	d1.db.Set("paused", "1")
	if d2.db.Get("paused") != "" {
		t.Fatal("account state leaked")
	}
	for _, d := range []*daemon{d1, d2} {
		if err := os.WriteFile(filepath.Join(d.root, "kept.txt"), []byte(d.token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	saved, _ := keyring.Get(keyringService, d2.credentialID())
	if err := service.RemoveAccount(struct {
		AccountID   string
		RemoveFiles bool
	}{one.ID, false}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, d1.credentialID()); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("removed account retained credential")
	}
	kept, _ := keyring.Get(keyringService, d2.credentialID())
	if kept != saved {
		t.Fatal("removing one account changed another credential")
	}
	for _, d := range []*daemon{d1, d2} {
		if _, err := os.Stat(filepath.Join(d.root, "kept.txt")); err != nil {
			t.Fatal("removal touched retained files")
		}
	}
	if list := m.list(); len(list) != 1 || list[0].ID != two.ID {
		t.Fatal("account list includes removed account")
	}
	// Re-adding the removed identity retains its own files, without adopting them.
	a.OrgID = "one"
	approved.Store(a)
	var again AccountStatus
	if err := service.AddAccount(LinkArgs{Server: srv.URL}, &again); err != nil {
		t.Fatal(err)
	}
	if again.ID != one.ID || again.Root != one.Root {
		t.Fatal("re-add lost account ownership")
	}
	if _, err := os.Stat(filepath.Join(again.Root, "kept.txt")); err != nil {
		t.Fatal(err)
	}
	var duplicate AccountStatus
	if err := service.AddAccount(LinkArgs{Server: srv.URL}, &duplicate); err == nil {
		t.Fatal("duplicate link accepted")
	}
	if kept, _ := keyring.Get(keyringService, d2.credentialID()); kept != saved {
		t.Fatal("duplicate changed another account")
	}
	// On restart both account databases restore independently.
	for _, d := range m.accounts {
		d.db.Close()
	}
	m.accounts = map[string]*daemon{}
	restarted, err := newAccountManager(t.Context(), primary)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, d := range restarted.accounts {
			d.db.Close()
		}
	}()
	if len(restarted.list()) != 2 {
		t.Fatal("restart lost accounts")
	}
}

func TestAccountRootLinkCannotWipeAnotherAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows junction controls run in engine tests")
	}
	keyring.MockInit()
	dir := t.TempDir()
	other := filepath.Join(dir, "other")
	if err := os.MkdirAll(filepath.Join(other, "Docs"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(other, "Docs", "private.txt")
	if err := os.WriteFile(file, []byte("other account"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "redirected")
	if err := os.Symlink(other, root); err != nil {
		t.Fatal(err)
	}
	d, err := newDaemon(filepath.Join(dir, "state"), root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.db.Close()
	d.db.PutWorkspace(engine.Workspace{ID: "same-workspace-id", Name: "Docs", Dir: "Docs"})
	if err := d.engine().StartWipe(); err == nil {
		t.Fatal("wipe followed another account's root")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "other account" {
		t.Fatal("another account's files changed")
	}
}
