package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/zalando/go-keyring"
)

func TestLinkForgetsPreviousWorkspaceBindingsAfterApproval(t *testing.T) {
	for _, declined := range []bool{false, true} {
		t.Run(map[bool]string{false: "approved", true: "declined"}[declined], func(t *testing.T) {
			srv := server(t)
			d, ctl := start(t, srv)
			previous := engine.Workspace{ID: "w1", Name: "Docs", Dir: "Docs", Cursor: 123}
			if err := d.db.PutWorkspace(previous); err != nil {
				t.Fatal(err)
			}
			if err := d.db.PutItem(engine.Item{ID: "old-file", Workspace: "w1", Kind: "file", Name: "payroll.txt", Path: "payroll.txt", State: "synced"}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(d.root, "Docs"), 0700); err != nil {
				t.Fatal(err)
			}
			retained := filepath.Join(d.root, "Docs", "payroll.txt")
			if err := os.WriteFile(retained, []byte("previous account"), 0600); err != nil {
				t.Fatal(err)
			}
			// A revoke without wipe leaves the binding and files, but forgets the token.
			d.mu.Lock()
			d.state = "revoked"
			d.mu.Unlock()
			if declined {
				d.open = func(string) error { return errors.New("approval declined") }
			}
			var out struct{}
			err := ctl("Link", LinkArgs{Server: srv.URL, Name: "laptop"}, &out)
			if declined != (err != nil) {
				t.Fatalf("declined=%v: %v", declined, err)
			}
			ws, err := d.db.Workspaces()
			if err != nil {
				t.Fatal(err)
			}
			if declined {
				if len(ws) != 1 || ws[0] != previous {
					t.Fatalf("failed approval changed previous bindings: %+v", ws)
				}
			} else {
				if len(ws) != 0 {
					t.Fatalf("new credentials retained previous workspace bindings: %+v", ws)
				}
				if item, err := d.db.Item("old-file"); err != nil || item != nil {
					t.Fatal("new credentials retained previous sync records", item, err)
				}
				if err := ctl("Choose", struct{ IDs []string }{[]string{"w1"}}, &out); err != nil {
					t.Fatal(err)
				}
				ws, err = d.db.Workspaces()
				if err != nil || len(ws) != 1 || ws[0].Dir == "Docs" || ws[0].Cursor != -1 {
					t.Fatalf("new account reused retained directory or cursor: %+v, %v", ws, err)
				}
			}
			if b, err := os.ReadFile(retained); err != nil || string(b) != "previous account" {
				t.Fatal("link changed retained files", string(b), err)
			}
		})
	}
}

func TestLinkRefusesActivationWhenWorkspaceResetFails(t *testing.T) {
	srv := server(t)
	d, ctl := start(t, srv)
	if err := d.db.Close(); err != nil {
		t.Fatal(err)
	}
	var out struct{}
	if err := ctl("Link", LinkArgs{Server: srv.URL}, &out); err == nil {
		t.Fatal("new credentials activated despite failed workspace reset")
	}
	d.mu.Lock()
	token := d.token
	d.mu.Unlock()
	if token != "" {
		t.Fatal("failed link activated a credential")
	}
	if _, err := keyring.Get(keyringService, srv.URL); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("failed workspace reset published a new keyring credential", err)
	}
}
