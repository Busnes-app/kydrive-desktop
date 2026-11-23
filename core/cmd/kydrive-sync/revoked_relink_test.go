package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

func TestRevokedSelectedAccountRelinksWithoutLosingBindings(t *testing.T) {
	for _, added := range []bool{false, true} {
		t.Run(fmt.Sprintf("added_%t", added), func(t *testing.T) {
			f := newAccountLinkFixture(t)
			who := f.identity.Load().(api.Account)
			selector, d := "legacy", f.primary
			if added {
				var a AccountStatus
				if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &a); err != nil {
					t.Fatal(err)
				}
				selector, d = a.ID, f.core.m.accounts[a.ID]
			} else if err := f.linkPrimary(); err != nil {
				t.Fatal(err)
			}
			otherWho := who
			otherWho.OrgID = "other"
			f.identity.Store(otherWho)
			var other AccountStatus
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &other); err != nil {
				t.Fatal(err)
			}
			otherDaemon := f.core.m.accounts[other.ID]
			otherSlot := otherDaemon.credentialID()
			otherSaved, _ := keyring.Get(keyringService, otherSlot)
			f.identity.Store(who)
			binding := engine.Workspace{ID: "docs", Name: "Docs", Dir: "Docs", Cursor: 10}
			if err := d.db.PutWorkspace(binding); err != nil {
				t.Fatal(err)
			}
			if err := d.db.PutItem(engine.Item{ID: "kept", Workspace: "docs", Kind: "file", Name: "kept.txt", Path: "kept.txt", State: "synced"}); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(d.root, "kept.txt")
			if err := os.WriteFile(file, []byte("local edit"), 0600); err != nil {
				t.Fatal(err)
			}
			slot, root, dir, oldToken := d.credentialID(), d.root, d.dir, d.token
			held := d.pin
			held.Floor = 123
			d.pin = held
			if err := storeCredential(slot, d.token, d.key, held); err != nil {
				t.Fatal(err)
			}
			f.revoked.Store(oldToken, true)
			d.pass(t.Context(), false)
			if d.state != "revoked" || d.token != "" || len(f.core.m.list()) != 2 {
				t.Fatal("fixture did not retain a server-revoked account")
			}
			if err := f.core.Link(struct{ AccountID, Server, Name string }{selector, f.server.URL, "computer"}, &struct{}{}); err != nil {
				t.Fatal("selected revoked account could not relink", err)
			}
			ws, err := d.db.Workspaces()
			if err != nil || len(ws) != 1 || ws[0] != binding {
				t.Fatal("revoked relink reset the retained workspace binding")
			}
			if item, err := d.db.Item("kept"); err != nil || item == nil {
				t.Fatal("revoked relink lost sync records")
			}
			if d.pin != held || d.token == "" || d.token == oldToken || d.credentialID() != slot || d.root != root || d.dir != dir {
				t.Fatal("revoked relink changed account storage or held pin")
			}
			d.lastSync = 0
			d.pass(t.Context(), false)
			if d.state != "idle" || d.lastSync == 0 {
				t.Fatal("relinked account did not resume actual sync")
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "local edit" {
				t.Fatal("relink changed local files")
			}
			if kept, _ := keyring.Get(keyringService, otherSlot); kept != otherSaved || otherDaemon.token == "" {
				t.Fatal("relink changed another account")
			}
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &AccountStatus{}); err == nil {
				t.Fatal("explicit AddAccount replaced an already retained identity")
			}
			// A revoked pin-only entry survives restart without reviving the token or key.
			f.revoked.Store(d.token, true)
			d.pass(t.Context(), false)
			if token, key, pin, err := loadCredential(slot); err != nil || token != "" || key != nil || pin != held {
				t.Fatal("revocation did not persist only the held pin")
			}
			client, open := f.primary.http, f.primary.open
			oldPrimary := f.primary
			for _, account := range f.core.m.accounts {
				account.db.Close()
			}
			oldPrimary.db.Close()
			primary, err := newDaemon(oldPrimary.dir, oldPrimary.root, "")
			if err != nil {
				t.Fatal(err)
			}
			primary.http, primary.open = client, open
			m, err := newAccountManager(t.Context(), primary)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, account := range m.accounts {
					account.db.Close()
				}
				primary.db.Close()
			})
			f.primary, f.core.m = primary, m
			d = primary
			if added {
				d = m.accounts[selector]
			}
			for _, account := range m.accounts {
				account.http, account.open = client, open
			}
			otherDaemon = m.accounts[other.ID]
			if d.state != "revoked" || d.token != "" || d.key != nil || d.pin != held {
				t.Fatal("restart lost the revoked pin or revived credentials")
			}
			opened := 0
			d.open = func(raw string) error { opened++; return open(raw) }
			linkSelected := func(server string) error {
				return f.core.Link(struct{ AccountID, Server, Name string }{selector, server, "computer"}, &struct{}{})
			}
			beforeIssue := f.issued.Load()
			if err := linkSelected("https://127.0.0.1:1"); err == nil || opened != 0 || f.issued.Load() != beforeIssue {
				t.Fatal("replacement did not refuse another server before opening a browser")
			}
			f.identity.Store(otherWho)
			if err := linkSelected(f.server.URL); err == nil {
				t.Fatal("replacement accepted another organization")
			}
			if f.issued.Load() != beforeIssue+1 {
				t.Fatal("wrong-identity approval did not exercise retained reauthentication")
			}
			if _, revoked := f.revoked.Load(fmt.Sprintf("kyd_link_%d", f.issued.Load())); !revoked {
				t.Fatal("wrong-identity approval was not revoked")
			}
			f.identity.Store(who)
			oldCert := f.server.TLS.Certificates[0]
			newCert, _ := tlsFixture(t, tlsKeyCase{leaf: "P256"})
			newLeaf, err := x509.ParseCertificate(newCert.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			client.Transport.(*http.Transport).TLSClientConfig.RootCAs.AddCert(newLeaf)
			f.server.CloseClientConnections()
			f.server.TLS.Certificates[0] = newCert
			beforeOpen, beforeIssue := opened, f.issued.Load()
			if err := linkSelected(f.server.URL); !errors.Is(err, errSignInPin) || opened != beforeOpen || f.issued.Load() != beforeIssue {
				t.Fatal("revoked reauthentication did not enforce the saved pin before browser/token exchange", err)
			}
			if token, key, pin, err := loadCredential(slot); err != nil || token != "" || key != nil || pin != held {
				t.Fatal("refused replacement changed the pin-only entry")
			}
			f.server.CloseClientConnections()
			f.server.TLS.Certificates[0] = oldCert
			if err := linkSelected(f.server.URL); err != nil {
				t.Fatal("correct owner could not relink after refusal", err)
			}
			d.pass(t.Context(), false)
			if d.state != "idle" || d.lastSync == 0 || d.pin != held || d.root != root || d.dir != dir {
				t.Fatal("restarted revoked account did not restore its retained session")
			}
			if ws, err := d.db.Workspaces(); err != nil || len(ws) != 1 || ws[0] != binding {
				t.Fatal("replacement changed retained workspace state")
			}
			if kept, _ := keyring.Get(keyringService, otherSlot); kept != otherSaved || otherDaemon.token == "" {
				t.Fatal("refused replacement changed another account")
			}
		})
	}
}

func TestRevokedPinSaveFailureWaitsForKeyringRecovery(t *testing.T) {
	f := newAccountLinkFixture(t)
	if err := f.linkPrimary(); err != nil {
		t.Fatal(err)
	}
	d := f.primary
	slot, pin := d.credentialID(), d.pin
	saved, _ := keyring.Get(keyringService, slot)
	f.revoked.Store(d.token, true)
	keyring.MockInitWithError(errors.New("keyring unavailable"))
	d.pass(t.Context(), false)
	if d.state != "revoked" || d.token != "" || d.key != nil || d.pin != pin || !d.credentialUnread {
		t.Fatal("unavailable keyring did not retain a blocked, pinned revoked session")
	}
	before := f.issued.Load()
	if err := f.linkPrimary(); !errors.Is(err, errCredentialUnread) || f.issued.Load() != before {
		t.Fatal("relink bypassed unavailable saved pin recovery", err)
	}
	keyring.MockInit()
	if err := keyring.Set(keyringService, slot, saved); err != nil {
		t.Fatal(err)
	}
	d.pass(t.Context(), false)
	if token, key, held, err := loadCredential(slot); err != nil || token != "" || key != nil || held != pin || d.credentialUnread {
		t.Fatal("keyring recovery did not retire the token/key and preserve pin")
	}
	if err := f.linkPrimary(); err != nil {
		t.Fatal(err)
	}
	d.pass(t.Context(), false)
	if d.state != "idle" || d.lastSync == 0 || d.pin != pin {
		t.Fatal("account did not resume pinned sync after recovery")
	}
}

func TestEndedPinOnlySessionCannotChangeLaterAccount(t *testing.T) {
	f := newAccountLinkFixture(t)
	if err := f.linkPrimary(); err != nil {
		t.Fatal(err)
	}
	d := f.primary
	f.revoked.Store(d.token, true)
	d.pass(t.Context(), false)
	d.mu.Lock()
	old := d.session().Store
	held := d.pin
	d.mu.Unlock()
	if err := f.unlinkPrimary(); err != nil {
		t.Fatal(err)
	}
	who := f.identity.Load().(api.Account)
	who.OrgID = "later"
	f.identity.Store(who)
	if err := f.linkPrimary(); err != nil {
		t.Fatal(err)
	}
	f.revoked.Store(d.token, true)
	d.pass(t.Context(), false)
	d.mu.Lock()
	d.pin = held // equal pins must not make two empty-token sessions interchangeable
	_ = d.session()
	d.mu.Unlock()
	if _, err := old.Current(); !errors.Is(err, errSessionEnded) {
		t.Fatal("ended pin-only session still reads a later account's pin")
	}
	next := held
	next.Floor++
	if old.Replace(held, next) || d.pin != held {
		t.Fatal("ended pin-only session changed a later account's pin")
	}
}
