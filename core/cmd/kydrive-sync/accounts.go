package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

type AccountArgs struct{ AccountID string }
type AccountStatus struct {
	ID, Server, User, Issuer, Sub, OrgID, Root, State string
	Linked                                            bool
}

// Each account owns a daemon, database, watcher, credential and retry memory.
// There is no process-wide selected account: every RPC resolves its own selector.
type accountManager struct {
	started  bool
	mu       sync.Mutex
	add      sync.Mutex
	primary  *daemon // the old single-account installation, preserved in place
	accounts map[string]*daemon
	pending  *daemon
	ctx      context.Context
}

func newAccountManager(ctx context.Context, primary *daemon) (*accountManager, error) {
	m := &accountManager{primary: primary, accounts: make(map[string]*daemon), ctx: ctx}
	entries, err := os.ReadDir(filepath.Join(primary.dir, "accounts"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validAccountID(entry.Name()) {
			continue
		}
		id := entry.Name()
		d, err := newDaemon(filepath.Join(primary.dir, "accounts", id), accountRoot(primary.root, id), "")
		if err != nil {
			return nil, err
		}
		if d.db.Get("storage_id") != id {
			d.db.Close()
			return nil, errors.New("account state does not match its identity")
		}
		d.prepareAccount = func(server string, tok api.Token) error { return m.checkAccount(d, server, tok) }
		m.accounts[id] = d
	}
	primary.prepareAccount = func(server string, tok api.Token) error { return m.checkAccount(primary, server, tok) }
	return m, nil
}

// Sibling roots keep even an old workspace named "accounts" disjoint. The
// Flatpak grants this parent folder explicitly, as it does for ~/KyDrive.
func accountRoot(root, id string) string { return filepath.Join(root+" Accounts", id) }

func (m *accountManager) resolve(id string) (*Core, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "legacy" {
		return &Core{d: m.primary, ctx: m.ctx}, nil
	}
	if id == "" {
		if m.primary.db.Get("server") != "" || len(m.accounts) == 0 {
			return &Core{d: m.primary, ctx: m.ctx}, nil
		}
		var only *daemon
		for _, d := range m.accounts {
			if d.db.Get("server") != "" {
				if only != nil {
					return nil, errors.New("choose a KyDrive account")
				}
				only = d
			}
		}
		if only == nil {
			return &Core{d: m.primary, ctx: m.ctx}, nil
		}
		return &Core{d: only, ctx: m.ctx}, nil
	}
	d := m.accounts[id]
	if d == nil {
		return nil, errors.New("unknown KyDrive account")
	}
	return &Core{d: d, ctx: m.ctx}, nil
}

func (m *accountManager) list() []AccountStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []AccountStatus{}
	add := func(id string, d *daemon) {
		if d.db.Get("server") == "" {
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		a := d.account()
		out = append(out, AccountStatus{ID: id, Server: d.db.Get("server"), User: a.User, Issuer: a.Issuer, Sub: a.Sub, OrgID: a.OrgID, Root: d.root, State: d.state, Linked: d.token != ""})
	}
	add("legacy", m.primary)
	for id, d := range m.accounts {
		add(id, d)
	}
	slices.SortFunc(out, func(a, b AccountStatus) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out
}

func (m *accountManager) start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = true
	for _, d := range m.accounts {
		go d.run(m.ctx)
	}
}

// Approval callers hold m.add through checking and publication, including browser waits.
// The owner may renew its own credential, but cannot publish another account's identity.
func (m *accountManager) checkAccount(owner *daemon, server string, tok api.Token) error {
	id := accountID(server, tokenAccount(tok))
	if id == "" { // identity-less old servers remain supported by primary Link
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range append([]*daemon{m.primary}, values(m.accounts)...) {
		if existing != owner && existing.db.Get("server") != "" && accountID(existing.db.Get("server"), existing.account()) == id {
			return errors.New("this account is already added; select it to sign in again")
		}
	}
	return nil
}

// AddAccount always uses a fresh, privately owned staging database. No browser
// approval can replace another account's credential or bindings.
func (m *accountManager) addAccount(in LinkArgs, out *AccountStatus) error {
	if !m.add.TryLock() {
		return errors.New("an account is already being added")
	}
	defer m.add.Unlock()
	dir := filepath.Join(m.primary.dir, "accounts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("could not create account state directory")
	}
	staging, err := os.MkdirTemp(dir, ".pending-")
	if err != nil {
		return errors.New("could not create account state")
	}
	defer os.RemoveAll(staging)
	d, err := newDaemon(staging, "", "")
	if err != nil {
		return err
	}
	committed := false
	cleanupKey := ""
	defer func() {
		if d != nil {
			if !committed && d.token != "" {
				d.revoke(d.db.Get("server"), api.Token{Token: d.token, Key: d.key, Pin: d.pin})
				if cleanupKey != "" {
					keyring.Delete(keyringService, cleanupKey)
				}
			}
			d.db.Close()
		}
	}()
	d.http, d.open = m.primary.http, m.primary.open
	var storageID string
	var retained *daemon
	d.prepareAccount = func(server string, t api.Token) error {
		id := accountID(server, tokenAccount(t))
		if id == "" {
			return errors.New("KyDrive did not supply an immutable account identity; update the server before adding accounts")
		}
		if err := m.checkAccount(d, server, t); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()

		storageID = id
		d.root = accountRoot(m.primary.root, id)
		// Credential identity can migrate; the first storage selector and root stay owned.
		for selector, existing := range m.accounts {
			if existing.db.Get("account_id") != id {
				continue
			}
			if existing.db.Get("wipe") != "" {
				return errors.New("this account still has an unfinished file removal; retry when it completes")
			}
			storageID, retained, d.root = selector, existing, existing.root
			break
		}
		return nil
	}
	m.mu.Lock()
	m.pending = d
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.pending = nil; m.mu.Unlock() }()
	if err := (&Core{d: d, ctx: m.ctx}).Link(in, &struct{}{}); err != nil {
		return err
	}
	cleanupKey = d.credentialID()
	if err := d.db.Set("storage_id", storageID); err != nil {
		return err
	}
	if retained != nil {
		unlock := retained.lockWork()
		defer unlock()
		if retained.db.Get("server") != "" {
			return errors.New("account is already linked")
		}
		if err := retained.activate(d.db.Get("server"), d.db.Get("device"), api.Token{Token: d.token, Key: d.key, Pin: d.pin, User: d.db.Get("user"), UserID: d.db.Get("user_id"), Issuer: d.db.Get("issuer"), Sub: d.db.Get("sub"), OrgID: d.db.Get("org_id"), DevicesURL: d.db.Get("devices_url")}); err != nil {
			return err
		}
		*out = accountSummary(storageID, retained)
		committed = true
		return nil
	}
	if err := d.db.Close(); err != nil {
		return err
	}
	if err := os.Rename(staging, filepath.Join(dir, storageID)); err != nil {
		return errors.New("could not commit account state")
	}
	d.dir = filepath.Join(dir, storageID)
	db, openErr := engine.Open(filepath.Join(d.dir, "state.db"))
	if openErr != nil {
		return openErr
	}
	d.db = db
	owner := d
	d.prepareAccount = func(server string, tok api.Token) error { return m.checkAccount(owner, server, tok) }
	m.mu.Lock()
	m.accounts[storageID] = d
	started := m.started
	m.mu.Unlock()
	*out = accountSummary(storageID, d)
	if started {
		go d.run(m.ctx)
	}
	committed = true
	d = nil // manager owns the open database now
	return nil
}

func values(accounts map[string]*daemon) []*daemon {
	out := make([]*daemon, 0, len(accounts))
	for _, d := range accounts {
		out = append(out, d)
	}
	return out
}

type AccountsCore struct{ m *accountManager }

func (c *AccountsCore) Accounts(_ struct{}, out *[]AccountStatus) error {
	*out = c.m.list()
	return nil
}
func (c *AccountsCore) AddAccount(in LinkArgs, out *AccountStatus) error {
	return c.m.addAccount(in, out)
}

func (c *AccountsCore) Status(in struct{ AccountID string }, out *Status) error {
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	if err := core.Status(struct{}{}, out); err != nil {
		return err
	}
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	if core.d == c.m.primary {
		out.AccountID = "legacy"
	} else {
		for id, d := range c.m.accounts {
			if d == core.d {
				out.AccountID = id
				break
			}
		}
	}
	return nil
}

func (c *AccountsCore) SignIn(in struct{ AccountID string }, out *struct{}) error {
	if !c.m.add.TryLock() {
		return errors.New("finish or cancel adding an account first")
	}
	defer c.m.add.Unlock()
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.SignIn(struct{}{}, out)
}

func (c *AccountsCore) Workspaces(in struct{ AccountID string }, out *[]Choice) error {
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.Workspaces(struct{}{}, out)
}

func (c *AccountsCore) Conflicts(in struct{ AccountID string }, out *[]engine.Issue) error {
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.Conflicts(struct{}{}, out)
}

func (c *AccountsCore) Activity(in struct {
	AccountID string
	After     int64
}, out *[]engine.Activity) error {
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.Activity(struct{ After int64 }{in.After}, out)
}

func (c *AccountsCore) Choose(in struct {
	AccountID string
	IDs       []string
}, out *struct{}) error {
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.Choose(struct{ IDs []string }{in.IDs}, out)
}

func (c *AccountsCore) Pause(in struct {
	AccountID string
	Paused    bool
}, out *struct{}) error {
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.Pause(struct{ Paused bool }{in.Paused}, out)
}

func (c *AccountsCore) ConfirmDeletes(in struct {
	AccountID string
	Workspace string
	Allow     bool
}, out *struct{}) error {
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.ConfirmDeletes(struct {
		Workspace string
		Allow     bool
	}{in.Workspace, in.Allow}, out)
}

func (c *AccountsCore) Unlink(in struct {
	AccountID   string
	RemoveFiles bool
}, out *struct{}) error {
	if !c.m.add.TryLock() {
		return errors.New("finish or cancel adding an account first")
	}
	defer c.m.add.Unlock()
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.Unlink(struct{ RemoveFiles bool }{in.RemoveFiles}, out)
}

func (c *AccountsCore) RemoveAccount(in struct {
	AccountID   string
	RemoveFiles bool
}, out *struct{}) error {
	if !c.m.add.TryLock() {
		return errors.New("finish or cancel adding an account first")
	}
	defer c.m.add.Unlock()
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.Unlink(struct{ RemoveFiles bool }{in.RemoveFiles}, out)
}

func (c *AccountsCore) Link(in struct{ AccountID, Server, Name string }, out *struct{}) error {
	if !c.m.add.TryLock() {
		return errors.New("finish or cancel adding an account first")
	}
	defer c.m.add.Unlock()
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}

	if core.d != c.m.primary {
		return errors.New("use AddAccount to add an account, or SignIn to restore its link")
	}
	return core.Link(LinkArgs{Server: in.Server, Name: in.Name}, out)
}
func (c *AccountsCore) CancelLink(in AccountArgs, out *struct{}) error {
	c.m.mu.Lock()
	d := c.m.pending
	c.m.mu.Unlock()
	if d != nil {
		return (&Core{d: d, ctx: c.m.ctx}).CancelLink(struct{}{}, out)
	}
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.CancelLink(struct{}{}, out)
}
func (c *AccountsCore) Quit(_ struct{}, out *struct{}) error {
	// Quit is process-wide. Reserve every account so an idle selected account
	// cannot terminate another one's in-flight upload or browser approval.
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	if c.m.pending != nil {
		return errors.New("an account is waiting for browser approval")
	}
	all := append([]*daemon{c.m.primary}, values(c.m.accounts)...)
	locked := []*daemon{}
	for _, d := range all {
		if !d.work.TryLock() {
			for _, v := range locked {
				v.work.Unlock()
			}
			return errors.New("KyDrive is syncing; try again shortly")
		}
		locked = append(locked, d)
		d.mu.Lock()
		busy := d.linking != nil
		d.mu.Unlock()
		if busy {
			for _, v := range locked {
				v.work.Unlock()
			}
			return errors.New("an account is waiting for browser approval")
		}
	}

	go func() { time.Sleep(200 * time.Millisecond); c.m.primary.exit(0) }()
	return nil
}

func accountSummary(id string, d *daemon) AccountStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.account()
	return AccountStatus{ID: id, Server: d.db.Get("server"), User: a.User, Issuer: a.Issuer, Sub: a.Sub, OrgID: a.OrgID, Root: d.root, State: d.state, Linked: d.token != ""}
}
