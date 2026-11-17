package engine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
)

// TestRealServerTLSPinRollover links through a TLS front to a real kydrive-server that signs
// the certificate in KYDRIVE_TLS_CERT_FILE, renews it, then has the front present a leaf the
// server did not sign (an interceptor's).
func TestRealServerTLSPinRollover(t *testing.T) {
	bin := os.Getenv("KYDRIVE_SERVER_BIN")
	if bin == "" {
		t.Skip("needs KYDRIVE_SERVER_BIN")
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	serial := int64(1)
	leaf := func() tls.Certificate {
		serial++
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: c}
	}
	certFile := filepath.Join(t.TempDir(), "front.pem")
	var mu sync.Mutex
	var served tls.Certificate
	var front *httptest.Server
	// serve has the front present c; signed also writes it where the server reads its certificate.
	serve := func(c tls.Certificate, signed bool) {
		if signed {
			pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})
			if err := os.WriteFile(certFile, pemBytes, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		served = c
		mu.Unlock()
		if front != nil {
			front.CloseClientConnections()
		}
	}
	a := leaf()
	serve(a, true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	t.Cleanup(hc.CloseIdleConnections)
	r := startServerAt(t, bin, &tlsFront{
		url:    "https://" + ln.Addr().String(),
		env:    []string{"KYDRIVE_TLS_CERT_FILE=" + certFile},
		client: hc,
		start: func(target string) {
			proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: target})
			proxy.ErrorLog = log.New(io.Discard, "", 0) // dial errors while the server starts
			// The server sees the public origin the device signs, as behind a real front.
			director := proxy.Director
			proxy.Director = func(req *http.Request) { director(req); req.Host = ln.Addr().String() }
			front = &httptest.Server{Listener: ln, Config: &http.Server{Handler: proxy, ErrorLog: log.New(io.Discard, "", 0)},
				TLS: &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
					mu.Lock()
					defer mu.Unlock()
					return &tls.Config{Certificates: []tls.Certificate{served}}, nil
				}}}
			front.StartTLS()
			t.Cleanup(front.Close)
		},
	})
	if r.pin.Leaf != api.SPKIPin(a.Leaf) || r.pin.IdentityKey == "" {
		t.Fatalf("link pin %+v", r.pin)
	}
	store := api.NewMemoryPins(r.pin)
	c := &api.Client{Base: r.base, Token: r.token, Key: r.key, HTTP: hc, Pins: &api.Pins{Store: store}}
	ctx := context.Background()
	if _, err := c.Workspaces(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // the renewal's statement must be newer than the link, in seconds

	b := leaf()
	serve(b, true)
	if _, err := c.Workspaces(ctx); err != nil {
		t.Fatalf("after renewal: %v", err)
	}
	pin, _ := store.Current()
	if pin.Leaf != api.SPKIPin(b.Leaf) || pin.IdentityKey != r.pin.IdentityKey || pin.Floor <= r.pin.Floor {
		t.Fatalf("pin after renewal %+v", pin)
	}

	serve(leaf(), false) // a locally trusted interceptor; the server still signs b
	if _, err := c.Workspaces(ctx); !errors.Is(err, api.ErrPinChanged) {
		t.Fatalf("interceptor accepted: %v", err)
	}
	if now, _ := store.Current(); now != pin {
		t.Fatalf("pin moved to %+v", now)
	}
}
