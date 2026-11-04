package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

// Q2: a link waiting for the browser can be cancelled, and a new one starts at once.
func TestCancelLink(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	opened := make(chan struct{}, 2)
	auto := d.open
	d.open = func(string) error { opened <- struct{}{}; return nil } // never approved
	done := make(chan error, 1)
	go func() { var out struct{}; done <- ctl("Link", LinkArgs{Server: srv.URL}, &out) }()
	<-opened
	var out struct{}
	if err := ctl("CancelLink", struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("cancelled link answered %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CancelLink did not end the pending link")
	}
	d.open = auto
	if err := ctl("Link", LinkArgs{Server: srv.URL}, &out); err != nil {
		t.Fatalf("a new link after cancel: %v", err)
	}
}

// Q2: Unlink also ends a pending link.
func TestUnlinkCancelsPendingLink(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	opened := make(chan struct{}, 1)
	d.open = func(string) error { opened <- struct{}{}; return nil }
	done := make(chan error, 1)
	go func() { var out struct{}; done <- ctl("Link", LinkArgs{Server: srv.URL}, &out) }()
	<-opened
	var out struct{}
	if err := ctl("Unlink", map[string]bool{"RemoveFiles": false}, &out); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("link completed after unlink")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Unlink did not end the pending link")
	}
}

// signedOut links d to srv with its workspace chosen, then puts it in the state a plain 401
// leaves.
func signedOut(t *testing.T, d *daemon, ctl func(string, any, any) error, server string) {
	t.Helper()
	var out struct{}
	if err := ctl("Link", LinkArgs{Server: server, Name: "laptop"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := d.db.PutWorkspace(engine.Workspace{ID: "w1", Name: "Docs", Dir: "Docs", Cursor: 7}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.state, d.token = "signed_out", "kyd_REFUSED"
	d.mu.Unlock()
}

// Q3: Sign in again with the same account keeps the workspace bindings.
func TestSignInKeepsWorkspaces(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	signedOut(t, d, ctl, srv.URL)
	var out struct{}
	if err := ctl("SignIn", struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
	ws, err := d.db.Workspaces()
	if err != nil || len(ws) != 1 || ws[0].Cursor != 7 {
		t.Fatalf("sign in changed the bindings: %+v, %v", ws, err)
	}
	var st Status
	if err := ctl("Status", struct{}{}, &st); err != nil || !st.Linked || st.State == "signed_out" {
		t.Fatalf("status after sign in %+v, %v", st, err)
	}
}

// Q3: Sign in again as another account is refused and keeps everything as it was.
func TestSignInRefusesAnotherAccount(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	signedOut(t, d, ctl, srv.URL)
	redeemUser.Store("mallory")
	defer redeemUser.Store("alice")
	selfRevoked.Store(0)
	var out struct{}
	err := ctl("SignIn", struct{}{}, &out)
	if err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("another account signed in: %v", err)
	}
	if selfRevoked.Load() != 1 {
		t.Fatal("the refused link's device was not revoked")
	}
	d.mu.Lock()
	state := d.state
	d.mu.Unlock()
	if ws, _ := d.db.Workspaces(); len(ws) != 1 || state != "signed_out" || d.db.Get("user") != "alice" {
		t.Fatalf("refused sign in changed state %q, bindings %+v", state, ws)
	}
}

// Q3: a signed-out core reads the keyring again after its backoff and resumes syncing.
func TestSignedOutRetriesFromKeyring(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	signedOut(t, d, ctl, srv.URL)
	if err := keyring.Delete(keyringService, srv.URL); err != nil {
		t.Fatal(err)
	}
	d.pass(t.Context(), false)
	d.mu.Lock()
	if d.state != "signed_out" || d.backoff == 0 {
		t.Fatalf("no credential: state %q, backoff %v", d.state, d.backoff)
	}
	d.retryAt = time.Time{} // the backoff has passed
	d.mu.Unlock()
	keyring.Set(keyringService, srv.URL, "kyd_SECRETTOKEN") // the keyring unlocked
	d.pass(t.Context(), false)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state != "idle" || d.token != "kyd_SECRETTOKEN" || d.backoff != 0 {
		t.Fatalf("after retry: state %q (%s), backoff %v", d.state, d.lastErr, d.backoff)
	}
}

// O-R5 (pass 2): Unlink that removes files finishes the unlink even when a file cannot go yet,
// and a later pass removes it.
func TestUnlinkRemovingFilesResumes(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permission bits do not stop this user")
	}
	srv := server(t)
	d, ctl := start(t, srv)
	var out struct{}
	if err := ctl("Link", LinkArgs{Server: srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	ro := filepath.Join(d.root, "Docs", "ro")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ro, "kept.txt"), []byte("x"), 0o600)
	d.db.PutWorkspace(engine.Workspace{ID: "w1", Name: "Docs", Dir: "Docs", Cursor: 5})
	os.Chmod(ro, 0o555)
	defer os.Chmod(ro, 0o755)
	if err := ctl("Unlink", map[string]bool{"RemoveFiles": true}, &out); err == nil || !strings.Contains(err.Error(), "not all removed") {
		t.Fatalf("unlink answered %v", err)
	}
	var st Status
	if err := ctl("Status", struct{}{}, &st); err != nil || st.Linked || st.State != "unlinked" {
		t.Fatalf("status %+v, %v", st, err)
	}
	os.Chmod(ro, 0o755)
	d.pass(t.Context(), false)
	if _, err := os.Stat(filepath.Join(d.root, "Docs")); !os.IsNotExist(err) {
		t.Fatalf("a later pass did not finish removing the files: %v", err)
	}
}

// Q3: an install linked before the account was recorded cannot tell another account from its
// own, so it is refused and keeps its bindings.
func TestSignInWithoutRecordedAccountRefused(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	signedOut(t, d, ctl, srv.URL)
	d.db.Set("user", "")
	selfRevoked.Store(0)
	var out struct{}
	if err := ctl("SignIn", struct{}{}, &out); err == nil || !strings.Contains(err.Error(), "unlink") {
		t.Fatalf("legacy sign in answered %v", err)
	}
	if ws, _ := d.db.Workspaces(); len(ws) != 1 || selfRevoked.Load() != 1 {
		t.Fatalf("bindings %+v, revoked %d", ws, selfRevoked.Load())
	}
}
