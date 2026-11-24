package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

func TestSameAccountReplacementFailurePreservesCredential(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		for _, stage := range []string{"folder", "database", "keyring", "keyring_ack"} {
			t.Run(fmt.Sprintf("%s_revoked_%t", stage, revoked), func(t *testing.T) {
				f := newAccountLinkFixture(t)
				if err := f.linkPrimary(); err != nil {
					t.Fatal(err)
				}
				d := f.primary
				binding := engine.Workspace{ID: "docs", Name: "Docs", Dir: "Docs", Cursor: 10}
				if err := d.db.PutWorkspace(binding); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(d.root, "kept.txt")
				if err := os.WriteFile(file, []byte("local edit"), 0600); err != nil {
					t.Fatal(err)
				}
				if revoked {
					f.revoked.Store(d.token, true)
					d.pass(t.Context(), false)
				} else {
					d.state = "signed_out"
				}
				d.mu.Lock()
				heldSession := d.session()
				d.mu.Unlock()
				old, token, key, pin, state := d.credentialID(), d.token, d.key, d.pin, d.state
				saved, err := keyring.Get(keyringService, old)
				if err != nil {
					t.Fatal(err)
				}
				identity, slot := d.db.Get("account_id"), d.db.Get("credential_slot")
				other := "account:" + accountID(f.server.URL, api.Account{UserID: "other"})
				if err := keyring.Set(keyringService, other, "other credential"); err != nil {
					t.Fatal(err)
				}
				if stage == "folder" {
					blocked := filepath.Join(d.dir, "blocked")
					if err := os.WriteFile(blocked, []byte("keep blocker"), 0600); err != nil {
						t.Fatal(err)
					}
					d.root = filepath.Join(blocked, "KyDrive")
				}
				root, dir := d.root, d.dir
				db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if stage == "database" {
					if _, err := db.Exec(`CREATE TRIGGER refuse_pointer BEFORE INSERT ON config
WHEN NEW.key='credential_slot' BEGIN SELECT RAISE(FAIL,'pointer commit unavailable'); END`); err != nil {
						t.Fatal(err)
					}
				}
				set, del := migrationSet, migrationDelete
				defer func() { migrationSet, migrationDelete = set, del }()
				candidate := ""
				migrationSet = func(service, user, value string) error {
					candidate = user
					if stage == "keyring" {
						return errors.New("keyring unavailable")
					}
					if err := set(service, user, value); err != nil {
						return err
					}
					if stage == "keyring_ack" {
						return errors.New("keyring acknowledgement unavailable")
					}
					return nil
				}
				migrationDelete = func(service, user string) error {
					if stage != "folder" {
						return errors.New("keyring cleanup unavailable")
					}
					return del(service, user)
				}
				if err := f.core.Link(struct{ AccountID, Server, Name string }{"legacy", f.server.URL, "computer"}, &struct{}{}); err == nil {
					t.Fatal("precommit fault accepted replacement")
				}
				if got, err := keyring.Get(keyringService, old); err != nil || got != saved {
					t.Fatal("precommit failure overwrote original credential bytes")
				}
				if d.token != token || d.key != key || d.pin != pin || d.pins != heldSession || d.state != state || d.credentialID() != old || d.db.Get("credential_slot") != slot || d.db.Get("account_id") != identity || d.root != root || d.dir != dir {
					t.Fatal("precommit failure changed original session, identity or storage")
				}
				if ws, err := d.db.Workspaces(); err != nil || len(ws) != 1 || ws[0] != binding {
					t.Fatal("precommit failure changed workspace bindings")
				}
				if data, err := os.ReadFile(file); err != nil || string(data) != "local edit" {
					t.Fatal("precommit failure changed local file")
				}
				if got, _ := keyring.Get(keyringService, other); got != "other credential" {
					t.Fatal("precommit failure touched another account")
				}
				if stage != "folder" && (candidate == old || d.db.Get("credential_migration") != candidate) {
					t.Fatal("uncommitted credential lacks independently owned retry marker")
				}
				migrationSet, migrationDelete = set, del
				if stage == "database" {
					if _, err := db.Exec(`DROP TRIGGER refuse_pointer`); err != nil {
						t.Fatal(err)
					}
				}
				// Simulate process loss before cleanup: restart follows the old durable pointer.
				if err := d.db.Close(); err != nil {
					t.Fatal(err)
				}
				restored, err := newDaemon(dir, root, "")
				if err != nil {
					t.Fatal(err)
				}
				defer restored.db.Close()
				restored.http, restored.open = d.http, d.open
				if restored.token != token || restored.pin != pin || restored.credentialID() != old || restored.db.Get("credential_migration") != "" {
					t.Fatal("restart activated staged credentials or lost original pin")
				}
				if key != nil && (restored.key == nil || !key.Equal(restored.key)) {
					t.Fatal("restart changed original signing key")
				}
				if revoked && restored.state != "revoked" {
					t.Fatal("restart revived revoked token")
				}
				if candidate != "" && candidate != old {
					if _, err := keyring.Get(keyringService, candidate); !errors.Is(err, keyring.ErrNotFound) {
						t.Fatal("restart did not retire abandoned stage")
					}
				}
				if stage != "folder" {
					if !revoked {
						restored.state = "signed_out"
					}
					if err := (&Core{d: restored, ctx: t.Context()}).Link(LinkArgs{Server: f.server.URL}, &struct{}{}); err != nil {
						t.Fatal("replacement failed after recovery", err)
					}
					restored.pass(t.Context(), false)
					if restored.state != "idle" || restored.lastSync == 0 || restored.token == token || restored.pin != pin {
						t.Fatal("replacement did not resume pinned sync")
					}
				}
			})
		}
	}
}

func TestReplacementCommitAndRestartKeepNewPhysicalSlot(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		for _, fault := range []string{"delete", "marker"} {
			t.Run(fmt.Sprintf("%s_revoked_%t", fault, revoked), func(t *testing.T) {
				f := newAccountLinkFixture(t)
				if err := f.linkPrimary(); err != nil {
					t.Fatal(err)
				}
				d := f.primary
				// Start from an already replaced physical slot, not just the canonical legacy item.
				d.state = "signed_out"
				if err := f.linkPrimary(); err != nil {
					t.Fatal(err)
				}
				if revoked {
					f.revoked.Store(d.token, true)
					d.pass(t.Context(), false)
				} else {
					d.state = "signed_out"
				}
				old, identity, pin := d.credentialID(), d.db.Get("account_id"), d.pin
				before, _ := keyring.Get(keyringService, old)
				db, err := sql.Open("sqlite", filepath.Join(d.dir, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if fault == "marker" {
					if _, err := db.Exec(`CREATE TRIGGER refuse_cleanup BEFORE UPDATE ON config
WHEN NEW.key='credential_migration' AND NEW.value=''
BEGIN SELECT RAISE(FAIL,'cleanup marker unavailable'); END`); err != nil {
						t.Fatal(err)
					}
				}
				del := migrationDelete
				defer func() { migrationDelete = del }()
				migrationDelete = func(service, user string) error {
					if user == old && fault == "delete" {
						return errors.New("keyring cleanup unavailable")
					}
					return del(service, user)
				}
				if err := f.linkPrimary(); err != nil {
					t.Fatal("durable replacement failed activation", err)
				}
				slot, token, key := d.credentialID(), d.token, d.key
				saved, err := keyring.Get(keyringService, slot)
				if err != nil {
					t.Fatal(err)
				}
				if slot == old || d.db.Get("account_id") != identity || d.db.Get("credential_migration") != old || token == "" || d.state != "idle" || d.pin != pin {
					t.Fatal("committed replacement did not install its exact session/pointer")
				}
				gotToken, gotKey, gotPin, err := loadCredential(slot)
				if err != nil || gotToken != token || gotKey == nil || !gotKey.Equal(key) || gotPin != pin {
					t.Fatal("committed physical item differs from in-memory session")
				}
				if fault == "delete" {
					if got, _ := keyring.Get(keyringService, old); got != before {
						t.Fatal("failed retirement changed old item")
					}
				}
				if err := d.db.Close(); err != nil {
					t.Fatal(err)
				}
				if fault == "marker" {
					if _, err := db.Exec(`DROP TRIGGER refuse_cleanup`); err != nil {
						t.Fatal(err)
					}
				}
				// Crash after pointer commit: startup must never migrate the pointer back to canonical.
				restored, err := newDaemon(d.dir, d.root, "")
				if err != nil {
					t.Fatal(err)
				}
				defer restored.db.Close()
				restored.http, restored.open = d.http, d.open
				if restored.credentialID() != slot {
					t.Fatal("restart lost committed physical pointer")
				}
				if fault == "delete" && (restored.state != "signed_out" || !restored.credentialUnread) {
					t.Fatal("unavailable retirement did not leave live recovery state")
				}
				migrationDelete = del
				restored.pass(t.Context(), false)
				if restored.token != token || restored.key == nil || !restored.key.Equal(key) || restored.pin != pin || restored.state != "idle" || restored.lastSync == 0 || restored.credentialID() != slot || restored.db.Get("credential_migration") != "" {
					t.Fatal("recovered replacement did not resume exact new session and sync")
				}
				if got, _ := keyring.Get(keyringService, slot); got != saved {
					t.Fatal("recovery rewrote committed credential bytes")
				}
				if _, err := keyring.Get(keyringService, old); !errors.Is(err, keyring.ErrNotFound) {
					t.Fatal("recovery retained old physical item")
				}
			})
		}
	}
}
