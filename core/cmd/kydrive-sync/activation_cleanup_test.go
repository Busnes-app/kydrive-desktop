package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/zalando/go-keyring"
)

func TestActivationCommitsSessionBeforeCleanup(t *testing.T) {
	for _, stage := range []string{"delete", "marker"} {
		for _, initial := range []string{"unlinked", "idle"} {
			for _, legacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s_%s_legacy_%t", stage, initial, legacy), func(t *testing.T) {
					f := newAccountLinkFixture(t)
					d := f.primary
					if legacy {
						f.identity.Store(api.Account{User: "legacy"})
						handler := f.server.Config.Handler
						f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.URL.Path == "/api/drive/device" {
								http.NotFound(w, r)
								return
							}
							handler.ServeHTTP(w, r)
						})
					}
					old := f.server.URL // pre-account installation's origin-keyed credential
					oldP256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
					if err != nil {
						t.Fatal(err)
					}
					oldKey := api.SigningKeyFromP256(oldP256)
					oldPin := api.Pin{Leaf: "sha256/previous", Floor: 1}
					if err := storeCredential(old, "kyd_previous", oldKey, oldPin); err != nil {
						t.Fatal(err)
					}
					previous, _ := keyring.Get(keyringService, old)
					if err := d.db.SetConfig(map[string]string{"server": old, "user": "previous", "device": "computer", "kept": "account state"}); err != nil {
						t.Fatal(err)
					}
					d.state, d.key, d.pin = initial, oldKey, oldPin
					if err := os.MkdirAll(d.root, 0700); err != nil {
						t.Fatal(err)
					}
					file := filepath.Join(d.root, "kept.txt")
					if err := os.WriteFile(file, []byte("local edit"), 0600); err != nil {
						t.Fatal(err)
					}
					other := "account:" + accountID(old, api.Account{UserID: "other"})
					if err := storeCredential(other, "kyd_other", nil, api.Pin{}); err != nil {
						t.Fatal(err)
					}
					locked, attempts, retired := true, 0, 0
					del := migrationDelete
					t.Cleanup(func() { migrationDelete = del })
					migrationDelete = func(service, user string) error {
						if user != old {
							return errors.New("cleanup tried to delete another credential")
						}
						attempts++
						if stage == "delete" && locked {
							return errors.New("keyring temporarily unavailable")
						}
						err := del(service, user)
						if err == nil {
							retired++
						}
						return err
					}
					state, err := sql.Open("sqlite", filepath.Join(d.dir, "state.db"))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { state.Close() })
					if stage == "marker" {
						_, err = state.Exec(`CREATE TRIGGER refuse_cleanup BEFORE UPDATE ON config
WHEN NEW.key='credential_migration' AND NEW.value=''
BEGIN SELECT RAISE(FAIL, 'cleanup marker temporarily unavailable'); END`)
						if err != nil {
							t.Fatal(err)
						}
					}
					linkErr := f.linkPrimary()
					slot := d.credentialID()
					token, key, pin, err := loadCredential(slot)
					if err != nil || slot == old || token == "kyd_previous" || pin.Leaf == "" {
						t.Fatal("new credential pointer was not committed")
					}
					if d.token != token || !key.Equal(d.key) || d.pin != pin || d.state != "idle" || d.credentialUnread {
						t.Fatalf("committed credential left the in-memory session inactive: %v", linkErr)
					}
					if linkErr != nil {
						t.Fatal("post-commit cleanup failed the link", linkErr)
					}
					if legacy != strings.HasPrefix(slot, "pending:") || d.db.Get("credential_migration") != old || attempts == 0 {
						t.Fatal("activation lost its pending cleanup marker or skipped the injected failure")
					}
					committed, _ := keyring.Get(keyringService, slot)
					if stage == "delete" {
						if saved, _ := keyring.Get(keyringService, old); saved != previous {
							t.Fatal("failed cleanup lost the previous credential")
						}
					}
					locked = false
					if stage == "marker" {
						if _, err := state.Exec(`DROP TRIGGER refuse_cleanup`); err != nil {
							t.Fatal(err)
						}
					}
					// Cleanup must retry for an idle account even when sync itself is paused.
					if err := d.db.Set("paused", "1"); err != nil {
						t.Fatal(err)
					}
					d.pass(t.Context(), false)
					if d.db.Get("credential_migration") != "" || retired != 1 {
						t.Fatal("idle account did not finish cleanup exactly once")
					}
					if _, err := keyring.Get(keyringService, old); !errors.Is(err, keyring.ErrNotFound) {
						t.Fatal("recovered keyring retained the retired slot")
					}
					if err := d.db.Set("paused", ""); err != nil {
						t.Fatal(err)
					}
					d.pass(t.Context(), false)
					if d.state != "idle" || d.lastSync == 0 || d.token != token || !key.Equal(d.key) || d.pin != pin {
						t.Fatal("committed session did not sync without restart")
					}
					if saved, _ := keyring.Get(keyringService, slot); saved != committed {
						t.Fatal("cleanup changed the committed credential bytes")
					}
					if saved, _ := keyring.Get(keyringService, other); saved != "kyd_other" {
						t.Fatal("cleanup changed another account's credential")
					}
					if data, err := os.ReadFile(file); err != nil || string(data) != "local edit" || d.db.Get("kept") != "account state" {
						t.Fatal("cleanup changed files or account state")
					}
				})
			}
		}
	}
}

func TestActivationRefusesBeforePointerCommit(t *testing.T) {
	f := newAccountLinkFixture(t)
	d := f.primary
	old := f.server.URL
	if err := d.db.SetConfig(map[string]string{"server": old, "user": "previous", "device": "computer"}); err != nil {
		t.Fatal(err)
	}
	if err := storeCredential(old, "kyd_previous", nil, api.Pin{}); err != nil {
		t.Fatal(err)
	}
	state, err := sql.Open("sqlite", filepath.Join(d.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.Exec(`CREATE TRIGGER refuse_commit BEFORE INSERT ON config
WHEN NEW.key='credential_migration' AND NEW.value<>''
BEGIN SELECT RAISE(FAIL, 'pointer commit refused'); END`); err != nil {
		t.Fatal(err)
	}
	deletes := 0
	del := migrationDelete
	defer func() { migrationDelete = del }()
	migrationDelete = func(service, user string) error { deletes++; return del(service, user) }
	if err := f.linkPrimary(); err == nil {
		t.Fatal("failed pointer commit activated an account")
	}
	if d.token != "" || d.key != nil || d.pin != (api.Pin{}) || d.state != "unlinked" || d.credentialID() != old || d.db.Get("user") != "previous" || d.db.Get("credential_migration") != "" || deletes != 0 {
		t.Fatal("pre-commit failure changed the old pointer or in-memory session")
	}
	if kept, _ := keyring.Get(keyringService, old); kept != "kyd_previous" {
		t.Fatal("pre-commit failure lost the previous credential")
	}
}

func TestPendingCleanupReservesOldAccountSlotUntilUnlinkedRecovery(t *testing.T) {
	f := newAccountLinkFixture(t)
	if err := f.linkPrimary(); err != nil {
		t.Fatal(err)
	}
	d := f.primary
	d.state = "signed_out"
	if err := f.core.SignIn(AccountArgs{AccountID: "legacy"}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	previousIdentity := f.identity.Load().(api.Account)
	source := d.credentialID()
	before, _ := keyring.Get(keyringService, source)
	// Model a legacy idle primary awaiting a fresh link, with its old credential still stored.
	d.token, d.state = "", "idle"
	next := previousIdentity
	next.OrgID = "next"
	f.identity.Store(next)
	locked := true
	del := migrationDelete
	defer func() { migrationDelete = del }()
	migrationDelete = func(service, user string) error {
		if user == source && locked {
			return errors.New("retirement temporarily unavailable")
		}
		return del(service, user)
	}
	if err := f.linkPrimary(); err != nil {
		t.Fatal(err)
	}
	if d.db.Get("credential_migration") != source || d.credentialID() == source {
		t.Fatal("fixture did not retain canonical-slot cleanup")
	}
	f.identity.Store(previousIdentity)
	assertReserved := func() {
		t.Helper()
		if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &AccountStatus{}); err == nil || !strings.Contains(err.Error(), "still being retired") {
			t.Fatal("another approval claimed a slot awaiting deletion", err)
		}
		if kept, _ := keyring.Get(keyringService, source); kept != before {
			t.Fatal("reserved slot was overwritten")
		}
	}
	assertReserved()
	// A subsequent renewal must preserve an earlier source marker if cleanup is still blocked.
	f.identity.Store(next)
	active := d.token
	d.state = "signed_out"
	if err := f.core.SignIn(struct{ AccountID string }{"legacy"}, &struct{}{}); !errors.Is(err, errKeyringUnavailable) {
		t.Fatal("renewal bypassed unfinished credential retirement", err)
	}
	if d.db.Get("credential_migration") != source || d.token != active {
		t.Fatal("renewal replaced a pending source marker or active credential")
	}
	f.identity.Store(previousIdentity)
	if err := f.unlinkPrimary(); err != nil {
		t.Fatal(err)
	}
	assertReserved()
	// An unlinked daemon also retries its own pending cleanup without reauthentication.
	locked = false
	d.pass(t.Context(), false)
	if d.state != "unlinked" || d.db.Get("credential_migration") != "" {
		t.Fatal("unlinked account did not finish pending cleanup")
	}
	var added AccountStatus
	if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &added); err != nil {
		t.Fatal(err)
	}
	other := f.core.m.accounts[added.ID]
	saved, err := keyring.Get(keyringService, other.credentialID())
	if _, err := keyring.Get(keyringService, source); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("recovered physical source was retained")
	}
	if err != nil || saved == before {
		t.Fatal("recovered slot did not receive the new account's credential")
	}
	d.pass(t.Context(), false)
	if kept, _ := keyring.Get(keyringService, other.credentialID()); kept != saved {
		t.Fatal("stale cleanup deleted another account's new credential")
	}
	other.pass(t.Context(), false)
	if other.state != "idle" || other.lastSync == 0 {
		t.Fatal("account could not sync after old-slot recovery")
	}
}
