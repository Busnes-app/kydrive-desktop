package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/zalando/go-keyring"
)

// OS keyring boundary; tests inject availability failures without changing its store.
var errKeyringUnavailable = errors.New("OS keyring unavailable; unlock it and KyDrive will retry")

var migrationGet = keyring.Get
var migrationSet = keyring.Set
var migrationDelete = keyring.Delete

// Hash a length-delimited tuple, never a username or token. Legacy local IDs have
// their own namespace; they are not KyIdentity subjects.
func accountID(server string, a api.Account) string {
	namespace := "kyidentity"
	if a.Issuer == "" && a.Sub == "" && a.OrgID == "" {
		if a.UserID == "" {
			return ""
		}
		namespace, a.Issuer, a.Sub = "kydrive-local", server, a.UserID
	}
	if a.Issuer == "" || a.Sub == "" {
		return ""
	}
	b, _ := json.Marshal([]string{namespace, server, a.Issuer, a.Sub, a.OrgID})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func tokenAccount(t api.Token) api.Account {
	return api.Account{Issuer: t.Issuer, Sub: t.Sub, OrgID: t.OrgID, UserID: t.UserID, User: t.User}
}

func (d *daemon) account() api.Account {
	return api.Account{Issuer: d.db.Get("issuer"), Sub: d.db.Get("sub"), OrgID: d.db.Get("org_id"), UserID: d.db.Get("user_id"), User: d.db.Get("user")}
}

func (d *daemon) credentialID() string {
	if d.db.Get("server") == "" { // retained identity metadata no longer owns a credential
		return ""
	}
	if id := d.db.Get("account_id"); id != "" {
		return "account:" + id
	}
	if slot := d.db.Get("credential_slot"); slot != "" {
		return slot
	}
	return d.db.Get("server") // only an existing link whose identity is not known yet
}

// Copy, commit the pointer, then remove the old item. A crash at any boundary
// leaves at least one credential, and retry never overwrites a different link.
// Caller owns d.work (or the daemon has not started).
func (d *daemon) migrateCredential() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.migrateCredentialLocked()
}
func (d *daemon) migrateCredentialLocked() error {
	server := d.db.Get("server")
	account := d.account()
	id := accountID(server, account)
	if id == "" && (account.Issuer != "" || account.Sub != "" || account.OrgID != "") {
		return errors.New("stored KyIdentity account identity is incomplete")
	}
	if server == "" {
		return nil
	}
	if id == "" { // identity-less primary links still retire their previous item
		return d.finishCredentialMigrationLocked()
	}
	old := d.credentialID()
	target := "account:" + id
	if old == target {
		return d.finishCredentialMigrationLocked()
	}
	value, err := migrationGet(keyringService, old)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: could not read credential for migration", errKeyringUnavailable)
	}
	prior, err := migrationGet(keyringService, target)
	if err == nil && prior != value {
		return errors.New("this account already has a different device link")
	}
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("%w: could not read account credential", errKeyringUnavailable)
	}
	if err := migrationSet(keyringService, target, value); err != nil {
		return fmt.Errorf("%w: could not migrate account credential", errKeyringUnavailable)
	}
	if err := d.db.SetConfig(map[string]string{"credential_migration": old, "account_id": id, "credential_slot": target}); err != nil {
		return err
	}
	return d.migrateCredentialLocked()
}

// The marker was committed with the new credential pointer. Keep its old slot
// reserved until deletion and marker cleanup both succeed. Caller holds d.mu.
func (d *daemon) finishCredentialMigrationLocked() error {
	source := d.db.Get("credential_migration")
	if source == "" {
		return nil
	}
	if source != d.credentialID() {
		if err := migrationDelete(keyringService, source); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("%w: could not finish credential migration", errKeyringUnavailable)
		}
	}
	return d.db.Set("credential_migration", "")
}

func validAccountID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 32 && strings.ToLower(id) == id
}
