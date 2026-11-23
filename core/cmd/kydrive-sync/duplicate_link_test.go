package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

type accountLinkFixture struct {
	server   *httptest.Server
	primary  *daemon
	core     *AccountsCore
	identity atomic.Value
	issued   atomic.Int32
	tokens   sync.Map
	revoked  sync.Map
}

func newAccountLinkFixture(t *testing.T) *accountLinkFixture {
	t.Helper()
	keyring.MockInit()
	f := &accountLinkFixture{}
	f.identity.Store(api.Account{Issuer: "https://identity.example", Sub: "person", OrgID: "b", UserID: "person-b", User: "person"})
	cert, roots := tlsFixture(t, tlsKeyCase{leaf: "P256"})
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/devices/token" {
			who := f.identity.Load().(api.Account)
			token := fmt.Sprintf("kyd_link_%d", f.issued.Add(1))
			f.tokens.Store(token, who)
			json.NewEncoder(w).Encode(api.Token{Token: token, Issuer: who.Issuer, Sub: who.Sub, OrgID: who.OrgID, UserID: who.UserID, User: who.User})
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		who, found := f.tokens.Load(token)
		if _, revoked := f.revoked.Load(token); !found || revoked {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/devices/self":
			f.revoked.Store(token, true)
			w.WriteHeader(204)
		case "/api/drive/device":
			json.NewEncoder(w).Encode(who)
		case "/api/drive/workspaces":
			json.NewEncoder(w).Encode([]api.Workspace{{ID: "docs", Name: "Docs", Kind: "group", Role: "reader", Head: 10}})
		case "/api/drive/workspaces/docs/changes":
			json.NewEncoder(w).Encode(api.Changes{Next: 10})
		case "/api/drive/workspaces/docs/snapshot":
			json.NewEncoder(w).Encode(api.Snapshot{Head: 10})
		default:
			http.NotFound(w, r)
		}
	}))
	f.server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	dir := t.TempDir()
	p, err := newDaemon(filepath.Join(dir, "state"), filepath.Join(dir, "KyDrive"), "")
	if err != nil {
		t.Fatal(err)
	}
	f.primary = p
	p.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
	p.open = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		go func() {
			res, err := http.Get(q.Get("redirect") + "?code=approved&state=" + url.QueryEscape(q.Get("state")))
			if err == nil {
				res.Body.Close()
			}
		}()
		return nil
	}
	m, err := newAccountManager(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	f.core = &AccountsCore{m: m}
	t.Cleanup(func() {
		for _, d := range m.accounts {
			d.db.Close()
		}
		p.db.Close()
	})
	return f
}

func (f *accountLinkFixture) linkPrimary() error {
	return f.core.Link(struct{ AccountID, Server, Name string }{"legacy", f.server.URL, "computer"}, &struct{}{})
}
func (f *accountLinkFixture) unlinkPrimary() error {
	return f.core.Unlink(struct {
		AccountID   string
		RemoveFiles bool
	}{"legacy", false}, &struct{}{})
}

func TestPrimaryLinkRejectsAddedIdentityBeforeMutation(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained_primary_%t", retained), func(t *testing.T) {
			f := newAccountLinkFixture(t)
			if retained {
				if err := f.linkPrimary(); err != nil {
					t.Fatal(err)
				}
				if err := f.unlinkPrimary(); err != nil {
					t.Fatal(err)
				}
			}
			var added AccountStatus
			if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &added); err != nil {
				t.Fatal(err)
			}
			b := f.core.m.accounts[added.ID]
			slot := b.credentialID()
			before, err := keyring.Get(keyringService, slot)
			if err != nil {
				t.Fatal(err)
			}
			key, pin, token := b.key, b.pin, b.token
			if key == nil || pin.Leaf == "" {
				t.Fatal("fixture did not establish signing key and TLS pin")
			}
			if err := b.db.Set("kept", "account b state"); err != nil {
				t.Fatal(err)
			}
			if err := b.db.PutWorkspace(engine.Workspace{ID: "docs", Name: "Docs", Dir: "Docs", Cursor: 10}); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(b.root, "kept.txt")
			if err := os.WriteFile(file, []byte("account b file"), 0600); err != nil {
				t.Fatal(err)
			}
			ownBinding := engine.Workspace{ID: "own", Name: "Drafts", Dir: "Drafts", Cursor: 17}
			if err := f.primary.db.PutWorkspace(ownBinding); err != nil {
				t.Fatal(err)
			}
			if err := f.linkPrimary(); err == nil {
				t.Fatal("duplicate primary approval was accepted")
			}
			duplicate := fmt.Sprintf("kyd_link_%d", f.issued.Load())
			if _, ok := f.revoked.Load(duplicate); !ok {
				t.Fatal("newly issued duplicate credential was not revoked")
			}
			own, _ := f.primary.db.Workspaces()
			if len(own) != 1 || own[0] != ownBinding {
				t.Fatal("rejected approval reset primary bindings")
			}
			if f.primary.token != "" {
				t.Fatal("rejected primary approval activated")
			}
			assertB := func() {
				t.Helper()
				if saved, _ := keyring.Get(keyringService, slot); saved != before || b.token != token || !b.key.Equal(key) || b.pin != pin {
					t.Fatal("another approval changed B's credential, signing key or pin")
				}
				if _, ok := f.revoked.Load(token); ok {
					t.Fatal("B's original device was revoked")
				}
				if b.db.Get("kept") != "account b state" {
					t.Fatal("B's state changed")
				}
				if data, err := os.ReadFile(file); err != nil || string(data) != "account b file" {
					t.Fatal("B's file changed")
				}
				ws, _ := b.db.Workspaces()
				if len(ws) != 1 || ws[0].Cursor != 10 {
					t.Fatal("B's workspace state changed")
				}
			}
			assertB()
			if err := f.unlinkPrimary(); err != nil {
				t.Fatal(err)
			}
			assertB()
			// A distinct primary approval must not retire an inactive primary's old B reference.
			who := f.identity.Load().(api.Account)
			who.OrgID = "a"
			who.UserID = "person-a"
			f.identity.Store(who)
			if err := f.linkPrimary(); err != nil {
				t.Fatal(err)
			}
			if f.primary.credentialID() == slot {
				t.Fatal("distinct primary shared B's keyring slot")
			}
			assertB()
			if err := f.unlinkPrimary(); err != nil {
				t.Fatal(err)
			}
			b.pass(t.Context(), false)
			if b.state != "idle" || b.lastSync == 0 {
				t.Fatal("B did not continue syncing")
			}
			assertB()
		})
	}
}

func TestPrimaryLegacyLinkStillWorks(t *testing.T) {
	f := newAccountLinkFixture(t)
	f.identity.Store(api.Account{User: "legacy"}) // old exchange, no immutable identity
	if err := f.linkPrimary(); err != nil {
		t.Fatal(err)
	}
	if f.primary.token == "" || !strings.HasPrefix(f.primary.credentialID(), "pending:") {
		t.Fatal("old-server primary link did not retain its private credential slot")
	}
	slot := f.primary.credentialID()
	if err := f.unlinkPrimary(); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, slot); err != keyring.ErrNotFound {
		t.Fatal("old-server primary credential survived unlink")
	}
}

func TestManagedSignInKeepsItsOwnIdentity(t *testing.T) {
	for _, primary := range []bool{false, true} {
		t.Run(fmt.Sprintf("primary_%t", primary), func(t *testing.T) {
			f := newAccountLinkFixture(t)
			d, id := f.primary, "legacy"
			if primary {
				if err := f.linkPrimary(); err != nil {
					t.Fatal(err)
				}
			} else {
				var added AccountStatus
				if err := f.core.AddAccount(LinkArgs{Server: f.server.URL}, &added); err != nil {
					t.Fatal(err)
				}
				d, id = f.core.m.accounts[added.ID], added.ID
			}
			binding := engine.Workspace{ID: "docs", Name: "Docs", Dir: "Docs", Cursor: 10}
			if err := d.db.PutWorkspace(binding); err != nil {
				t.Fatal(err)
			}
			slot, oldToken := d.credentialID(), d.token
			d.state = "signed_out"
			if err := f.core.SignIn(struct{ AccountID string }{id}, &struct{}{}); err != nil {
				t.Fatal(err)
			}
			if d.credentialID() != slot || d.token == oldToken || d.token == "" {
				t.Fatal("owner could not renew its own credential")
			}
			ws, err := d.db.Workspaces()
			if err != nil || len(ws) != 1 || ws[0] != binding {
				t.Fatal("renewal changed the owner's bindings")
			}
		})
	}
}

func TestAccountApprovalPathsKeepUniquenessSerialized(t *testing.T) {
	for _, first := range []string{"primary", "added"} {
		t.Run(first, func(t *testing.T) {
			f := newAccountLinkFixture(t)
			opened, release := make(chan struct{}), make(chan struct{})
			var block, unblock sync.Once
			approve := f.primary.open
			f.primary.open = func(raw string) error { block.Do(func() { close(opened); <-release }); return approve(raw) }
			done := make(chan error, 1)
			var added AccountStatus
			go func() {
				if first == "primary" {
					done <- f.linkPrimary()
				} else {
					done <- f.core.AddAccount(LinkArgs{Server: f.server.URL}, &added)
				}
			}()
			received := false
			defer func() {
				unblock.Do(func() { close(release) })
				if !received {
					<-done
				}
			}()
			<-opened // first browser approval is waiting with the existing manager lock held
			var err error
			if first == "primary" {
				err = f.core.AddAccount(LinkArgs{Server: f.server.URL}, &AccountStatus{})
			} else {
				err = f.linkPrimary()
			}
			if err == nil || f.issued.Load() != 0 {
				t.Fatal("overlapping approval passed the manager reservation")
			}
			unblock.Do(func() { close(release) })
			err = <-done
			received = true
			if err != nil {
				t.Fatal(err)
			}
			owner := f.primary
			if first == "added" {
				owner = f.core.m.accounts[added.ID]
			}
			slot := owner.credentialID()
			held, _ := keyring.Get(keyringService, slot)
			if first == "primary" {
				err = f.core.AddAccount(LinkArgs{Server: f.server.URL}, &AccountStatus{})
			} else {
				err = f.linkPrimary()
			}
			if err == nil {
				t.Fatal("second completed approval accepted the same identity")
			}
			if saved, _ := keyring.Get(keyringService, slot); saved != held {
				t.Fatal("rejected competing approval changed its owner's credential")
			}
		})
	}
}
