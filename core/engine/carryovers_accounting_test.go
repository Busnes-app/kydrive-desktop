package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
)

func TestCarryoverReaderAccountingCancels(t *testing.T) {
	for _, cancelAt := range []int{3, 4} {
		t.Run(fmt.Sprint(cancelAt), func(t *testing.T) {
			v := setup(t, map[string]string{"a.txt": "a", "b.txt": "b"})
			if err := os.Remove(v.path("b.txt")); err != nil {
				t.Fatal(err)
			}
			v.write("z-new.txt", "reader edit")
			ws, err := v.e.DB.Workspaces()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checksAfterListing := 0
			progress := &cancelAfterProgress{Context: ctx, cancel: cancel, progressed: func() bool {
				issues, err := v.e.DB.Issues()
				if err != nil {
					t.Fatal(err)
				}
				for _, i := range issues {
					if i.Path == "z-new.txt" && i.Kind == "readonly" {
						checksAfterListing++
						return checksAfterListing == cancelAt
					}
				}
				return false
			}}
			v.e.Context = progress
			err = v.e.readerEdits(ws[0])
			if !progress.triggered || !errors.Is(err, context.Canceled) {
				t.Errorf("reader accounting ignored cancellation after listing: checks=%d triggered=%v err=%v", checksAfterListing, progress.triggered, err)
			}
			after, err := v.e.workspace(v.ws)
			if err != nil {
				t.Fatal(err)
			}
			if after.Cursor != ws[0].Cursor {
				t.Errorf("canceled reader accounting wrote restore cursor %d (was %d)", after.Cursor, ws[0].Cursor)
			}
			// On resumption, a real reader delete still arms restoration before a promoted writer pushes.
			v.e.Context = context.Background()
			if err := v.e.readerEdits(ws[0]); err != nil {
				t.Fatal(err)
			}
			after, err = v.e.workspace(v.ws)
			if err != nil || after.Cursor != -1 {
				t.Fatalf("reader deletion did not request restoration: %+v %v", after, err)
			}
			v.sync()
			if v.read("b.txt") != "b" || v.remote("b.txt") != "b" {
				t.Fatal("promotion failed to restore the reader delete")
			}
		})
	}
}

func TestCarryoverReaderRetryHeldMissingCopyIsExcluded(t *testing.T) {
	v := setup(t, map[string]string{"held.txt": "held", "kept.txt": "kept"})
	ws, err := v.e.DB.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	it, err := v.e.DB.ItemAt(v.ws, "held.txt")
	if err != nil {
		t.Fatal(err)
	}
	f := api.File{ID: it.ID, Workspace: v.ws, Name: it.Name, Revision: it.Revision, Digest: it.Digest, Size: it.Size, Mtime: it.Mtime}
	if _, err := v.e.DB.AddRetry(v.ws, f); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(v.path("held.txt")); err != nil {
		t.Fatal(err)
	}
	if err := v.e.readerEdits(ws[0]); err != nil {
		t.Fatal(err)
	}
	after, err := v.e.workspace(v.ws)
	if err != nil || after.Cursor != ws[0].Cursor {
		t.Fatalf("retry-held missing copy requested restore: %+v %v", after, err)
	}
	retries, err := v.e.DB.Retries(v.ws)
	if err != nil || len(retries) != 1 || retries[0].ID != it.ID {
		t.Fatalf("held retry lost: %+v %v", retries, err)
	}
}

func TestCarryoverRetryAccountingCancelsAfterProgress(t *testing.T) {
	v := setup(t, nil)
	memory := NewMemory()
	v.e.Memory = memory
	const total = 128
	otherKey := "other-workspace/retry-000"
	otherNext := time.Now().Add(20 * time.Minute)
	memory.wait[otherKey], memory.next[otherKey] = 20*time.Minute, otherNext
	for i := range total {
		id := fmt.Sprintf("retry-%03d", i)
		if err := os.Mkdir(v.path(id), 0700); err != nil {
			t.Fatal(err)
		}
		for _, owner := range []string{v.ws, "other-workspace"} {
			if _, err := v.e.DB.AddRetry(owner, api.File{ID: id, Workspace: owner, Name: id, Digest: digest([]byte("blocked")), Size: 7, Mtime: "2026-01-02T03:04:05Z"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ws, err := v.e.DB.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := &cancelAfterProgress{Context: ctx, cancel: cancel, progressed: func() bool { return len(memory.wait) > 1 }}
	v.e.Context = progress
	err = v.e.pull(progress, ws[0], ws[0].Cursor)
	if !progress.triggered || !errors.Is(err, context.Canceled) {
		t.Errorf("retry accounting ignored cancellation: triggered=%v err=%v", progress.triggered, err)
	}
	if len(memory.wait) != 2 || len(memory.next) != 2 {
		t.Errorf("accounting continued after first entry: waits=%d next=%d", len(memory.wait), len(memory.next))
	}
	for key, wait := range memory.wait {
		if wait == 0 || memory.next[key].IsZero() {
			t.Errorf("partial backoff pair %q", key)
		}
	}
	for _, owner := range []string{v.ws, "other-workspace"} {
		retries, err := v.e.DB.Retries(owner)
		if err != nil || len(retries) != total {
			t.Errorf("persistent retries changed for %s: %d %v", owner, len(retries), err)
		}
	}
	if memory.wait[otherKey] != 20*time.Minute || memory.next[otherKey] != otherNext {
		t.Error("other workspace backoff changed")
	}
	// A fresh pass skips the already-backed-off entry and finishes the rest independently.
	v.e.Context = context.Background()
	if err := v.e.pull(context.Background(), ws[0], ws[0].Cursor); err != nil {
		t.Fatal(err)
	}
	if len(memory.wait) != total+1 || len(memory.next) != total+1 {
		t.Fatalf("resumed accounting incomplete: %d/%d", len(memory.wait), len(memory.next))
	}
	if memory.wait[otherKey] != 20*time.Minute || memory.next[otherKey] != otherNext {
		t.Fatal("resumption changed other workspace backoff")
	}
}

func TestCarryoverRetryAccountingAlreadyCanceled(t *testing.T) {
	v := setup(t, map[string]string{"retry.txt": "old"})
	v.e.Memory = NewMemory()
	it, err := v.e.DB.ItemAt(v.ws, "retry.txt")
	if err != nil {
		t.Fatal(err)
	}
	v.r.Put(t, v.ws, "retry.txt", []byte("new"))
	if _, err := v.e.DB.AddRetry(v.ws, api.File{ID: it.ID, Workspace: v.ws, Name: it.Name, Revision: it.Revision + 1, Digest: digest([]byte("new")), Size: 3, Mtime: it.Mtime}); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(v.r.URL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/drive/files/"+it.ID+"/download" {
			cancel()
			http.Error(w, "canceled transfer", 503)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	v.e.API.Base = front.URL
	v.e.Context = ctx
	ws, err := v.e.DB.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.e.pull(ctx, ws[0], ws[0].Cursor); !errors.Is(err, context.Canceled) {
		t.Fatalf("pull %v", err)
	}
	if len(v.e.Memory.wait) != 0 || len(v.e.Memory.next) != 0 {
		t.Fatalf("already-canceled error continuation performed backoff accounting: %+v", v.e.Memory.wait)
	}
	retries, err := v.e.DB.Retries(v.ws)
	if err != nil || len(retries) != 1 {
		t.Fatalf("persistent retry changed: %+v %v", retries, err)
	}
}
