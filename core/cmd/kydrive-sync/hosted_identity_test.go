package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

func TestHostedDeviceIdentityDoesNotRequireLocalID(t *testing.T) {
	hosted := api.Account{Issuer: "https://identity.example", Sub: "person", OrgID: "org", UserID: "old-local", User: "person"}
	for _, tc := range []struct {
		name             string
		upgrade, allowed bool
		change           func(*api.Account)
	}{
		{"matching without local ID", false, true, func(a *api.Account) { a.UserID = "" }},
		{"matching with different local ID", false, true, func(a *api.Account) { a.UserID = "new-local" }},
		{"local upgrade without local ID", true, true, func(a *api.Account) { a.UserID = "" }},
		{"local upgrade with local ID", true, true, func(a *api.Account) {}},
		{"wrong issuer", false, false, func(a *api.Account) { a.Issuer = "https://other.example" }},
		{"wrong subject", false, false, func(a *api.Account) { a.Sub = "other" }},
		{"wrong organization", false, false, func(a *api.Account) { a.OrgID = "other" }},
		{"removed organization", false, false, func(a *api.Account) { a.OrgID = "" }},
		{"missing issuer", false, false, func(a *api.Account) { a.Issuer = "" }},
		{"missing subject", false, false, func(a *api.Account) { a.Sub = "" }},
		{"hosted downgrade", false, false, func(a *api.Account) { a.Issuer, a.Sub, a.OrgID = "", "", "" }},
		{"partial local upgrade", true, false, func(a *api.Account) { a.Sub = "" }},
		{"legacy changed local ID", true, false, func(a *api.Account) { a.Issuer, a.Sub, a.OrgID = "", "", ""; a.UserID = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccountLinkFixture(t)
			initial := hosted
			if tc.upgrade {
				initial.Issuer, initial.Sub, initial.OrgID = "", "", ""
			}
			f.identity.Store(initial)
			var a AccountStatus
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &a); err != nil {
				t.Fatal(err)
			}
			d := f.core.m.accounts[a.ID]
			// Exercise the committed physical replacement pointer too.
			d.state = "signed_out"
			if err := f.core.SignIn(AccountArgs{AccountID: a.ID}, &struct{}{}); err != nil {
				t.Fatal(err)
			}
			otherWho := initial
			otherWho.UserID, otherWho.Sub, otherWho.OrgID = "other-local", "other", "other"
			if tc.upgrade {
				otherWho.Sub, otherWho.OrgID = "", ""
			}
			f.identity.Store(otherWho)
			var other AccountStatus
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &other); err != nil {
				t.Fatal(err)
			}
			otherDaemon := f.core.m.accounts[other.ID]
			otherSlot := otherDaemon.credentialID()
			otherSaved, _ := keyring.Get(keyringService, otherSlot)
			slot, token, key, pin, root, dir := d.credentialID(), d.token, d.key, d.pin, d.root, d.dir
			saved, err := keyring.Get(keyringService, slot)
			if err != nil {
				t.Fatal(err)
			}
			binding := engine.Workspace{ID: "docs", Name: "Docs", Dir: "Docs", Cursor: 0}
			if err := d.db.PutWorkspace(binding); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "kept.txt")
			if err := os.WriteFile(file, []byte("local edit"), 0600); err != nil {
				t.Fatal(err)
			}
			response := hosted
			tc.change(&response)
			f.tokens.Store(token, response)
			assertState := func() {
				t.Helper()
				if d.token != token || d.key == nil || !d.key.Equal(key) || d.pin != pin || d.root != root || d.dir != dir || d.db.Get("storage_id") != a.ID {
					t.Fatal("identity check changed credential, pin or storage")
				}
				if got, _ := keyring.Get(keyringService, d.credentialID()); got != saved {
					t.Fatal("identity check changed exact credential bytes")
				}
				ws, err := d.db.Workspaces()
				expected := binding
				if tc.allowed {
					expected.Cursor = 10
				}
				if err != nil || len(ws) != 1 || ws[0] != expected {
					t.Fatal("identity refusal changed bindings or allowed sync failed to pull")
				}
				if data, err := os.ReadFile(file); err != nil || string(data) != "local edit" {
					t.Fatal("identity check changed local files")
				}
				if got, _ := keyring.Get(keyringService, otherSlot); got != otherSaved || otherDaemon.token == "" {
					t.Fatal("identity check changed another account")
				}
			}
			d.pass(t.Context(), false)
			if tc.allowed {
				if d.state != "idle" || d.lastSync == 0 || d.account() != response {
					t.Fatal("complete matching hosted identity did not sync", d.lastErr)
				}
				if tc.upgrade {
					if d.credentialID() != "account:"+accountID(f.server.URL, response) {
						t.Fatal("authenticated hosted upgrade did not migrate credential identity")
					}
					if _, err := keyring.Get(keyringService, slot); !errors.Is(err, keyring.ErrNotFound) {
						t.Fatal("identity upgrade retained old physical slot")
					}
				} else if d.credentialID() != slot {
					t.Fatal("matching hosted identity moved physical slot")
				}
			} else if d.state != "error" || d.lastSync != 0 || d.db.Get("identity_checked") != "" || d.credentialID() != slot || d.account() != initial {
				t.Fatal("changed or partial identity was accepted or mutated account")
			}
			assertState()
			if tc.allowed {
				f.identity.Store(response)
				if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &AccountStatus{}); err == nil {
					t.Fatal("hosted approval bypassed active duplicate guard")
				}
				if err := f.linkPrimary(); err == nil {
					t.Fatal("hosted primary approval bypassed active duplicate guard")
				}
				assertState()
			}
			current := d.credentialID()
			if err := d.db.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := newDaemon(dir, root, "")
			if err != nil {
				t.Fatal(err)
			}
			defer restored.db.Close()
			restored.http, restored.open = f.primary.http, f.primary.open
			d = restored
			d.pass(t.Context(), false)
			if (d.state == "idle" && d.lastSync != 0) != tc.allowed || d.credentialID() != current {
				t.Fatal("restart changed hosted identity decision or physical pointer")
			}
			assertState()
			otherDaemon.pass(t.Context(), false)
			if otherDaemon.state != "idle" || otherDaemon.lastSync == 0 {
				t.Fatal("identity refusal blocked another account")
			}
		})
	}
}
