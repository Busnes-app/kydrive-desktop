package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/Busnes-app/kydrive-desktop/core/api"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func rewriteKeepingMetadata(t *testing.T, p string, content string) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func TestCarryoverSameMetadataEditUploads(t *testing.T) {
	v := setup(t, map[string]string{"vault.bin": "AAAA"})
	rewriteKeepingMetadata(t, v.path("vault.bin"), "BBBB")
	v.sync()
	if got := v.remote("vault.bin"); got != "BBBB" {
		t.Fatalf("server kept %q", got)
	}
	noConflicts(t, v)
}

func TestCarryoverSameMetadataReaderEditListed(t *testing.T) {
	f := newFake(t)
	v := setupOn(t, f, map[string]string{"vault.bin": "AAAA"})
	f.reader = true
	rewriteKeepingMetadata(t, v.path("vault.bin"), "BBBB")
	v.sync()
	v.sync()
	issues, err := v.e.DB.Issues()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, i := range issues {
		if i.Kind == "readonly" && i.Path == "vault.bin" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("readonly issues %d, want 1: %+v", count, issues)
	}
	if v.remote("vault.bin") != "AAAA" {
		t.Fatal("reader uploaded")
	}
}

func TestCarryoverMovedBetweenWorkspaces(t *testing.T) {
	for _, sourceFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(sourceFirst), func(t *testing.T) {
			v := setup(t, map[string]string{"keep.txt": "keep", "move.txt": "original"})
			name := "Zed"
			if !sourceFirst {
				name = "Aardvark"
			}
			dest := v.r.NewWorkspace(t, name)
			if err := v.e.Choose(context.Background(), []string{v.ws, dest}); err != nil {
				t.Fatal(err)
			}
			v.sync()
			id := v.r.ID(t, v.ws, "move.txt")
			if _, err := v.e.API.MoveFile(context.Background(), id, 1, dest, "", "moved.txt"); err != nil {
				t.Fatal(err)
			}
			v.sync()
			v.sync()
			if v.exists("move.txt") {
				t.Fatal("server-moved file reappeared in source")
			}
			if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"keep.txt"}) {
				t.Fatalf("source paths %v", got)
			}
			if got, err := os.ReadFile(filepath.Join(v.root, name, "moved.txt")); err != nil || string(got) != "original" {
				t.Fatalf("destination %q %v", got, err)
			}
		})
	}
}

func TestCarryoverDownloadOutageStopsPass(t *testing.T) {
	for _, mode := range []string{"dial", "body", "503"} {
		t.Run(mode, func(t *testing.T) {
			v := setup(t, nil)
			for i := 0; i < 5; i++ {
				v.r.Put(t, v.ws, fmt.Sprintf("%d.txt", i), []byte("bytes"))
			}
			target, _ := url.Parse(v.r.URL())
			proxy := httputil.NewSingleHostReverseProxy(target)
			var attempts atomic.Int32
			var offline atomic.Bool
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if offline.Load() {
					http.Error(w, "network unavailable", 503)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/download") {
					attempts.Add(1)
					offline.Store(true)
					if mode == "503" {
						http.Error(w, "unavailable", 503)
						return
					}
					if mode == "body" {
						w.Header().Set("Content-Length", "5")
						w.WriteHeader(200)
						w.Write([]byte("b"))
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
					return
				}
				proxy.ServeHTTP(w, r)
			}))
			defer front.Close()
			v.e.API.Base = front.URL
			v.e.API.HTTP = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
			if err := v.e.Sync(context.Background()); err == nil {
				t.Error("outage reported a successful pass")
			}
			if got := attempts.Load(); got != 1 {
				t.Errorf("downloads attempted %d, want 1", got)
			}
			activity, _ := v.e.DB.Activity(0)
			for _, a := range activity {
				if strings.Contains(a.Message, "Download of") {
					t.Errorf("per-file network warning: %s", a.Message)
				}
			}
			retries, _ := v.e.DB.Retries(v.ws)
			if len(retries) != 1 {
				t.Errorf("retry count %d", len(retries))
			}
			v.e.API.Base = v.r.URL()
			v.sync()
			if got := tree(t, v.path("")); len(got) != 5 {
				t.Fatalf("recovery tree %v", got)
			}
		})
	}
}

func TestCarryoverChooseNormalization(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires native macOS normalization-insensitive filesystem")
	}
	for _, searchable := range []bool{true, false} {
		t.Run(fmt.Sprint(searchable), func(t *testing.T) {
			v := setup(t, nil)
			retained := filepath.Join(v.root, "Cafe\u0301")
			if err := os.Mkdir(retained, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(retained, "private.txt"), []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(v.root, "Caf\u00e9")); err != nil {
				t.Skip("filesystem distinguishes normalization")
			}
			ws := v.r.NewWorkspace(t, "Caf\u00e9")
			if !searchable {
				if os.Geteuid() == 0 {
					t.Skip("root ignores search permissions")
				}
				os.Chmod(v.root, 0600)
				defer os.Chmod(v.root, 0700)
			}
			err := v.e.Choose(context.Background(), []string{v.ws, ws})
			if !searchable {
				if err == nil {
					t.Fatal("lookup failure was treated as a free name")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			workspaces, _ := v.e.DB.Workspaces()
			for _, w := range workspaces {
				if w.ID == ws && w.Dir == "Caf\u00e9" {
					t.Fatal("retained normalized folder adopted")
				}
			}
		})
	}
}

func TestCarryoverUpgradeChecksPendingEdit(t *testing.T) {
	for _, reader := range []bool{true, false} {
		t.Run(fmt.Sprint(reader), func(t *testing.T) {
			f := newFake(t)
			v := setupOn(t, f, map[string]string{"edit.bin": "AAAA", "same.bin": "same"})
			f.reader = reader
			if _, err := v.e.DB.db.Exec(`UPDATE items SET local_ctime=0`); err != nil {
				t.Fatal(err)
			}
			rewriteKeepingMetadata(t, v.path("edit.bin"), "BBBB")
			v.sync()
			if !reader && v.remote("edit.bin") != "BBBB" {
				t.Fatal("upgrade swallowed pending edit")
			}
			issues, _ := v.e.DB.Issues()
			count := 0
			for _, i := range issues {
				if i.Kind == "readonly" {
					count++
					if i.Path != "edit.bin" {
						t.Fatalf("unchanged upgrade row reported: %+v", i)
					}
				}
			}
			if reader && count != 1 {
				t.Fatalf("reader upgrade issues %+v", issues)
			}
		})
	}
}

func TestCarryoverRewriteSettlesBeforeUpload(t *testing.T) {
	v := setup(t, map[string]string{"vault.bin": "AAAA"})
	v.e.Settle = 100 * time.Millisecond
	rewriteKeepingMetadata(t, v.path("vault.bin"), "BBBB")
	v.sync()
	if !v.e.Unsettled || v.remote("vault.bin") != "AAAA" {
		t.Fatal("same-metadata writer was not allowed to settle")
	}
	time.Sleep(150 * time.Millisecond)
	v.e.Unsettled = false
	v.sync()
	if v.remote("vault.bin") != "BBBB" || v.e.Unsettled {
		t.Fatal("settled rewrite did not upload")
	}
}

func TestCarryoverLocalTraversalStopsOnCancellation(t *testing.T) {
	v := setup(t, map[string]string{"a.txt": "a", "sub/b.txt": "b"})
	ws, _ := v.e.DB.Workspaces()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v.e.Context = ctx
	if _, err := v.e.scan(ws[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan %v", err)
	}
	if err := v.e.evict(ws[0], ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("evict %v", err)
	}
	if !v.exists("a.txt") || !v.exists("sub/b.txt") {
		t.Fatal("canceled evict removed copies")
	}
}

// A write landing after upload completion must remain pending, even with preserved metadata.
func TestCarryoverUploadDoesNotRecordALaterRewrite(t *testing.T) {
	v := setup(t, map[string]string{"vault.bin": "AAAA"})
	rewriteKeepingMetadata(t, v.path("vault.bin"), "BBBB")
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	var edited atomic.Bool
	proxy.ModifyResponse = func(r *http.Response) error {
		if strings.HasSuffix(r.Request.URL.Path, "/complete") && edited.CompareAndSwap(false, true) {
			rewriteKeepingMetadata(t, v.path("vault.bin"), "CCCC")
		}
		return nil
	}
	front := httptest.NewServer(proxy)
	defer front.Close()
	v.e.API.Base = front.URL
	v.sync()
	v.sync()
	if v.remote("vault.bin") != "CCCC" {
		t.Fatal("post-upload rewrite recorded as already synced")
	}
}

// Prior pass-2 certification objection: source cleanup must survive dropping the destination.
func TestCarryoverBlockedMoveSurvivesDestinationUnchoose(t *testing.T) {
	for _, name := range []string{"Archive", "Zed"} {
		t.Run(name, func(t *testing.T) {
			v := setup(t, map[string]string{"ro/move.txt": "original", "keep.txt": "keep"})
			dest := v.r.NewWorkspace(t, name)
			if err := v.e.Choose(context.Background(), []string{v.ws, dest}); err != nil {
				t.Fatal(err)
			}
			v.sync()
			readOnly(t, v.path("ro"))
			id := v.r.ID(t, v.ws, "ro/move.txt")
			if _, err := v.e.API.MoveFile(context.Background(), id, 1, dest, "", "moved.txt"); err != nil {
				t.Fatal(err)
			}
			v.sync()
			if err := v.e.Choose(context.Background(), []string{v.ws}); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(v.path("ro"), 0755); err != nil {
				t.Fatal(err)
			}
			v.sync()
			v.sync()
			if v.exists("ro/move.txt") {
				t.Fatal("source cleanup was lost with destination retry")
			}
			if got := v.r.Paths(t, v.ws); !slices.Equal(got, []string{"keep.txt"}) {
				t.Fatalf("source server %v", got)
			}
			if b, ok := v.r.Read(t, dest, "moved.txt"); !ok || string(b) != "original" {
				t.Fatalf("destination %q %v", b, ok)
			}
		})
	}
}

func TestCarryoverLegacyStateMigrationPreservesRetries(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(schema, "local_ctime INTEGER NOT NULL DEFAULT 0, ", "", 1)
	oldSchema = strings.Replace(oldSchema, "id TEXT NOT NULL, workspace TEXT NOT NULL, file TEXT NOT NULL, PRIMARY KEY(workspace,id)", "id TEXT PRIMARY KEY, workspace TEXT NOT NULL, file TEXT NOT NULL", 1)
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	old := &DB{db: db}
	if _, err := old.AddRetry("source", api.File{ID: "same-id", Workspace: "source"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO items VALUES('same-id','source','file','','file.txt',1,'digest',4,'mtime','file.txt',4,1,2,'synced')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	upgraded, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	it, err := upgraded.Item("same-id")
	if err != nil || it == nil || it.LocalCtime != 0 || it.Digest != "digest" {
		t.Fatalf("legacy item %+v %v", it, err)
	}
	if _, err := upgraded.AddRetry("destination", api.File{ID: "same-id", Workspace: "destination", Name: "new.txt"}); err != nil {
		t.Fatal(err)
	}
	source, _ := upgraded.Retries("source")
	destination, _ := upgraded.Retries("destination")
	if len(source) != 1 || len(destination) != 1 || source[0].Name != "" || destination[0].Name != "new.txt" {
		t.Fatalf("retries shared: source=%+v destination=%+v", source, destination)
	}
	if err := upgraded.DropWorkspace("destination"); err != nil {
		t.Fatal(err)
	}
	source, _ = upgraded.Retries("source")
	if len(source) != 1 {
		t.Fatal("dropping destination lost source retry")
	}
}
