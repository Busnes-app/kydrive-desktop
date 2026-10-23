package main

import (
	"cmp"
	"context"
	"errors"
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
	*out = Status{Linked: d.token != "", Server: d.db.Get("server"), DevicesURL: d.db.Get("devices_url"), Root: d.root,
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

// Link pairs this computer through the browser; it returns once approved, or fails after
// ten minutes.
func (c *Core) Link(in LinkArgs, _ *struct{}) error {
	d := c.d
	server, err := serverOrigin(in.Server)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if d.token != "" || d.linking {
		d.mu.Unlock()
		return errors.New("already linked or linking; unlink first")
	}
	d.linking = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.linking = false; d.mu.Unlock() }()
	host, _ := os.Hostname()
	name := deviceName(cmp.Or(strings.TrimSpace(in.Name), host))
	tok, err := link.Link(c.ctx, link.Request{Server: server, Name: name, Platform: platform(), Open: d.open, HTTP: d.http})
	if err != nil {
		return err
	}
	// An approved link starts a new connection; retained files keep no old sync binding.
	d.work.Lock()
	defer d.work.Unlock()
	previous, err := d.db.Workspaces()
	if err != nil {
		return err
	}
	for _, w := range previous {
		if err := d.db.DropWorkspace(w.ID); err != nil {
			return err
		}
	}
	if err := keyring.Set(keyringService, server, tok.Token); err != nil {
		return errors.New("could not store the credential in the OS keyring: " + err.Error())
	}
	for k, v := range map[string]string{"server": server, "device": name, "devices_url": tok.DevicesURL} {
		if err := d.db.Set(k, v); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(d.root, 0o700); err != nil {
		return err
	}
	d.mu.Lock()
	d.token, d.state, d.lastErr = tok.Token, "idle", ""
	d.mu.Unlock()
	d.trigger(true)
	return nil
}

// Unlink revokes this device on the server (best effort) and forgets the credential. Local
// files stay unless RemoveFiles, which still keeps unuploaded edits in Unsynced/.
func (c *Core) Unlink(in struct{ RemoveFiles bool }, _ *struct{}) error {
	d := c.d
	d.work.Lock()
	defer d.work.Unlock()
	if e := d.engine(); e.API.Token != "" {
		ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
		if err := e.API.RevokeSelf(ctx); err != nil {
			log.Printf("unlink: the server did not revoke this computer: %v", err)
		}
		cancel()
	}
	ws, err := d.db.Workspaces()
	if err != nil {
		return err
	}
	if in.RemoveFiles {
		err = d.engine().Wipe()
	} else {
		for _, w := range ws {
			err = errors.Join(err, d.db.DropWorkspace(w.ID))
		}
	}
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.forget()
	d.state, d.lastErr = "unlinked", ""
	return errors.Join(d.db.Set("server", ""), d.db.Set("devices_url", ""))
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
	d.work.Lock()
	err := d.engine().Choose(c.ctx, in.IDs)
	d.work.Unlock()
	d.trigger(true)
	return err
}

func (c *Core) Pause(in struct{ Paused bool }, _ *struct{}) error {
	v := ""
	if in.Paused {
		v = "1"
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
	d.work.Lock()
	err := d.engine().ConfirmDeletes(in.Workspace, in.Allow)
	d.work.Unlock()
	d.trigger(true)
	return err
}
