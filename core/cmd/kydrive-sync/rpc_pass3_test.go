package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// O-R3 (pass 3): a shell started again knows nothing of a link still waiting in the browser;
// a new Link replaces it instead of refusing for ten minutes.
func TestLinkReplacesAPendingLink(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	opened := make(chan struct{}, 1)
	auto := d.open
	d.open = func(string) error { opened <- struct{}{}; return nil } // never approved
	done := make(chan error, 1)
	go func() { var out struct{}; done <- ctl("Link", LinkArgs{Server: srv.URL}, &out) }()
	<-opened
	d.open = auto
	var out struct{}
	if err := ctl("Link", LinkArgs{Server: srv.URL}, &out); err != nil {
		t.Fatalf("a new link while one waits: %v", err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("the replaced link answered %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced link still waits")
	}
}

// S-R5 (pass 3): Sign in again waits for an unfinished wipe, which would otherwise drop the
// bindings it keeps once syncing resumes.
func TestSignInWaitsForAPendingWipe(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permission bits do not stop this user")
	}
	srv := server(t)
	d, ctl := start(t, srv)
	signedOut(t, d, ctl, srv.URL)
	ro := filepath.Join(d.root, "Docs", "ro")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ro, "kept.txt"), []byte("x"), 0o600)
	os.Chmod(ro, 0o555)
	defer os.Chmod(ro, 0o755)
	d.db.Set("wipe", "1")
	var out struct{}
	if err := ctl("SignIn", struct{}{}, &out); err == nil || !strings.Contains(err.Error(), "not all removed") {
		t.Fatalf("signed in over an unfinished wipe: %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state != "signed_out" {
		t.Fatalf("state %q", d.state)
	}
}

// Q5: Status names the core's version, and Quit ends an idle core but not one at work.
func TestQuit(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	var st Status
	if err := ctl("Status", struct{}{}, &st); err != nil || st.Version != version {
		t.Fatalf("status version %q, %v", st.Version, err)
	}
	exited := make(chan int, 1)
	d.exit = func(code int) { exited <- code }
	var out struct{}
	d.work.Lock()
	if err := ctl("Quit", struct{}{}, &out); err == nil {
		t.Fatal("quit during a pass")
	}
	d.work.Unlock()
	if err := ctl("Quit", struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the core did not exit")
	}
	if d.work.TryLock() {
		t.Fatal("the work lock was let go before the exit")
	}
}

// Q7 (pass 3), the part needing no server change: a username whose case the directory changed
// is still the same account.
func TestSignInIgnoresUsernameCase(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	signedOut(t, d, ctl, srv.URL)
	redeemUser.Store("Alice")
	defer redeemUser.Store("alice")
	var out struct{}
	if err := ctl("SignIn", struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
}
