package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

// Write the old tuple verbatim when constructing a pre-normalization installation.
func verbatimOriginID(server string, a api.Account) string {
	namespace := "kyidentity"
	if a.Issuer == "" && a.Sub == "" && a.OrgID == "" {
		namespace, a.Issuer, a.Sub = "kydrive-local", server, a.UserID
	}
	b, _ := json.Marshal([]string{namespace, server, a.Issuer, a.Sub, a.OrgID})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestAccountIdentityCanonicalizesOnlyServerOrigin(t *testing.T) {
	for _, who := range []api.Account{
		{UserID: "local"},
		{Issuer: "https://IDENTITY.example:443/", Sub: "Person", OrgID: "Org", UserID: "local"},
	} {
		want := accountID("https://drive.example", who)
		for _, origin := range []string{"https://DRIVE.example:443", "https://Drive.Example/", "https://drive.example:443/"} {
			if got := accountID(origin, who); got != want {
				t.Errorf("equivalent server origin changed account identity")
			}
		}
	}
	who := api.Account{Issuer: "https://IDENTITY.example:443/", Sub: "Person", OrgID: "Org"}
	original := accountID("https://drive.example", who)
	for _, changed := range []api.Account{
		{Issuer: "https://identity.example", Sub: who.Sub, OrgID: who.OrgID},
		{Issuer: who.Issuer, Sub: "person", OrgID: who.OrgID},
		{Issuer: who.Issuer, Sub: who.Sub, OrgID: "org"},
	} {
		if accountID("https://drive.example", changed) == original {
			t.Error("server normalization changed opaque issuer, subject or organization comparison")
		}
	}
}

func TestPersistedOriginKeepsAccountOwnership(t *testing.T) {
	const canonical, stored = "https://drive.example", "https://DRIVE.example:443"
	for _, added := range []bool{false, true} {
		for _, hosted := range []bool{false, true} {
			t.Run(fmt.Sprintf("added_%t_hosted_%t", added, hosted), func(t *testing.T) {
				f := newAccountLinkFixture(t)
				// Keep normal certificate/hostname verification while dialing only the local fixture.
				target, _ := url.Parse(f.server.URL)
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"drive.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
				der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
				if err != nil {
					t.Fatal(err)
				}
				leaf, err := x509.ParseCertificate(der)
				if err != nil {
					t.Fatal(err)
				}
				roots := x509.NewCertPool()
				roots.AddCert(leaf)
				f.server.TLS.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}
				transport := f.primary.http.Transport.(*http.Transport)
				transport.Proxy = nil
				transport.TLSClientConfig = &tls.Config{RootCAs: roots}
				transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, target.Host)
				}
				f.server.URL = canonical
				who := api.Account{UserID: "local", User: "person"}
				if hosted {
					who.Issuer, who.Sub, who.OrgID = "https://IDENTITY.example:443/", "Person", "Org"
				}
				f.identity.Store(who)
				f.tokens.Store("kyd_preserved", who)
				selector := "legacy"
				dir, root := f.primary.dir, f.primary.root
				source := stored
				metadata := map[string]string{"server": stored, "user": who.User, "user_id": who.UserID, "issuer": who.Issuer, "sub": who.Sub, "org_id": who.OrgID, "device": "computer", "kept": "account state"}
				if added {
					selector = verbatimOriginID(stored, who)
					dir = filepath.Join(f.primary.dir, "accounts", selector)
					root = accountRoot(f.primary.root, selector)
					source = "account:" + selector
					metadata["account_id"], metadata["credential_slot"], metadata["storage_id"] = selector, source, selector
					metadata["credential_migration"] = stored // earlier pointer commit, cleanup still pending
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				db := f.primary.db
				if added {
					db, err = engine.Open(filepath.Join(dir, "state.db"))
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := db.SetConfig(metadata); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if added {
					f.primary.db.Close()
				}
				pin := api.Pin{Leaf: api.SPKIPin(leaf), Floor: 1}
				for _, slot := range []string{source, stored} {
					if err := storeCredential(slot, "kyd_preserved", key, pin); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := keyring.Get(keyringService, source)
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(root, "kept.txt")
				if err := os.WriteFile(file, []byte("local edit"), 0600); err != nil {
					t.Fatal(err)
				}
				get, set, del := migrationGet, migrationSet, migrationDelete
				t.Cleanup(func() { migrationGet, migrationSet, migrationDelete = get, set, del })
				locked, copies := true, 0
				newSlot := "account:" + accountID(canonical, who)
				migrationGet = func(service, user string) (string, error) {
					if locked && !added && user == stored {
						return "", errors.New("source temporarily unavailable")
					}
					return get(service, user)
				}
				migrationSet = func(service, user, value string) error {
					if user == newSlot {
						copies++
					}
					return set(service, user, value)
				}
				migrationDelete = func(service, user string) error {
					if locked && added && user == stored {
						return errors.New("pending cleanup temporarily unavailable")
					}
					return del(service, user)
				}
				primary, err := newDaemon(f.primary.dir, f.primary.root, "")
				if err != nil {
					t.Fatal(err)
				}
				primary.http, primary.open = f.primary.http, f.primary.open
				m, err := newAccountManager(t.Context(), primary)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					for _, d := range m.accounts {
						d.db.Close()
					}
					primary.db.Close()
				})
				f.primary, f.core.m = primary, m
				d := primary
				if added {
					d = m.accounts[selector]
					d.http, d.open = primary.http, primary.open
				}
				if err := f.core.AddAccount(LinkArgs{Server: canonical}, &AccountStatus{}); err == nil {
					t.Fatal("canonical approval duplicated an account with a persisted mixed-case/default-port origin")
				}
				if added {
					if err := f.linkPrimary(); err == nil {
						t.Fatal("primary approval duplicated the persisted added account")
					}
				}
				if kept, _ := keyring.Get(keyringService, stored); kept != before || d.db.Get("server") != stored {
					t.Fatal("failed migration lost the original origin-keyed source")
				}
				if added && d.db.Get("credential_migration") != stored {
					t.Fatal("new identity migration discarded an earlier cleanup marker")
				}
				locked = false
				d.pass(t.Context(), false)
				if d.state != "idle" || d.lastSync == 0 || d.credentialID() != newSlot || d.token != "kyd_preserved" || !d.key.Equal(key) || d.pin != pin || copies != 1 {
					t.Fatal("origin migration did not recover the exact credential and sync")
				}
				if err := d.migrateCredential(); err != nil {
					t.Fatal(err)
				}
				if kept, _ := keyring.Get(keyringService, newSlot); kept != before || copies != 1 || d.db.Get("credential_migration") != "" {
					t.Fatal("repeated origin migration changed bytes or pending cleanup")
				}
				for _, old := range []string{stored, source} {
					if _, err := keyring.Get(keyringService, old); !errors.Is(err, keyring.ErrNotFound) {
						t.Fatal("origin migration did not retire the original source")
					}
				}
				// Restart after migration: the old stored spelling must not select another item/root.
				client, open := primary.http, primary.open
				for _, loaded := range m.accounts {
					loaded.db.Close()
				}
				primary.db.Close()
				primary, err = newDaemon(f.primary.dir, f.primary.root, "")
				if err != nil {
					t.Fatal(err)
				}
				primary.http, primary.open = client, open
				m, err = newAccountManager(t.Context(), primary)
				if err != nil {
					t.Fatal(err)
				}
				f.primary, f.core.m = primary, m
				d = primary
				if added {
					d = m.accounts[selector]
					d.http, d.open = client, open
				}
				if kept, _ := keyring.Get(keyringService, newSlot); kept != before || d.token != "kyd_preserved" || !d.key.Equal(key) || d.pin != pin || copies != 1 {
					t.Fatal("restart changed the migrated credential or selected another account")
				}
				d.pass(t.Context(), false)
				if d.state != "idle" || d.lastSync == 0 {
					t.Fatal("account did not sync after restarting with its original stored origin")
				}
				if added {
					if err := f.core.RemoveAccount(struct {
						AccountID   string
						RemoveFiles bool
					}{selector, false}, &struct{}{}); err != nil {
						t.Fatal(err)
					}
					var again AccountStatus
					if err := f.core.AddAccount(LinkArgs{Server: canonical}, &again); err != nil {
						t.Fatal(err)
					}
					if again.ID != selector || again.Root != root || len(m.accounts) != 1 || m.accounts[selector] != d {
						t.Fatal("origin migration/re-add forked account storage")
					}
					d.lastSync = 0
					d.pass(t.Context(), false)
					if d.state != "idle" || d.lastSync == 0 {
						t.Fatal("retained account did not resume sync after re-add")
					}
				}
				if d.dir != dir || d.root != root || d.db.Get("kept") != "account state" || (added && d.db.Get("storage_id") != selector) {
					t.Fatal("origin normalization changed storage ownership")
				}
				if data, err := os.ReadFile(file); err != nil || string(data) != "local edit" {
					t.Fatal("origin normalization changed retained files")
				}
			})
		}
	}
}

func TestRemoveBeforeOriginMigrationKeepsRetainedIdentity(t *testing.T) {
	f := newAccountLinkFixture(t)
	const canonical, stored = "https://127.0.0.1", "https://127.0.0.1:443"
	target, _ := url.Parse(f.server.URL)
	transport := f.primary.http.Transport.(*http.Transport)
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target.Host)
	}
	f.server.URL = canonical
	who := f.identity.Load().(api.Account)
	selector := verbatimOriginID(stored, who)
	source := "account:" + selector
	dir := filepath.Join(f.primary.dir, "accounts", selector)
	root := accountRoot(f.primary.root, selector)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := engine.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConfig(map[string]string{"server": stored, "user": who.User, "user_id": who.UserID, "issuer": who.Issuer, "sub": who.Sub, "org_id": who.OrgID, "account_id": selector, "credential_slot": source, "storage_id": selector, "kept": "original state"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := storeCredential(source, "kyd_previous", nil, api.Pin{}); err != nil {
		t.Fatal(err)
	}
	get := migrationGet
	defer func() { migrationGet = get }()
	locked := true
	migrationGet = func(service, user string) (string, error) {
		if locked && user == source {
			return "", errors.New("source unavailable")
		}
		return get(service, user)
	}
	m, err := newAccountManager(t.Context(), f.primary)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, d := range m.accounts {
			d.db.Close()
		}
	}()
	f.core.m = m
	d := m.accounts[selector]
	d.http, d.open = f.primary.http, f.primary.open
	if d.state != "signed_out" || d.db.Get("account_id") != selector {
		t.Fatal("fixture did not pause migration before its pointer commit")
	}
	if err := f.core.RemoveAccount(struct {
		AccountID   string
		RemoveFiles bool
	}{selector, false}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, source); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("removal did not delete through the original credential pointer")
	}
	locked = false
	var again AccountStatus
	if err := f.core.AddAccount(LinkArgs{Server: canonical}, &again); err != nil {
		t.Fatal(err)
	}
	if again.ID != selector || again.Root != root || m.accounts[selector] != d || len(m.accounts) != 1 || d.db.Get("kept") != "original state" {
		t.Fatal("removal before origin migration forked the retained account")
	}
	d.pass(t.Context(), false)
	if d.state != "idle" || d.lastSync == 0 {
		t.Fatal("retained account did not sync after early removal/re-add")
	}
}
