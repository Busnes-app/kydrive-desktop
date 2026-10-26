package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/engine"
	"github.com/Busnes-app/kydrive-desktop/core/link"
	"github.com/zalando/go-keyring"
)

// Use the production daemon client with fixture roots, never InsecureSkipVerify.
func tlsDaemon(t *testing.T, roots *x509.CertPool) *daemon {
	t.Helper()
	dir := t.TempDir()
	d, err := newDaemon(dir, filepath.Join(dir, "files"), filepath.Join(dir, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	tr := d.http.Transport.(*http.Transport)
	tr.TLSClientConfig = &tls.Config{RootCAs: roots}
	t.Cleanup(func() { tr.CloseIdleConnections(); d.db.Close() })
	return d
}

type tlsKeyCase struct {
	name, root, intermediate, leaf string
	allowed                        bool
}

func fixtureKey(t *testing.T, kind string) crypto.Signer {
	t.Helper()
	switch kind {
	case "RSA1024", "RSA2047", "RSA2048":
		bits := map[string]int{"RSA1024": 1024, "RSA2047": 2047, "RSA2048": 2048}[kind]
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			t.Fatal(err)
		}
		return k
	case "P224", "P256":
		curve := elliptic.P256()
		if kind == "P224" {
			curve = elliptic.P224()
		}
		k, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	case "Ed25519":
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	default:
		t.Fatalf("unknown fixture key %s", kind)
		return nil
	}
}

func tlsFixture(t *testing.T, tc tlsKeyCase) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	makeCert := func(key crypto.Signer, parent *x509.Certificate, signer crypto.Signer, ca bool, serial int64) ([]byte, *x509.Certificate) {
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: fmt.Sprintf("synthetic-%d", serial)}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: ca, KeyUsage: x509.KeyUsageDigitalSignature}
		if ca {
			c.KeyUsage |= x509.KeyUsageCertSign
		} else {
			c.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
			c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		if parent == nil {
			parent = c
			signer = key
		}
		der, err := x509.CreateCertificate(rand.Reader, c, parent, key.Public(), signer)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return der, parsed
	}
	leafKey := fixtureKey(t, tc.leaf)
	roots := x509.NewCertPool()
	if tc.root == "" {
		der, cert := makeCert(leafKey, nil, nil, false, 1)
		roots.AddCert(cert)
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: leafKey}, roots
	}
	rootKey := fixtureKey(t, tc.root)
	_, root := makeCert(rootKey, nil, nil, true, 1)
	roots.AddCert(root)
	parent, signer := root, rootKey
	var intermediateDER []byte
	if tc.intermediate != "" {
		intermediateKey := fixtureKey(t, tc.intermediate)
		intermediateDER, parent = makeCert(intermediateKey, root, rootKey, true, 2)
		signer = intermediateKey
	}
	leafDER, _ := makeCert(leafKey, parent, signer, false, 3)
	chain := [][]byte{leafDER}
	if intermediateDER != nil {
		chain = append(chain, intermediateDER)
	}
	return tls.Certificate{Certificate: chain, PrivateKey: leafKey}, roots
}

func TestDaemonRejectsUndersizedTLSKeys(t *testing.T) {
	for _, tc := range []tlsKeyCase{
		{name: "RSA1024 leaf", leaf: "RSA1024"},
		{name: "RSA2047 leaf", leaf: "RSA2047"},
		{name: "RSA2048 leaf", leaf: "RSA2048", allowed: true},
		{name: "RSA1024 root", root: "RSA1024", leaf: "P256"},
		{name: "RSA1024 intermediate", root: "P256", intermediate: "RSA1024", leaf: "P256"},
		{name: "P224 root", root: "P224", leaf: "P256"},
		{name: "P256 chain", root: "P256", intermediate: "P256", leaf: "P256", allowed: true},
		{name: "Ed25519 chain", root: "Ed25519", leaf: "Ed25519", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, roots := tlsFixture(t, tc)
			var received atomic.Int32
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/devices/token":
					io.WriteString(w, `{"token":"kyd_synthetic"}`)
				case "/api/drive/files/f/download":
					io.WriteString(w, "data")
				case "/api/drive/workspaces/w/upload-sessions":
					io.WriteString(w, `{"id":"s"}`)
				case "/api/drive/upload-sessions/s":
					w.WriteHeader(204)
				case "/api/drive/upload-sessions/s/complete":
					io.WriteString(w, `{"id":"f"}`)
				default:
					io.WriteString(w, "[]")
				}
			}))
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
			srv.StartTLS()
			defer srv.Close()
			d := tlsDaemon(t, roots)
			c := &api.Client{Base: srv.URL, Token: "kyd_synthetic", HTTP: d.http}
			operations := []struct {
				name string
				run  func() error
			}{
				{"workspace API", func() error { _, err := c.Workspaces(context.Background()); return err }},
				{"redemption", func() error { _, err := c.Redeem(context.Background(), "synthetic", "synthetic"); return err }},
				{"download", func() error {
					hash := sha256.Sum256([]byte("data"))
					return c.Download(context.Background(), api.File{ID: "f", Size: 4, Digest: hex.EncodeToString(hash[:])}, io.Discard)
				}},
				{"upload", func() error {
					_, err := c.Upload(context.Background(), "w", api.Upload{Name: "file", Size: 4}, bytes.NewReader([]byte("data")))
					return err
				}},
				{"browser link exchange", func() error {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_, err := link.Link(ctx, link.Request{Server: srv.URL, HTTP: d.http, Open: func(raw string) error {
						u, err := url.Parse(raw)
						if err != nil {
							return err
						}
						q := u.Query()
						response, err := http.Get(q.Get("redirect") + "?code=synthetic&state=" + url.QueryEscape(q.Get("state")))
						if response != nil {
							response.Body.Close()
						}
						return err
					}})
					return err
				}},
			}
			for _, op := range operations {
				t.Run(op.name, func(t *testing.T) {
					before := received.Load()
					err := op.run()
					if (err == nil) != tc.allowed {
						t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
					}
					if !tc.allowed && received.Load() != before {
						t.Fatal("delivered a request to an undersized TLS peer")
					}
				})
			}
		})
	}
}

func TestDaemonRefusesRedirects(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "[]") }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	roots := x509.NewCertPool()
	roots.AddCert(source.Certificate())
	d := tlsDaemon(t, roots)
	c := &api.Client{Base: source.URL, Token: "kyd_synthetic", HTTP: d.http}
	if _, err := c.Workspaces(context.Background()); err == nil {
		t.Fatal("followed an HTTPS-to-HTTP redirect")
	}
	if received.Load() != 0 {
		t.Fatal("redirect destination received a request")
	}
}

func TestDaemonStillRequiresTrustAndHostname(t *testing.T) {
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "[]") }))
	defer source.Close()
	source.Config.ErrorLog = log.New(io.Discard, "", 0)
	d := tlsDaemon(t, x509.NewCertPool())
	c := &api.Client{Base: source.URL, HTTP: d.http}
	if _, err := c.Workspaces(context.Background()); err == nil {
		t.Fatal("accepted an untrusted certificate")
	}
	roots := x509.NewCertPool()
	roots.AddCert(source.Certificate())
	tr := d.http.Transport.(*http.Transport)
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "wrong.example.invalid"}
	if _, err := c.Workspaces(context.Background()); err == nil {
		t.Fatal("accepted the wrong hostname")
	}
}

func TestTLSFailureKeepsCredentialsBindingsAndLocalEdits(t *testing.T) {
	cert, roots := tlsFixture(t, tlsKeyCase{leaf: "RSA1024"})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("request reached weak peer") }))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	d := tlsDaemon(t, roots)
	keyring.MockInit()
	const token = "kyd_synthetic_preserve"
	if err := keyring.Set(keyringService, srv.URL, token); err != nil {
		t.Fatal(err)
	}
	if err := d.db.Set("server", srv.URL); err != nil {
		t.Fatal(err)
	}
	binding := engine.Workspace{ID: "w", Name: "Docs", Dir: "Docs", Cursor: 17}
	if err := d.db.PutWorkspace(binding); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d.root, "Docs"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(d.root, "Docs", "local.txt")
	if err := os.WriteFile(file, []byte("unuploaded edit"), 0600); err != nil {
		t.Fatal(err)
	}
	d.token = token
	d.pass(context.Background(), false)
	if d.state != "error" || d.lastErr == "" {
		t.Fatalf("state=%q, error=%q", d.state, d.lastErr)
	}
	saved, err := keyring.Get(keyringService, srv.URL)
	if err != nil || saved != token || d.token != token {
		t.Fatal("TLS failure removed a credential")
	}
	bindings, err := d.db.Workspaces()
	if err != nil || len(bindings) != 1 || bindings[0] != binding || d.db.Get("server") != srv.URL {
		t.Fatal("TLS failure changed bindings")
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "unuploaded edit" {
		t.Fatal("TLS failure lost the local edit")
	}
}
