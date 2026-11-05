package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/Busnes-app/kydrive-desktop/core/link"
	"github.com/zalando/go-keyring"
)

// Core is the JSON-RPC service the shells call, as "Core.<Method>".
type Core struct {
	d   *daemon
	ctx context.Context
}

type Status struct {
	Version    string // this core's release, for a shell that bundles a newer one (Quit)
	Linked     bool
	Server     string
	DevicesURL string // the account page where this computer can be revoked
	Root       string
	State      string
	Error      string
	Paused     bool
	LastSync   int64
	Workspaces []WorkspaceStatus
}

type WorkspaceStatus struct {
	ID, Name, Dir string
	HeldDeletes   int // local deletes waiting for ConfirmDeletes
}

func (c *Core) Status(_ struct{}, out *Status) error {
	d := c.d
	d.mu.Lock()
	*out = Status{Version: version, Linked: d.token != "", Server: d.db.Get("server"), DevicesURL: d.db.Get("devices_url"), Root: d.root,
		State: d.state, Error: d.lastErr, Paused: d.db.Get("paused") != "", LastSync: d.lastSync}
	d.mu.Unlock()
	ws, err := d.db.Workspaces()
	for _, w := range ws {
		out.Workspaces = append(out.Workspaces, WorkspaceStatus{ID: w.ID, Name: w.Name, Dir: w.Dir, HeldDeletes: w.Deletes})
	}
	return err
}

type LinkArgs struct{ Server, Name string }

// serverOrigin accepts https, or http only on a loopback host for development.
func serverOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return "", errors.New("enter the KyDrive address, e.g. https://drive.example.com")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return "", errors.New("KyDrive must be reached over https")
	}
	return u.Scheme + "://" + u.Host, nil
}

// platform is the server's name for this OS.
func platform() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

// deviceName makes name fit the server's name rule and conflict-copy file names: no path
// separators or control characters, at most 100 bytes.
func deviceName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return '-'
		case unicode.IsControl(r) || unicode.In(r, unicode.Cf):
			return -1
		}
		return r
	}, name)
	for len(name) > 100 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	return cmp.Or(strings.TrimSpace(name), "computer")
}

// pending is a link waiting for the browser; CancelLink and Unlink end it.
type pending struct{ cancel context.CancelFunc }

// begin marks a link as waiting once ok allows it (called under d.mu), replacing one already
// waiting: a shell started again does not know of it and could not cancel it. end clears the mark.
func (c *Core) begin(ok func() error) (ctx context.Context, end func(), err error) {
	d := c.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ok(); err != nil {
		return nil, nil, err
	}
	d.cancelLink()
	ctx, cancel := context.WithCancel(c.ctx)
	p := &pending{cancel}
	d.linking = p
	return ctx, func() {
		cancel()
		d.mu.Lock()
		if d.linking == p {
			d.linking = nil
		}
		d.mu.Unlock()
	}, nil
}

// cancelLink ends a link waiting for the browser. The caller holds d.mu.
func (d *daemon) cancelLink() {
	if d.linking != nil {
		d.linking.cancel()
		d.linking = nil
	}
}

func linkErr(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return errors.New("linking was cancelled")
	}
	return err
}

// Link pairs this computer through the browser; it returns once approved, or fails after
// ten minutes or CancelLink.
func (c *Core) Link(in LinkArgs, _ *struct{}) error {
	d := c.d
	server, err := serverOrigin(in.Server)
	if err != nil {
		return err
	}
	ctx, end, err := c.begin(func() error {
		if d.token != "" {
			return errors.New("already linked; unlink first")
		}
		return nil
	})
	if err != nil {
		return err
	}
	defer end()
	// A new link drops the old bindings, which an unfinished wipe still needs.
	if err := d.finishWipe(); err != nil {
		return err
	}
	host, _ := os.Hostname()
	name := deviceName(cmp.Or(strings.TrimSpace(in.Name), host))
	tok, err := link.Link(ctx, link.Request{Server: server, Name: name, Platform: platform(), Open: d.open, HTTP: d.http})
	if err != nil {
		return linkErr(ctx, err)
	}
	// An approved link starts a new connection; retained files keep no old sync binding.
	unlock := d.lockWork()
	defer unlock()
	if err := d.stillWanted(ctx, server, tok); err != nil {
		return err
	}
	previous, err := d.db.Workspaces()
	if err != nil {
		return err
	}
	for _, w := range previous {
		if err := d.db.DropWorkspace(w.ID); err != nil {
			return err
		}
	}
	return d.activate(server, name, tok)
}

// activate stores an approved credential and starts syncing with it. The caller holds d.work.
func (d *daemon) activate(server, name string, tok api.Token) error {
	if err := keyring.Set(keyringService, server, tok.Token); err != nil {
		return errors.New("could not store the credential in the OS keyring: " + err.Error())
	}
	for k, v := range map[string]string{"server": server, "device": name, "devices_url": tok.DevicesURL, "user": tok.User, "user_id": tok.UserID} {
		if err := d.db.Set(k, v); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(d.root, 0o700); err != nil {
		return err
	}
	d.mu.Lock()
	d.token, d.state, d.lastErr, d.backoff = tok.Token, "idle", "", 0
	d.mu.Unlock()
	d.trigger(true)
	return nil
}

// SignIn links this computer again to the server and account it syncs, keeping its workspaces,
// after KyDrive stopped accepting its credential. Another account is refused.
func (c *Core) SignIn(_ struct{}, _ *struct{}) error {
	d := c.d
	server, name := d.db.Get("server"), d.db.Get("device")
	ctx, end, err := c.begin(func() error {
		if server == "" || d.state != "signed_out" {
			return errors.New("this computer is not signed out")
		}
		return nil
	})
	if err != nil {
		return err
	}
	defer end()
	// The bindings it keeps would be dropped by an unfinished wipe once syncing again.
	if err := d.finishWipe(); err != nil {
		return err
	}
	tok, err := link.Link(ctx, link.Request{Server: server, Name: name, Platform: platform(), Open: d.open, HTTP: d.http})
	if err != nil {
		return linkErr(ctx, err)
	}
	if err := d.sameAccount(tok); err != nil {
		d.revoke(server, tok)
		return err
	}
	unlock := d.lockWork()
	defer unlock()
	if err := d.stillWanted(ctx, server, tok); err != nil {
		return err
	}
	return d.activate(server, name, tok)
}

// sameAccount refuses a sign-in as any account but the one recorded at link. An install
// with no immutable ID uses the legacy username check; with neither identity it is refused.
func (d *daemon) sameAccount(tok api.Token) error {
	if id := d.db.Get("user_id"); id != "" {
		if tok.UserID == id {
			return nil
		}
		return errors.New("the sign-in did not confirm the same KyDrive account; sign in with the original account, or unlink to use another account")
	}
	switch was := d.db.Get("user"); {
	case was == "":
		return errors.New("this computer was linked before KyDrive recorded which account it syncs, so it cannot sign in again; unlink, then link it")
	case !strings.EqualFold(tok.User, was): // the server's usernames are unique regardless of case
		return fmt.Errorf("you signed in as %s, but this computer syncs the files of %s; sign in as %s, or unlink to use another account", tok.User, was, was)
	}
	return nil
}

// stillWanted refuses an approval that arrived after CancelLink or Unlink, revoking it;
// otherwise the link is committed and nothing is left to cancel. The caller holds d.work,
// which Unlink takes after cancelling.
func (d *daemon) stillWanted(ctx context.Context, server string, tok api.Token) error {
	d.mu.Lock()
	cancelled := ctx.Err() != nil
	if !cancelled {
		d.linking = nil
	}
	d.mu.Unlock()
	if !cancelled {
		return nil
	}
	d.revoke(server, tok)
	return errors.New("linking was cancelled")
}

// revoke drops a credential the server issued but this computer will not use.
func (d *daemon) revoke(server string, tok api.Token) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := (&api.Client{Base: server, Token: tok.Token, HTTP: d.http}).RevokeSelf(ctx); err != nil {
		log.Printf("link: the server did not revoke an unused link: %v", err)
	}
}

// CancelLink ends a link or sign-in waiting for approval in the browser. One already approved
// and being saved cannot be cancelled.
func (c *Core) CancelLink(_ struct{}, _ *struct{}) error {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	if c.d.linking == nil {
		return errors.New("no link is waiting for approval")
	}
	c.d.cancelLink()
	return nil
}

// finishWipe completes a wipe the server asked for, before anything drops its bindings.
func (d *daemon) finishWipe() error {
	unlock := d.lockWork()
	defer unlock()
	if err := d.engine().ResumeWipe(); err != nil {
		return wipeUnfinished(err)
	}
	return nil
}

func wipeUnfinished(err error) error {
	return errors.New("files from this computer's previous link are not all removed yet; close any app using them and try again: " + err.Error())
}

// Unlink revokes this device on the server (best effort) and forgets the credential. Local
// files stay unless RemoveFiles, which still keeps unuploaded edits in Unsynced/.
func (c *Core) Unlink(in struct{ RemoveFiles bool }, _ *struct{}) error {
	d := c.d
	d.mu.Lock()
	d.cancelLink()
	d.mu.Unlock()
	unlock := d.lockWork()
	defer unlock()
	// Unlink drops the bindings, which an unfinished wipe still needs.
	if err := d.engine().ResumeWipe(); err != nil {
		return wipeUnfinished(err)
	}
	if e := d.engine(); e.API.Token != "" {
		ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
		if err := e.API.RevokeSelf(ctx); err != nil {
			log.Printf("unlink: the server did not revoke this computer: %v", err)
		}
		cancel()
	}
	var wiped error
	if in.RemoveFiles {
		// Recorded first, like a wipe the server asks for: a file that cannot go yet (open
		// elsewhere) is removed by a later pass, and Link waits for it.
		if err := d.engine().StartWipe(); err != nil {
			wiped = errors.New("unlinked, but files from this computer are not all removed yet; KyDrive removes them once no app is using them, and linking waits until then: " + err.Error())
		}
	} else {
		ws, err := d.db.Workspaces()
		for _, w := range ws {
			err = errors.Join(err, d.db.DropWorkspace(w.ID))
		}
		if err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.forget()
	d.state, d.lastErr = "unlinked", ""
	return errors.Join(wiped, d.db.Set("server", ""), d.db.Set("devices_url", ""), d.db.Set("user", ""))
}

type Choice struct {
	ID, Name, Kind, Role string
	Chosen               bool
}

func (c *Core) Workspaces(_ struct{}, out *[]Choice) error {
	d := c.d
	list, err := d.engine().API.Workspaces(c.ctx)
	if err != nil {
		return err
	}
	ws, err := d.db.Workspaces()
	*out = []Choice{}
	for _, s := range list {
		chosen := slices.ContainsFunc(ws, func(w engine.Workspace) bool { return w.ID == s.ID })
		*out = append(*out, Choice{ID: s.ID, Name: s.Name, Kind: s.Kind, Role: s.Role, Chosen: chosen})
	}
	return err
}

// Choose sets the synced workspaces; one dropped from the list is removed from this computer,
// keeping unuploaded edits in Unsynced/.
func (c *Core) Choose(in struct{ IDs []string }, _ *struct{}) error {
	d := c.d
	unlock := d.lockWork()
	err := d.engine().Choose(c.ctx, in.IDs)
	unlock()
	d.trigger(true)
	return err
}

func (c *Core) Pause(in struct{ Paused bool }, _ *struct{}) error {
	v := ""
	if in.Paused {
		v = "1"
		unlock := c.d.lockWork()
		defer unlock()
	}
	if err := c.d.db.Set("paused", v); err != nil {
		return err
	}
	if !in.Paused {
		c.d.trigger(true)
	}
	return nil
}

// Conflicts lists conflict copies, skipped names and files kept in Unsynced/.
func (c *Core) Conflicts(_ struct{}, out *[]engine.Issue) error {
	issues, err := c.d.db.Issues()
	*out = append([]engine.Issue{}, issues...)
	return err
}

func (c *Core) Activity(in struct{ After int64 }, out *[]engine.Activity) error {
	a, err := c.d.db.Activity(in.After)
	*out = append([]engine.Activity{}, a...)
	return err
}

// ConfirmDeletes answers the large-delete guard for a workspace.
func (c *Core) ConfirmDeletes(in struct {
	Workspace string
	Allow     bool
}, _ *struct{}) error {
	d := c.d
	unlock := d.lockWork()
	err := d.engine().ConfirmDeletes(in.Workspace, in.Allow)
	unlock()
	d.trigger(true)
	return err
}

// Quit ends this core so that a shell bundling a newer one can start it after an upgrade. It is
// refused while a pass or a link is under way (the shell asks again later), and holds the work
// lock until the exit so nothing starts meanwhile.
func (c *Core) Quit(_ struct{}, _ *struct{}) error {
	d := c.d
	if !d.work.TryLock() {
		return errors.New("KyDrive is syncing; try again shortly")
	}
	d.mu.Lock()
	busy := d.linking != nil
	d.mu.Unlock()
	if busy {
		d.work.Unlock()
		return errors.New("a link is waiting for approval in the browser")
	}
	log.Printf("quitting: a shell asked for its newer core")
	go func() {
		time.Sleep(200 * time.Millisecond) // the answer reaches the shell first
		d.db.Close()
		d.exit(0)
	}()
	return nil
}
