package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Busnes-app/kydrive-desktop/core/engine"
)

type testIdentity struct{ ID, Name string }

func identityServer(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Int32) {
	t.Helper()
	identity := &atomic.Value{}
	identity.Store(testIdentity{"u1", "alice"})
	revoked := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := identity.Load().(testIdentity)
		switch r.URL.Path {
		case "/api/devices/token":
			json.NewEncoder(w).Encode(map[string]string{"token": "kyd_SECRETTOKEN", "user": who.Name, "user_id": who.ID})
		case "/api/drive/device":
			if r.Header.Get("Authorization") != "Bearer kyd_SECRETTOKEN" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"user": who.Name, "user_id": who.ID})
		case "/api/devices/self":
			revoked.Add(1)
			w.WriteHeader(204)
		case "/api/drive/workspaces":
			json.NewEncoder(w).Encode([]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, identity, revoked
}

func TestSignInUsesImmutableAccountID(t *testing.T) {
	for _, tc := range []struct {
		name, id, user string
		allowed        bool
	}{
		{"same account", "u1", "alice", true},
		{"renamed account", "u1", "alice.renamed", true},
		{"reused username", "u2", "alice", false},
		{"missing identity", "", "alice", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, identity, revoked := identityServer(t)
			d, ctl := start(t, srv)
			signedOut(t, d, ctl, srv.URL)
			if d.db.Get("user_id") != "u1" {
				t.Errorf("link did not record immutable identity")
			}
			identity.Store(testIdentity{tc.id, tc.user})
			var out struct{}
			err := ctl("SignIn", struct{}{}, &out)
			if (err == nil) != tc.allowed {
				t.Fatalf("SignIn as %s/%s: %v, allowed=%v", tc.id, tc.user, err, tc.allowed)
			}
			ws, _ := d.db.Workspaces()
			if len(ws) != 1 || ws[0].Cursor != 7 {
				t.Fatalf("bindings changed: %+v", ws)
			}
			if d.db.Get("user_id") != "u1" {
				t.Errorf("recorded identity changed")
			}
			if !tc.allowed && (revoked.Load() != 1 || d.db.Get("user") != "alice") {
				t.Fatalf("refusal changed old account or kept new credential")
			}
		})
	}
}

func TestLegacyLinkLearnsItsAccountBeforeSignedOut(t *testing.T) {
	srv, identity, _ := identityServer(t)
	d, ctl := start(t, srv)
	var out struct{}
	if err := ctl("Link", LinkArgs{Server: srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	d.db.Set("user", "")
	d.db.Set("user_id", "") // pre-0.7 state
	d.pass(t.Context(), false)
	if d.db.Get("user_id") != "u1" || d.db.Get("user") != "alice" {
		t.Fatalf("good pass did not learn account: %q/%q", d.db.Get("user_id"), d.db.Get("user"))
	}
	d.db.PutWorkspace(engine.Workspace{ID: "w1", Name: "Docs", Dir: "Docs", Cursor: 7})
	d.mu.Lock()
	d.state = "signed_out"
	d.mu.Unlock()
	identity.Store(testIdentity{"u1", "renamed"})
	if err := ctl("SignIn", struct{}{}, &out); err != nil {
		t.Fatalf("migrated account cannot sign in again after rename: %v", err)
	}
}

func TestOlderServerKeepsLegacyRefusal(t *testing.T) {
	srv := server(t) // no immutable ID and no device-account route
	d, ctl := start(t, srv)
	var out struct{}
	if err := ctl("Link", LinkArgs{Server: srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	d.db.Set("user", "")
	d.db.Set("user_id", "")
	d.pass(t.Context(), false)
	d.mu.Lock()
	d.state = "signed_out"
	d.mu.Unlock()
	if err := ctl("SignIn", struct{}{}, &out); err == nil || !strings.Contains(err.Error(), "unlink") {
		t.Fatalf("unknown legacy account accepted: %v", err)
	}
}
