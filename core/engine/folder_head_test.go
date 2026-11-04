package engine

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

type interceptTrash func(*http.Request) (*http.Response, error)

func (f interceptTrash) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A colleague writes after the existing feed check, just before the trash request reaches
// the server. Only the server's atomic head precondition can close this last window.
func TestFolderTrashRefusesAChangeAfterItsFeedCheck(t *testing.T) {
	v := setup(t, map[string]string{"folder/old.txt": "old"})
	folder := v.r.ID(t, v.ws, "folder")
	if err := os.RemoveAll(v.path("folder")); err != nil {
		t.Fatal(err)
	}
	var injected atomic.Bool
	transport := v.e.API.HTTP.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	v.e.API.HTTP = &http.Client{Transport: interceptTrash(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/folders/"+folder+"/trash") && injected.CompareAndSwap(false, true) {
			v.r.Put(t, v.ws, "folder/new.txt", []byte("colleague"))
		}
		return transport.RoundTrip(r)
	})}
	v.sync()
	if _, ok := v.r.Read(t, v.ws, "folder/new.txt"); !ok {
		t.Fatal("folder trash lost a colleague's concurrent file")
	}
	it, err := v.e.DB.ItemAt(v.ws, "folder")
	if err != nil || it == nil || it.State != "synced" {
		t.Fatalf("409 discarded or stranded folder state: %+v,%v", it, err)
	}
	v.sync()
	if b, err := os.ReadFile(v.path("folder/new.txt")); err != nil || string(b) != "colleague" {
		t.Fatalf("next pull did not preserve the new file: %q,%v", b, err)
	}
}

// The old server refuses unknown fields. The checked legacy request must still work;
// otherwise a backwards-compatible upgrade would stop all folder deletes there.
func TestFolderTrashWorksWithOlderStrictServer(t *testing.T) {
	v := setupOn(t, newFake(t), map[string]string{"folder/old.txt": "old"})
	if err := os.RemoveAll(v.path("folder")); err != nil {
		t.Fatal(err)
	}
	var heads, legacy atomic.Int32
	transport := v.e.API.HTTP.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	v.e.API.HTTP = &http.Client{Transport: interceptTrash(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" && strings.Contains(r.URL.Path, "/folders/") && strings.HasSuffix(r.URL.Path, "/trash") {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(b))
			var body map[string]any
			if err := json.Unmarshal(b, &body); err != nil {
				t.Fatal(err)
			}
			if _, ok := body["head"]; ok {
				heads.Add(1)
				return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"invalid drive input"}`))}, nil
			}
			legacy.Add(1)
		}
		return transport.RoundTrip(r)
	})}
	v.sync()
	if heads.Load() != 1 || legacy.Load() != 1 {
		t.Fatalf("head=%d legacy=%d; expected one conditional then one compatible request", heads.Load(), legacy.Load())
	}
	if _, ok := v.r.Read(t, v.ws, "folder/old.txt"); ok {
		t.Fatal("older server no longer accepts folder deletion")
	}
}
