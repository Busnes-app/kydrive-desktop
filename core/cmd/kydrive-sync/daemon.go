package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

// keyringService names the OS keyring item; its user is the server origin.
const keyringService = "KyDrive"

const (
	pollEvery = 45 * time.Second
	scanEvery = 10 * time.Minute
	settle    = 3 * time.Second
)

type daemon struct {
	dir, root, sock string
	db              *engine.DB
	http            *http.Client
	open            func(url string) error

	work sync.Mutex // one engine operation at a time
	mu   sync.Mutex // guards the fields below
	token,
	state, // unlinked, idle, syncing, revoked, signed_out or error
	lastErr string
	lastSync int64
	linking  bool
	kick     chan bool // true asks for a local scan as well
}

func newDaemon(dir, root, sock string) (*daemon, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := engine.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		return nil, err
	}
	d := &daemon{dir: dir, root: root, sock: sock, db: db, open: openBrowser, kick: make(chan bool, 1), state: "unlinked",
		http: &http.Client{
			Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 30 * time.Second, ResponseHeaderTimeout: time.Minute},
			// Never send a token or link verifier to a redirected origin or HTTP downgrade.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}}
	if server := db.Get("server"); server != "" {
		tok, err := keyring.Get(keyringService, server)
		if err != nil {
			d.state, d.lastErr = "signed_out", "the keyring holds no KyDrive credential; link again"
		} else {
			d.token, d.state = tok, "idle"
		}
	}
	return d, nil
}

// listen opens the socket in an owner-only directory, refusing to replace a live core.
func (d *daemon) listen() (net.Listener, error) {
	dir := filepath.Dir(d.sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", d.sock, time.Second); err == nil {
		c.Close()
		return nil, errors.New("another kydrive-sync is already running")
	}
	os.Remove(d.sock)
	ln, err := net.Listen("unix", d.sock)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(d.sock, 0o600); err != nil && runtime.GOOS != "windows" {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (d *daemon) serve(ctx context.Context, ln net.Listener) {
	srv := rpc.NewServer()
	srv.RegisterName("Core", &Core{d: d, ctx: ctx})
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go srv.ServeCodec(jsonrpc.NewServerCodec(c))
	}
}

func call(sock, method string, params []byte, out any) error {
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return fmt.Errorf("kydrive-sync is not running: %w", err)
	}
	cl := jsonrpc.NewClient(c)
	defer cl.Close()
	return cl.Call("Core."+method, jsonRaw(params), out)
}

type jsonRaw []byte

func (r jsonRaw) MarshalJSON() ([]byte, error) { return r, nil }

func (d *daemon) trigger(local bool) {
	select {
	case d.kick <- local:
	default:
	}
}

// engine builds an engine for one operation. The caller must not hold d.mu.
func (d *daemon) engine() *engine.Engine {
	d.mu.Lock()
	token := d.token
	d.mu.Unlock()
	return &engine.Engine{Root: d.root, Device: d.db.Get("device"), DB: d.db, Settle: settle, Now: time.Now,
		API:      &api.Client{Base: d.db.Get("server"), Token: token, HTTP: d.http},
		CaseFold: runtime.GOOS == "windows" || runtime.GOOS == "darwin", WinNames: runtime.GOOS == "windows"}
}

// run polls every 45 s and right after a wake or network change, and scans locally at start,
// every 10 minutes and after file system events.
func (d *daemon) run(ctx context.Context) {
	go d.watch(ctx)
	poll, scan, probe := time.NewTicker(pollEvery), time.NewTicker(scanEvery), time.NewTicker(5*time.Second)
	defer poll.Stop()
	defer scan.Stop()
	defer probe.Stop()
	wall, addrs := time.Now().Round(0), netPrint()
	d.pass(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			d.pass(ctx, false)
		case <-scan.C:
			d.pass(ctx, true)
		case local := <-d.kick:
			d.pass(ctx, local)
		case now := <-probe.C:
			// Suspend stops the monotonic clock but not the wall clock.
			now = now.Round(0)
			woke := now.Sub(wall) > 15*time.Second
			wall = now
			if a := netPrint(); woke || a != addrs {
				addrs = a
				d.pass(ctx, false)
			}
		}
	}
}

// netPrint fingerprints the machine's addresses, which change with the network.
func netPrint() string {
	addrs, _ := net.InterfaceAddrs()
	var s []string
	for _, a := range addrs {
		s = append(s, a.String())
	}
	slices.Sort(s)
	return strings.Join(s, ",")
}

func (d *daemon) pass(ctx context.Context, local bool) {
	d.mu.Lock()
	if d.token == "" || d.db.Get("paused") != "" || d.state == "revoked" || d.state == "signed_out" {
		d.mu.Unlock()
		return
	}
	d.state = "syncing"
	d.mu.Unlock()

	d.work.Lock()
	e := d.engine()
	var err error
	if local {
		err = e.Sync(ctx)
	} else {
		err = e.Poll(ctx)
	}
	d.work.Unlock()
	if e.Unsettled {
		time.AfterFunc(settle+time.Second, func() { d.trigger(true) })
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case errors.Is(err, engine.ErrRevoked):
		d.state, d.lastErr = "revoked", err.Error()
		d.forget()
	case errors.Is(err, engine.ErrSignedOut):
		d.state, d.lastErr = "signed_out", err.Error()
	case err != nil:
		d.state, d.lastErr = "error", err.Error()
		log.Printf("sync: %v", err)
	default:
		d.state, d.lastErr, d.lastSync = "idle", "", time.Now().Unix()
	}
}

// forget deletes the credential from the keyring. The caller holds d.mu.
func (d *daemon) forget() {
	if err := keyring.Delete(keyringService, d.db.Get("server")); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		log.Printf("keyring: %v", err)
	}
	d.token = ""
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
