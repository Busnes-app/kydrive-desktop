// Command kydrive-sync is the KyDrive desktop sync core: one per user, driven by a native
// shell over JSON-RPC on an owner-only local socket.
//
//	kydrive-sync                      run the core
//	kydrive-sync ctl METHOD [PARAMS]  call a running core, e.g. ctl Status
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
)

// version is this release; docs/RELEASING.md lists it with the shells' version fields.
const version = "0.7.0"

func main() {
	log.SetFlags(0)
	dir, root, sock, err := paths()
	if err != nil {
		log.Fatal("could not initialize KyDrive; check state permissions and whether another core is running")
	}
	switch {
	case len(os.Args) > 1 && os.Args[1] == "ctl":
		os.Exit(ctl(sock, os.Args[2:]))
	case len(os.Args) > 1:
		fmt.Fprintln(os.Stderr, "usage: kydrive-sync [ctl METHOD [JSON-PARAMS]]")
		os.Exit(2)
	}
	if runtime.GOOS == "windows" {
		// Earlier releases kept state in roaming %APPDATA%.
		if cfg, err := os.UserConfigDir(); err == nil {
			if err := moveState(filepath.Join(cfg, "kydrive"), dir); err != nil {
				log.Fatal("could not initialize KyDrive; check state permissions and whether another core is running")
			}
		}
	}
	d, err := newDaemon(dir, root, sock)
	if err != nil {
		log.Fatal("could not initialize KyDrive; check state permissions and whether another core is running")
	}
	ln, err := d.listen()
	if err != nil {
		log.Fatal("could not initialize KyDrive; check state permissions and whether another core is running")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m, err := newAccountManager(ctx, d)
	if err != nil {
		log.Fatal("could not load KyDrive accounts")
	}
	d.manager = m
	m.start()
	go d.serve(ctx, ln)
	d.run(ctx)
	ln.Close()
	d.db.Close()
}

// paths are the per-user state directory, the sync root and the socket. Windows uses
// %LOCALAPPDATA%: a roaming state.db would reach another computer without its keyring token.
func paths() (dir, root, sock string, err error) {
	cfg, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		cfg, err = os.UserCacheDir()
	}
	if err != nil {
		return
	}
	if !filepath.IsAbs(cfg) {
		return "", "", "", fmt.Errorf("state directory is not absolute")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir = filepath.Join(cfg, "kydrive")
	sockDir := dir
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" && runtime.GOOS == "linux" {
		sockDir = filepath.Join(rt, "kydrive")
	}
	return dir, filepath.Join(home, "KyDrive"), filepath.Join(sockDir, "sync.sock"), nil
}

// moveState moves state.db and its WAL files from old to dir, once. It never overwrites state
// already in dir, and a failed move is an error, so the core never starts on an empty database
// beside an existing one.
func moveState(old, dir string) error {
	if _, err := os.Stat(filepath.Join(old, "state.db")); err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err == nil {
		log.Print("ignoring old roaming state: local state already exists")
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, n := range []string{"state.db-wal", "state.db-shm", "state.db"} { // the database last
		if err := os.Rename(filepath.Join(old, n), filepath.Join(dir, n)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not move old roaming state")
		}
	}
	return nil
}

func ctl(sock string, args []string) int {
	if len(args) == 0 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: kydrive-sync ctl METHOD [JSON-PARAMS]\nmethods: Accounts AddAccount RemoveAccount Status Link SignIn CancelLink Unlink Workspaces Choose Pause Conflicts Activity ConfirmDeletes Vault Quit")
		return 2
	}
	params := "{}"
	if len(args) == 2 {
		params = args[1]
	}
	var out json.RawMessage
	if err := call(sock, args[0], []byte(params), &out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var pretty any
	json.Unmarshal(out, &pretty)
	b, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Println(string(b))
	return 0
}
