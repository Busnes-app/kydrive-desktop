package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

func TestReAddMigratedAccountKeepsStorageAndFiles(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart_%t", restart), func(t *testing.T) {
			f := newAccountLinkFixture(t)
			who := api.Account{UserID: "local-person", User: "person"}
			f.identity.Store(who)
			var original AccountStatus
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &original); err != nil {
				t.Fatal(err)
			}
			d := f.core.m.accounts[original.ID]
			dir, root, slot, token := d.dir, d.root, d.credentialID(), d.token
			key, pin := d.key, d.pin
			before, err := keyring.Get(keyringService, slot)
			if err != nil {
				t.Fatal(err)
			}
			stateFile, err := os.Stat(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := d.db.Set("kept", "original account state"); err != nil {
				t.Fatal(err)
			}
			if err := d.db.PutWorkspace(engine.Workspace{ID: "docs", Name: "Docs", Dir: "Docs", Cursor: 10}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "Docs"), 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "Docs", "kept.txt")
			if err := os.WriteFile(file, []byte("retained local edit"), 0600); err != nil {
				t.Fatal(err)
			}
			assertStorage := func() {
				t.Helper()
				if d.dir != dir || d.root != root || d.db.Get("storage_id") != original.ID || d.db.Get("kept") != "original account state" {
					t.Fatal("account lost its retained storage or state")
				}
				if current, err := os.Stat(filepath.Join(d.dir, "state.db")); err != nil || !os.SameFile(stateFile, current) {
					t.Fatal("account replaced its original database")
				}
				if data, err := os.ReadFile(file); err != nil || string(data) != "retained local edit" {
					t.Fatal("account changed its retained file")
				}
			}
			// The authenticated account route learns hosted claims for the existing link.
			who.Issuer, who.Sub, who.OrgID = "https://identity.example", "person", "org"
			f.tokens.Store(token, who)
			f.identity.Store(who)
			d.pass(t.Context(), false)
			identity := accountID(f.server.URL, who)
			migratedSlot := "account:" + identity
			if d.state != "idle" || d.lastSync == 0 || identity == original.ID || d.db.Get("account_id") != identity || d.credentialID() != migratedSlot {
				t.Fatal("fixture did not migrate local identity through a sync pass")
			}
			if saved, err := keyring.Get(keyringService, migratedSlot); err != nil || saved != before || d.token != token || !d.key.Equal(key) || d.pin != pin {
				t.Fatal("identity migration changed the credential bytes, key or pin")
			}
			if _, err := keyring.Get(keyringService, slot); !errors.Is(err, keyring.ErrNotFound) {
				t.Fatal("migration retained the old credential slot")
			}
			assertStorage()
			// Active duplicate approval still cannot publish over the migrated credential.
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &AccountStatus{}); err == nil {
				t.Fatal("duplicate added approval accepted after identity migration")
			}
			if err := f.linkPrimary(); err == nil {
				t.Fatal("duplicate primary approval accepted after identity migration")
			}
			if saved, _ := keyring.Get(keyringService, migratedSlot); saved != before {
				t.Fatal("duplicate approval changed the migrated credential")
			}
			if err := f.core.RemoveAccount(struct {
				AccountID   string
				RemoveFiles bool
			}{original.ID, false}, &struct{}{}); err != nil {
				t.Fatal(err)
			}
			if _, err := keyring.Get(keyringService, migratedSlot); !errors.Is(err, keyring.ErrNotFound) {
				t.Fatal("removal retained the migrated credential")
			}
			if restart {
				if err := d.db.Close(); err != nil {
					t.Fatal(err)
				}
				f.core.m.accounts = map[string]*daemon{}
				m, err := newAccountManager(t.Context(), f.primary)
				if err != nil {
					t.Fatal(err)
				}
				f.core.m = m
				t.Cleanup(func() {
					for _, restored := range m.accounts {
						restored.db.Close()
					}
				})
				d = m.accounts[original.ID]
				d.http, d.open = f.primary.http, f.primary.open
			}
			assertStorage()
			// A retained wipe must be checked by canonical identity before publishing a new link.
			if err := d.db.Set("wipe", "1"); err != nil {
				t.Fatal(err)
			}
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &AccountStatus{}); err == nil || !strings.Contains(err.Error(), "unfinished file removal") {
				t.Fatal("re-add bypassed the retained account's unfinished wipe", err)
			}
			if _, err := keyring.Get(keyringService, migratedSlot); !errors.Is(err, keyring.ErrNotFound) {
				t.Fatal("refused re-add published a credential")
			}
			if err := d.db.Set("wipe", ""); err != nil {
				t.Fatal(err)
			}
			var again AccountStatus
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &again); err != nil {
				t.Fatal(err)
			}
			if again.ID != original.ID || again.Root != root || len(f.core.m.accounts) != 1 || f.core.m.accounts[original.ID] != d {
				t.Fatal("re-add replaced the original storage selector, daemon or sync root")
			}
			assertStorage()
			for _, unexpected := range []string{filepath.Join(f.primary.dir, "accounts", identity), accountRoot(f.primary.root, identity)} {
				if _, err := os.Stat(unexpected); !os.IsNotExist(err) {
					t.Fatal("re-add created storage at the migrated identity hash")
				}
			}
			// Removal forgets bindings; choosing again keeps local edits out of the new binding.
			if err := f.core.Choose(struct {
				AccountID string
				IDs       []string
			}{original.ID, []string{"docs"}}, &struct{}{}); err != nil {
				t.Fatal(err)
			}
			ws, err := d.db.Workspaces()
			if err != nil || len(ws) != 1 || ws[0].Dir == "Docs" || !strings.HasPrefix(ws[0].Dir, "Docs") {
				t.Fatal("re-add adopted the retained local files as synced copies")
			}
			d.lastSync = 0
			d.pass(t.Context(), false)
			if d.state != "idle" || d.lastSync == 0 || d.token == token || d.credentialID() != migratedSlot {
				t.Fatal("re-added account did not resume sync in its original storage")
			}
			loaded, loadedKey, loadedPin, err := loadCredential(migratedSlot)
			if err != nil || loaded != d.token || !loadedKey.Equal(d.key) || loadedPin != d.pin {
				t.Fatal("re-added account did not persist its new credential exactly")
			}
			assertStorage()
		})
	}
}
