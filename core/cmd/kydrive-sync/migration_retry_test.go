package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

func TestMigrationKeyringUnavailableKeepsRPCAndRecovers(t *testing.T) {
	for _, stage := range []string{"source read", "target read", "copy", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			keyring.MockInit()
			a := api.Account{Issuer: "https://identity.example", Sub: "person", OrgID: "one", UserID: "local", User: "person"}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/drive/device":
					json.NewEncoder(w).Encode(a)
				case "/api/drive/workspaces":
					json.NewEncoder(w).Encode([]api.Workspace{})
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			dir := t.TempDir()
			db, err := engine.Open(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.SetConfig(map[string]string{"server": srv.URL, "issuer": a.Issuer, "sub": a.Sub, "org_id": a.OrgID, "user_id": a.UserID, "user": a.User, "kept": "primary state"}); err != nil {
				t.Fatal(err)
			}
			db.Close()
			p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			key := api.SigningKeyFromP256(p256)
			pin := api.Pin{Leaf: "sha256/kept", IdentityKey: "kept", Floor: 123}
			if err := storeCredential(srv.URL, "kyd_preserved", key, pin); err != nil {
				t.Fatal(err)
			}
			saved, err := keyring.Get(keyringService, srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			target := "account:" + accountID(srv.URL, a)
			other := a
			other.OrgID = "two"
			otherID := accountID(srv.URL, other)
			otherDir := filepath.Join(dir, "accounts", otherID)
			if err := os.MkdirAll(otherDir, 0700); err != nil {
				t.Fatal(err)
			}
			otherDB, err := engine.Open(filepath.Join(otherDir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := otherDB.SetConfig(map[string]string{"server": srv.URL, "issuer": other.Issuer, "sub": other.Sub, "org_id": other.OrgID, "user_id": other.UserID, "user": other.User, "account_id": otherID, "storage_id": otherID, "kept": "other state"}); err != nil {
				t.Fatal(err)
			}
			otherDB.Close()
			if err := storeCredential("account:"+otherID, "kyd_other", nil, api.Pin{}); err != nil {
				t.Fatal(err)
			}
			otherSaved, _ := keyring.Get(keyringService, "account:"+otherID)
			get, set, del := migrationGet, migrationSet, migrationDelete
			defer func() { migrationGet, migrationSet, migrationDelete = get, set, del }()
			locked := true
			copies, deletes := 0, 0
			unavailable := errors.New("keyring temporarily locked")
			migrationGet = func(service, user string) (string, error) {
				if locked && ((stage == "source read" && user == srv.URL) || (stage == "target read" && user == target)) {
					return "", unavailable
				}
				return get(service, user)
			}
			migrationSet = func(service, user, value string) error {
				if locked && stage == "copy" && user == target {
					return unavailable
				}
				if user == target {
					copies++
				}
				return set(service, user, value)
			}
			migrationDelete = func(service, user string) error {
				if locked && stage == "cleanup" && user == srv.URL {
					return unavailable
				}
				if user == srv.URL {
					deletes++
				}
				return del(service, user)
			}
			run, err := os.MkdirTemp("", "kyd")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(run)
			d, err := newDaemon(dir, filepath.Join(dir, "KyDrive"), filepath.Join(run, "sync.sock"))
			if err != nil {
				t.Fatalf("temporary keyring failure prevented startup: %v", err)
			}
			defer d.db.Close()
			if d.state != "signed_out" || d.token != "" {
				t.Fatal("locked account did not stay signed out")
			}
			m, err := newAccountManager(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				for _, other := range m.accounts {
					other.db.Close()
				}
			}()
			d.manager = m
			ln, err := d.listen()
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			defer d.lock.Close()
			go d.serve(t.Context(), ln)
			var status Status
			if err := call(d.sock, "Status", []byte(`{"AccountID":"legacy"}`), &status); err != nil || status.State != "signed_out" {
				t.Fatal("RPC unavailable while keyring locked")
			}
			if len(m.list()) != 2 {
				t.Fatal("unavailable keyring hid another account")
			}
			if got, _ := keyring.Get(keyringService, srv.URL); got != saved {
				t.Fatal("locked migration lost source credential")
			}
			d.pass(t.Context(), false)
			if d.state != "signed_out" {
				t.Fatal("locked retry changed state")
			}
			locked = false
			d.mu.Lock()
			d.retryAt = time.Time{}
			d.mu.Unlock()
			d.pass(t.Context(), false)
			if d.state != "idle" || d.lastSync == 0 {
				t.Fatal("account did not resume synchronization")
			}
			if d.token != "kyd_preserved" || !d.key.Equal(key) || d.pin != pin {
				t.Fatal("migration changed token, signing key or pin")
			}
			if got, _ := keyring.Get(keyringService, target); got != saved {
				t.Fatal("migrated credential bytes changed")
			}
			if _, err := keyring.Get(keyringService, srv.URL); !errors.Is(err, keyring.ErrNotFound) {
				t.Fatal("source not retired")
			}
			if err := d.migrateCredential(); err != nil {
				t.Fatal(err)
			}
			if copies != 1 || deletes != 1 || d.db.Get("credential_migration") != "" {
				t.Fatal("migration did not complete exactly once")
			}
			if got, _ := keyring.Get(keyringService, "account:"+otherID); got != otherSaved || m.accounts[otherID].db.Get("kept") != "other state" || d.db.Get("kept") != "primary state" {
				t.Fatal("recovery changed another account or lost state")
			}
		})
	}
}

func TestMigrationSecurityErrorsStillRefuseStartup(t *testing.T) {
	for _, problem := range []string{"partial identity", "different credential"} {
		t.Run(problem, func(t *testing.T) {
			keyring.MockInit()
			dir := t.TempDir()
			a := api.Account{Issuer: "https://identity.example", Sub: "person", OrgID: "one", UserID: "local"}
			db, err := engine.Open(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			metadata := map[string]string{"server": "https://drive.example", "issuer": a.Issuer, "sub": a.Sub, "org_id": a.OrgID, "user_id": a.UserID}
			if problem == "partial identity" {
				metadata["sub"] = ""
			}
			if err := db.SetConfig(metadata); err != nil {
				t.Fatal(err)
			}
			db.Close()
			keyring.Set(keyringService, "https://drive.example", "kyd_original")
			target := "account:" + accountID("https://drive.example", a)
			if problem == "different credential" {
				keyring.Set(keyringService, target, "kyd_other")
			}
			d, err := newDaemon(dir, filepath.Join(dir, "root"), "")
			if err == nil {
				d.db.Close()
				t.Fatal("security error was treated as temporary keyring unavailability")
			}
			if got, _ := keyring.Get(keyringService, "https://drive.example"); got != "kyd_original" {
				t.Fatal("refusal changed source credential")
			}
			if problem == "different credential" {
				if got, _ := keyring.Get(keyringService, target); got != "kyd_other" {
					t.Fatal("refusal changed target credential")
				}
			}
		})
	}
}

func TestMigrationRetryDoesNotRecoverConflictingCredential(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	a := api.Account{Issuer: "https://identity.example", Sub: "person", OrgID: "one", UserID: "local"}
	server := "https://drive.example"
	db, err := engine.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConfig(map[string]string{"server": server, "issuer": a.Issuer, "sub": a.Sub, "org_id": a.OrgID, "user_id": a.UserID}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	keyring.Set(keyringService, server, "kyd_original")
	get := migrationGet
	defer func() { migrationGet = get }()
	migrationGet = func(service, user string) (string, error) { return "", errors.New("keyring temporarily locked") }
	d, err := newDaemon(dir, filepath.Join(dir, "root"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.db.Close()
	migrationGet = get
	target := "account:" + accountID(server, a)
	keyring.Set(keyringService, target, "kyd_other")
	d.pass(t.Context(), false)
	if d.state != "error" || d.token != "" || d.lastSync != 0 {
		t.Fatal("retry activated a credential despite an identity conflict")
	}
	if source, _ := keyring.Get(keyringService, server); source != "kyd_original" {
		t.Fatal("conflict changed the source")
	}
	if held, _ := keyring.Get(keyringService, target); held != "kyd_other" {
		t.Fatal("conflict changed the target")
	}
}
