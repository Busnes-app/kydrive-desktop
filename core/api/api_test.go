package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func client(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, Token: "kyd_secret", HTTP: srv.Client()}
}

func TestBearerAndWorkspaces(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer kyd_secret" || r.URL.Path != "/api/drive/workspaces" {
			http.Error(w, "bad", 400)
			return
		}
		io.WriteString(w, `[{"id":"w1","name":"Docs","kind":"shared","role":"editor","head":7}]`)
	})
	ws, err := c.Workspaces(context.Background())
	if err != nil || len(ws) != 1 || ws[0].Head != 7 || ws[0].Name != "Docs" {
		t.Fatalf("got %+v, %v", ws, err)
	}
}

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		status        int
		body          string
		revoked, wipe bool
	}{
		{401, `{"error":"device_revoked","wipe":true}`, true, true},
		{401, `{"error":"device_revoked","wipe":false}`, true, false},
		{401, `{"error":"Authentication required"}`, false, false},
		{410, `{"error":"Gone"}`, false, false},
	} {
		c := client(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		_, err := c.Changes(context.Background(), "w1", 3)
		var e *Error
		if !errors.As(err, &e) || e.Status != tc.status || e.Revoked() != tc.revoked || e.Wipe != tc.wipe {
			t.Errorf("%d %s: got %#v", tc.status, tc.body, err)
		}
	}
}

func TestChangesDecodes(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/drive/workspaces/w1/changes" || r.URL.Query().Get("after") != "3" {
			http.Error(w, "bad", 400)
			return
		}
		io.WriteString(w, `{"changes":[{"kind":"file","id":"f1","removed":false,"file":{"id":"f1","name":"a","revision":2,"digest":"ab","mtime":"2026-10-18T10:00:00Z"}},{"kind":"folder","id":"d1","removed":true}],"next":9,"more":true}`)
	})
	ch, err := c.Changes(context.Background(), "w1", 3)
	if err != nil || ch.Next != 9 || !ch.More || len(ch.Changes) != 2 || ch.Changes[0].File.Revision != 2 || !ch.Changes[1].Removed {
		t.Fatalf("got %+v, %v", ch, err)
	}
}

func TestDownloadChecksDigest(t *testing.T) {
	body := []byte("hello")
	sum := sha256.Sum256(body)
	c := client(t, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	var buf bytes.Buffer
	if err := c.Download(context.Background(), File{ID: "f1", Size: 5, Digest: hex.EncodeToString(sum[:])}, &buf); err != nil || buf.String() != "hello" {
		t.Fatalf("good digest: %q, %v", buf.String(), err)
	}
	if err := c.Download(context.Background(), File{ID: "f1", Size: 5, Digest: strings.Repeat("0", 64)}, io.Discard); err == nil {
		t.Fatal("a wrong digest was accepted")
	}
	if err := c.Download(context.Background(), File{ID: "f1", Size: 5}, io.Discard); err == nil {
		t.Fatal("a missing digest was accepted")
	}
}

// An upload resumes from the server's offset after the connection drops mid-chunk.
func TestUploadSurvivesDroppedConnection(t *testing.T) {
	defer func(n int64) { chunkSize = n }(chunkSize)
	chunkSize = 4
	var mu sync.Mutex
	var got []byte
	dropped := false
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/drive/workspaces/w1/upload-sessions":
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), `"mtime":"2026-10-18T10:00:00Z"`) || !strings.Contains(string(b), `"revision":3`) {
				http.Error(w, string(b), 400)
				return
			}
			io.WriteString(w, `{"id":"s1","size":10,"received":0}`)
		case r.Method == "PUT" && r.URL.Path == "/api/drive/upload-sessions/s1":
			if len(got) == 4 && !dropped {
				dropped = true
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			var start, end, total int64
			if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != int64(len(got)) || total != 10 {
				w.Header().Set("Upload-Offset", strconv.Itoa(len(got)))
				http.Error(w, "conflict", 409)
				return
			}
			b, _ := io.ReadAll(r.Body)
			got = append(got, b...)
			w.Header().Set("Upload-Offset", strconv.Itoa(len(got)))
			w.WriteHeader(204)
		case r.Method == "GET" && r.URL.Path == "/api/drive/upload-sessions/s1":
			io.WriteString(w, `{"id":"s1","size":10,"received":`+strconv.Itoa(len(got))+`}`)
		case r.Method == "POST" && r.URL.Path == "/api/drive/upload-sessions/s1/complete":
			io.WriteString(w, `{"id":"f1","revision":4,"size":10}`)
		default:
			http.Error(w, r.Method+" "+r.URL.Path, 404)
		}
	})
	data := []byte("0123456789")
	mtime := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	f, err := c.Upload(context.Background(), "w1", Upload{File: "f1", Revision: 3, Size: 10, Mtime: mtime}, bytes.NewReader(data))
	if err != nil || f.Revision != 4 || string(got) != string(data) || !dropped {
		t.Fatalf("got %+v %q dropped=%v: %v", f, got, dropped, err)
	}
}

func TestRedeem(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/devices/token" || r.Header.Get("Authorization") != "" ||
			r.FormValue("code") != "c1" || r.FormValue("verifier") != "v1" {
			http.Error(w, "bad", 400)
			return
		}
		io.WriteString(w, `{"token":"kyd_new","devices_url":"https://drive/account"}`)
	})
	c.Token = ""
	tok, err := c.Redeem(context.Background(), "c1", "v1")
	if err != nil || tok.Token != "kyd_new" || tok.DevicesURL != "https://drive/account" {
		t.Fatalf("got %+v, %v", tok, err)
	}
}

func TestRevokeSelf(t *testing.T) {
	calls := 0
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/api/devices/self" || r.Header.Get("Authorization") != "Bearer kyd_secret" {
			http.Error(w, "bad", 400)
			return
		}
		if calls++; calls == 1 {
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"device_revoked","wipe":false}`)
	})
	// A second call finds the device already revoked, which is the same outcome.
	for range 2 {
		if err := c.RevokeSelf(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
