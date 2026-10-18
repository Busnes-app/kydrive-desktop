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

func main() {
	log.SetFlags(0)
	dir, root, sock, err := paths()
	if err != nil {
		log.Fatal(err)
	}
	switch {
	case len(os.Args) > 1 && os.Args[1] == "ctl":
		os.Exit(ctl(sock, os.Args[2:]))
	case len(os.Args) > 1:
		fmt.Fprintln(os.Stderr, "usage: kydrive-sync [ctl METHOD [JSON-PARAMS]]")
		os.Exit(2)
	}
	d, err := newDaemon(dir, root, sock)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := d.listen()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go d.serve(ctx, ln)
	d.run(ctx)
	ln.Close()
	d.db.Close()
}

// paths are the per-user config directory, the sync root and the socket.
func paths() (dir, root, sock string, err error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return
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

func ctl(sock string, args []string) int {
	if len(args) == 0 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: kydrive-sync ctl METHOD [JSON-PARAMS]\nmethods: Status Link Unlink Workspaces Choose Pause Conflicts Activity ConfirmDeletes")
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
