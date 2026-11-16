package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// The vector is kydrive-server internal/api/testdata/device-signing-v1.json, byte for byte.
func TestSigningGoldenVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/device-signing-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		AppURL        string            `json:"app_url"`
		Body          string            `json:"body"`
		Date          string            `json:"date"`
		Headers       map[string]string `json:"headers"`
		Message       string            `json:"message"`
		Method        string            `json:"method"`
		Nonce         string            `json:"nonce"`
		Origin        string            `json:"origin"`
		PublicKey     string            `json:"public_key_spki_b64"`
		RequestURI    string            `json:"request_uri"`
		Signature     string            `json:"signature_b64"`
		SignedHeaders string            `json:"signed_headers"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if got := canonicalOrigin(v.AppURL); got != v.Origin {
		t.Fatalf("origin %q, want %q", got, v.Origin)
	}
	h := http.Header{}
	for k, val := range v.Headers {
		h.Set(k, val)
	}
	msg, err := message(v.Origin, v.Method, v.RequestURI, v.Date, v.Nonce, h, hexSum([]byte(v.Body)))
	if err != nil || msg != v.Message {
		t.Fatalf("message %q (%v), want %q", msg, err, v.Message)
	}
	if !strings.Contains(msg, "|"+hexSum([]byte(v.SignedHeaders))+"|") {
		t.Fatal("signed headers digest differs from the vector's")
	}
	der, _ := base64.StdEncoding.DecodeString(v.PublicKey)
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.StdEncoding.DecodeString(v.Signature)
	sum := sha256.Sum256([]byte(msg))
	if !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), sum[:], sig) {
		t.Fatal("vector signature does not verify")
	}
}

func TestOriginCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"https://Drive.Example":       "https://drive.example",
		"https://drive.example:8443/": "https://drive.example:8443",
		"http://127.0.0.1:80":         "http://127.0.0.1",
		"http://[::1]:8080":           "http://[::1]:8080",
	} {
		if got := canonicalOrigin(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestMessageRefusesAmbiguousFields(t *testing.T) {
	if _, err := message("https://d", "GET", "/", "1", "n", http.Header{"Depth": {"1", "2"}}, "d"); err == nil {
		t.Error("a signed header sent twice accepted")
	}
	if _, err := message("https://d", "GET", "/a|b", "1", "n", nil, "d"); err == nil {
		t.Error("request URI with | accepted")
	}
}

// verifier plays the server: it rebuilds each request's message and checks its signature,
// body digest and nonce, as kydrive-server devicesig.go does.
func verifier(t *testing.T, key *ecdsa.PublicKey, h http.HandlerFunc) *Client {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	var c *Client
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		date, nonce, digest := r.Header.Get("KyDrive-Date"), r.Header.Get("KyDrive-Nonce"), r.Header.Get("KyDrive-Content-SHA256")
		ms, _ := strconv.ParseInt(date, 10, 64)
		n, _ := base64.RawURLEncoding.DecodeString(nonce)
		sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("KyDrive-Signature"))
		msg, err := message(canonicalOrigin(c.Base), r.Method, r.RequestURI, date, nonce, r.Header, digest)
		sum := sha256.Sum256([]byte(msg))
		mu.Lock()
		replay := seen[nonce]
		seen[nonce] = true
		mu.Unlock()
		switch {
		case err != nil, len(n) != 16, replay, time.Since(time.UnixMilli(ms)).Abs() > time.Minute,
			digest != hexSum(body), !ecdsa.VerifyASN1(key, sum[:], sig):
			t.Errorf("%s %s: bad signature (%v)", r.Method, r.RequestURI, err)
			http.Error(w, `{"error":"bad signature"}`, 401)
			return
		}
		// A body over the server's 1 MiB buffer is refused without a Content-Length.
		if len(body) > 0 && r.ContentLength != int64(len(body)) {
			t.Errorf("%s %s: Content-Length %d for %d bytes", r.Method, r.RequestURI, r.ContentLength, len(body))
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c = &Client{Base: srv.URL, Token: "kyd_secret", HTTP: srv.Client()}
	return c
}

func TestSignedRequests(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var mu sync.Mutex
	var got []string
	c := verifier(t, &key.PublicKey, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Range"))
		mu.Unlock()
		switch {
		case r.Method == "GET":
			io.WriteString(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/upload-sessions"):
			io.WriteString(w, `{"id":"s 1"}`)
		case strings.HasSuffix(r.URL.Path, "/complete"):
			io.WriteString(w, `{"id":"f1","revision":1}`)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		}
	})
	c.Key = key
	ctx := context.Background()
	if _, err := c.Workspaces(ctx); err != nil {
		t.Fatal(err)
	}
	defer func(n int64) { chunkSize = n }(chunkSize)
	chunkSize = 3
	data := []byte("hello")
	if _, err := c.Upload(ctx, "w 1", Upload{Name: "a.txt", Size: 5, Mtime: time.Now()}, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := c.RevokeSelf(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /api/drive/workspaces ", "POST /api/drive/workspaces/w 1/upload-sessions ", "PUT /api/drive/upload-sessions/s 1 bytes 0-2/5",
		"PUT /api/drive/upload-sessions/s 1 bytes 3-4/5", "POST /api/drive/upload-sessions/s 1/complete ", "DELETE /api/devices/self "}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(got, "\n"))
	}
}

// A computer linked before signing has no key and keeps sending its token alone.
func TestUnsignedWithoutKey(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("KyDrive-Signature") != "" || r.Header.Get("KyDrive-Date") != "" {
			http.Error(w, "signed", 400)
			return
		}
		io.WriteString(w, `[]`)
	})
	if _, err := c.Workspaces(context.Background()); err != nil {
		t.Fatal(err)
	}
}
