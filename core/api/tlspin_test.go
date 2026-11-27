package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/hybridsig"
)

func TestTLSPinGoldenVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/tls-pin-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Host, Pin, Signature, Statement string
		IdentityKey                     string `json:"identity_key"`
		IssuedAt                        int64  `json:"issued_at"`
		SeedHex                         string `json:"seed_hex"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if got := string(pinStatement(v.Host, v.Pin, v.IssuedAt)); got != v.Statement {
		t.Fatalf("statement %q, want %q", got, v.Statement)
	}
	seed, _ := hex.DecodeString(v.SeedHex)
	priv := ed25519.NewKeyFromSeed(seed)
	if base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)) != v.IdentityKey {
		t.Fatal("seed does not give the identity key")
	}
	if got := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(v.Statement))); got != v.Signature {
		t.Fatalf("signature %s, want %s", got, v.Signature)
	}
	s := tlsPinStatement{Host: v.Host, Pin: v.Pin, IssuedAt: v.IssuedAt, Signature: v.Signature}
	stored := Pin{Leaf: "sha256/old", IdentityKey: v.IdentityKey, Floor: v.IssuedAt - 1}
	if next, ok := accept(stored, v.Host, v.Pin, s, v.IssuedAt); !ok || next != (Pin{Leaf: v.Pin, IdentityKey: v.IdentityKey, Floor: v.IssuedAt}) {
		t.Fatalf("golden statement refused: %v %v", next, ok)
	}
}

// pinServer is an HTTPS server whose leaf can change, signed by a test-only CA for 127.0.0.1.
type pinServer struct {
	t        *testing.T
	srv      *httptest.Server
	roots    *x509.CertPool
	ca       *x509.Certificate
	caKey    *ecdsa.PrivateKey
	identity ed25519.PrivateKey
	serve    func() *tls.Certificate // the leaf of the next handshake

	mu       sync.Mutex
	requests []*http.Request // every request that reached a handler
	// statement answers GET /api/devices/tls-pin for the leaf being served; nil means 503.
	statement       func(leaf string) any
	device          string            // identity_key in GET /api/drive/device and POST /api/devices/token
	deviceHybrid    string            // identity_key_hybrid in device/token replies
	deviceHybridSig string            // identity_key_hybrid_sig in device/token replies
	served          map[string]string // the leaf pin each connection got, by client address
}

func testHybridKey(t *testing.T, edKey ed25519.PrivateKey) *hybridsig.PrivateKey {
	t.Helper()
	edSeed := edKey.Seed()
	mldsaSeed, err := hkdf.Key(sha256.New, edSeed, nil, "kydrive-identity-mldsa65/1", hybridsig.SeedBytes)
	if err != nil {
		t.Fatal(err)
	}
	hk, err := hybridsig.FromSeeds(mldsaSeed, edSeed)
	if err != nil {
		t.Fatal(err)
	}
	return hk
}

func newPinServer(t *testing.T) *pinServer {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	_, id, _ := ed25519.GenerateKey(rand.Reader)
	p := &pinServer{t: t, ca: ca, caKey: caKey, roots: x509.NewCertPool(), identity: id, served: map[string]string{}}
	p.roots.AddCert(ca)
	p.statement = func(leaf string) any { return p.sign(id, "127.0.0.1", leaf, time.Now().Unix()) }
	p.srv = httptest.NewUnstartedServer(http.HandlerFunc(p.handle))
	p.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	// Per handshake: httptest adds its own certificate, which an IP client (no SNI) would get.
	p.srv.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		c := p.serve()
		p.mu.Lock()
		p.served[hello.Conn.RemoteAddr().String()] = pinOf(c)
		p.mu.Unlock()
		return &tls.Config{Certificates: []tls.Certificate{*c}}, nil
	}}
	p.srv.StartTLS()
	t.Cleanup(p.srv.Close)
	return p
}

func (p *pinServer) leaf() *tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		p.t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: c}
}

// use serves leaf from the next handshake on.
func (p *pinServer) use(leaf *tls.Certificate) {
	p.serve = func() *tls.Certificate { return leaf }
	p.srv.CloseClientConnections()
}

func pinOf(c *tls.Certificate) string { return SPKIPin(c.Leaf) }

func (p *pinServer) sign(key ed25519.PrivateKey, host, pin string, at int64) tlsPinStatement {
	sig := ed25519.Sign(key, pinStatement(host, pin, at))
	return tlsPinStatement{Host: host, Pin: pin, IssuedAt: at, Signature: base64.StdEncoding.EncodeToString(sig)}
}

func (p *pinServer) signV2(key ed25519.PrivateKey, host, pin string, at int64) tlsPinStatement {
	s := p.sign(key, host, pin, at)
	hk := testHybridKey(p.t, key)
	sigV2, err := hk.Sign(tlsPinContextV2, pinStatementBodyV2(host, pin, at))
	if err != nil {
		p.t.Fatal(err)
	}
	s.SignatureV2 = base64.StdEncoding.EncodeToString(sigV2)
	s.IdentityKeyHybrid = base64.StdEncoding.EncodeToString(hk.Public().Bytes())
	return s
}

func (p *pinServer) bindHybrid(edKey ed25519.PrivateKey, hybridPub *hybridsig.PublicKey) string {
	sig := ed25519.Sign(edKey, identityHybridBindStatement(hybridPub.ID()))
	return base64.StdEncoding.EncodeToString(sig)
}

func (p *pinServer) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.requests = append(p.requests, r)
	statement, device, deviceHybrid, deviceHybridSig, leaf := p.statement, p.device, p.deviceHybrid, p.deviceHybridSig, p.served[r.RemoteAddr]
	p.mu.Unlock()
	switch r.URL.Path {
	case "/api/devices/tls-pin":
		if statement == nil {
			http.Error(w, `{"error":"unavailable"}`, 503)
			return
		}
		json.NewEncoder(w).Encode(statement(leaf))
	case "/api/drive/device":
		resp := map[string]string{"user_id": "u1", "user": "alice", "identity_key": device}
		if deviceHybrid != "" {
			resp["identity_key_hybrid"] = deviceHybrid
		}
		if deviceHybridSig != "" {
			resp["identity_key_hybrid_sig"] = deviceHybridSig
		}
		json.NewEncoder(w).Encode(resp)
	case "/api/devices/token":
		resp := map[string]string{"token": "kyd_fresh", "user_id": "u1", "user": "alice", "identity_key": device}
		if deviceHybrid != "" {
			resp["identity_key_hybrid"] = deviceHybrid
		}
		if deviceHybridSig != "" {
			resp["identity_key_hybrid_sig"] = deviceHybridSig
		}
		json.NewEncoder(w).Encode(resp)
	default:
		io.WriteString(w, "[]")
	}
}

func (p *pinServer) idKey() string {
	return base64.StdEncoding.EncodeToString(p.identity.Public().(ed25519.PublicKey))
}

// paths lists the requests that reached the server since the last call.
func (p *pinServer) paths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.requests {
		out = append(out, r.URL.Path)
		if r.URL.Path == "/api/devices/tls-pin" && (r.Header.Get("Authorization") != "" || r.Header.Get("KyDrive-Signature") != "") {
			p.t.Error("the rollover fetch carried a credential")
		}
	}
	p.requests = nil
	return out
}

func (p *pinServer) client(store PinStore) *Client {
	key, _ := GenerateSigningKey()
	base := &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: p.roots}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	p.t.Cleanup(base.CloseIdleConnections)
	return &Client{Base: p.srv.URL, Token: "kyd_secret", Key: key, HTTP: base, Pins: &Pins{Store: store}}
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var ctx = context.Background()

// A wrong leaf fails in the handshake: nothing reaches the server, so no token is sent.
func TestPinWrongLeafSendsNothing(t *testing.T) {
	p := newPinServer(t)
	a, b := p.leaf(), p.leaf()
	p.use(b)
	store := NewMemoryPins(Pin{Leaf: pinOf(a), Floor: 1}) // no identity key
	if _, err := p.client(store).Workspaces(ctx); !errors.Is(err, ErrPinChanged) {
		t.Fatalf("err = %v", err)
	}
	if got := p.paths(); len(got) != 0 {
		t.Fatalf("reached the server: %v", got)
	}
	if pin, _ := store.Current(); pin.Leaf != pinOf(a) {
		t.Fatal("repinned without a signed statement")
	}
}

// Trust on first use, then the identity key from the device reply, then a signed renewal.
func TestPinTrustOnFirstUseAndSignedRollover(t *testing.T) {
	p := newPinServer(t)
	a, b := p.leaf(), p.leaf()
	p.use(a)
	p.device = p.idKey()
	store := NewMemoryPins(Pin{})
	c := p.client(store)
	c.Pins.Now = func() time.Time { return time.Now().Add(-time.Minute) } // first pinned a minute ago
	if _, err := c.Workspaces(ctx); err != nil {
		t.Fatal(err)
	}
	c.Pins.Now = nil
	c.LearnIdentity(ctx)
	c.LearnIdentity(ctx) // once per session
	pin, _ := store.Current()
	if pin.Leaf != pinOf(a) || pin.IdentityKey != p.idKey() || pin.Floor == 0 {
		t.Fatalf("pin after first use: %+v", pin)
	}
	if got := p.paths(); !eq(got, []string{"/api/drive/workspaces", "/api/drive/device"}) {
		t.Fatalf("requests %v", got)
	}
	p.use(b)
	if _, err := c.Workspaces(ctx); err != nil {
		t.Fatal(err)
	}
	if got := p.paths(); !eq(got, []string{"/api/devices/tls-pin", "/api/drive/workspaces"}) {
		t.Fatalf("requests %v", got)
	}
	next, _ := store.Current()
	if next.Leaf != pinOf(b) || next.IdentityKey != p.idKey() || next.Floor <= pin.Floor {
		t.Fatalf("pin after renewal: %+v", next)
	}
	// Replay the retired leaf's earlier statement, not a newly authorized rollover.
	retired := p.sign(p.identity, "127.0.0.1", pinOf(a), pin.Floor)
	p.statement = func(string) any { return retired }
	p.use(a)
	if _, err := c.Workspaces(ctx); !errors.Is(err, ErrPinChanged) {
		t.Fatalf("old leaf accepted: %v", err)
	}
	if got := p.paths(); !eq(got, []string{"/api/devices/tls-pin"}) {
		t.Fatalf("requests reaching the retired leaf: %v", got)
	}
	if held, _ := store.Current(); held != next {
		t.Fatal("retired statement changed the stored pin")
	}
}

func TestPinRolloverRefusals(t *testing.T) {
	now := time.Now().Unix()
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]func(p *pinServer, served, floor int64) func(string) any{
		"relayed for another leaf": func(p *pinServer, _, _ int64) func(string) any {
			c := pinOf(p.leaf())
			return func(string) any { return p.sign(p.identity, "127.0.0.1", c, now) }
		},
		"stale": func(p *pinServer, _, _ int64) func(string) any {
			return func(l string) any { return p.sign(p.identity, "127.0.0.1", l, now-maxSkew-60) }
		},
		"future": func(p *pinServer, _, _ int64) func(string) any {
			return func(l string) any { return p.sign(p.identity, "127.0.0.1", l, now+maxSkew+60) }
		},
		"replay at the floor": func(p *pinServer, _, floor int64) func(string) any {
			return func(l string) any { return p.sign(p.identity, "127.0.0.1", l, floor) }
		},
		"another host": func(p *pinServer, _, _ int64) func(string) any {
			return func(l string) any { return p.sign(p.identity, "drive.example", l, now) }
		},
		"another key": func(p *pinServer, _, _ int64) func(string) any {
			return func(l string) any { return p.sign(other, "127.0.0.1", l, now) }
		},
		"bad signature": func(p *pinServer, _, _ int64) func(string) any {
			return func(l string) any { s := p.sign(p.identity, "127.0.0.1", l, now); s.IssuedAt++; return s }
		},
		"unavailable (503)": func(*pinServer, int64, int64) func(string) any { return nil },
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			p := newPinServer(t)
			a, b := p.leaf(), p.leaf()
			p.use(b)
			floor := now - 3600
			p.statement = statement(p, now, floor)
			want := Pin{Leaf: pinOf(a), IdentityKey: p.idKey(), Floor: floor}
			store := NewMemoryPins(want)
			if _, err := p.client(store).Workspaces(ctx); !errors.Is(err, ErrPinChanged) {
				t.Fatalf("err = %v", err)
			}
			if got := p.paths(); !eq(got, []string{"/api/devices/tls-pin"}) {
				t.Fatalf("requests %v", got)
			}
			if pin, _ := store.Current(); pin != want {
				t.Fatalf("pin moved to %+v", pin)
			}
		})
	}
}

// The freshness floor: a statement signed before this computer pinned (a retired leaf's, from
// the last day) must not roll a new link back.
func TestPinStatementOlderThanFirstPinRefused(t *testing.T) {
	p := newPinServer(t)
	a, b := p.leaf(), p.leaf()
	linked := time.Now().Unix()
	retired := p.sign(p.identity, "127.0.0.1", pinOf(a), linked-3600) // valid, an hour before linking
	p.statement = func(string) any { return retired }
	p.device = p.idKey()
	p.use(b)
	store := NewMemoryPins(Pin{})
	c := p.client(store)
	if _, err := c.Workspaces(ctx); err != nil { // pins B
		t.Fatal(err)
	}
	c.LearnIdentity(ctx)
	pinned, _ := store.Current()
	if pinned.Leaf != pinOf(b) || pinned.IdentityKey == "" {
		t.Fatalf("pin %+v", pinned)
	}
	p.paths()
	p.use(a)
	if _, err := c.Workspaces(ctx); !errors.Is(err, ErrPinChanged) {
		t.Fatalf("err = %v", err)
	}
	if got := p.paths(); !eq(got, []string{"/api/devices/tls-pin"}) {
		t.Fatalf("requests reaching the retired leaf: %v", got)
	}
	if now, _ := store.Current(); now != pinned {
		t.Fatalf("rolled back to %+v", now)
	}
}

// The identity key is learned once, over the pinned leaf, and never replaced.
func TestPinIdentityKeyWriteOnce(t *testing.T) {
	p := newPinServer(t)
	a := p.leaf()
	p.use(a)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	p.device = base64.StdEncoding.EncodeToString(other.Public().(ed25519.PublicKey))
	want := Pin{Leaf: pinOf(a), IdentityKey: p.idKey(), Floor: 1}
	store := NewMemoryPins(want)
	if _, err := p.client(store).DeviceAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if pin, _ := store.Current(); pin != want {
		t.Fatalf("identity key replaced: %+v", pin)
	}
	p.device = "not a key"
	none := NewMemoryPins(Pin{Leaf: pinOf(a), Floor: 1})
	p.client(none).DeviceAccount(ctx)
	if pin, _ := none.Current(); pin.IdentityKey != "" {
		t.Fatal("learned a malformed identity key")
	}
}

// A server flapping between leaves rolls over once and fails the retry.
func TestPinFlappingRollsOnce(t *testing.T) {
	p := newPinServer(t)
	a, b := p.leaf(), p.leaf()
	// Handshakes: the request meets B, the fetch meets B and repins, the retry meets A.
	var n atomic.Int32
	p.serve = func() *tls.Certificate {
		if n.Add(1) <= 2 {
			return b
		}
		return a
	}
	store := NewMemoryPins(Pin{Leaf: pinOf(a), IdentityKey: p.idKey(), Floor: 1})
	c := p.client(store)
	c.HTTP.Transport.(*http.Transport).DisableKeepAlives = true
	if _, err := c.Workspaces(ctx); !errors.Is(err, ErrPinChanged) {
		t.Fatalf("err = %v", err)
	}
	if got := p.paths(); !eq(got, []string{"/api/devices/tls-pin"}) {
		t.Fatalf("requests %v", got)
	}
	if pin, _ := store.Current(); pin.Leaf != pinOf(b) {
		t.Fatalf("pin %+v", pin)
	}
}

// Concurrent requests meeting the same renewal share one fetch.
func TestPinConcurrentRolloverFetchesOnce(t *testing.T) {
	p := newPinServer(t)
	a, b := p.leaf(), p.leaf()
	p.use(b)
	store := NewMemoryPins(Pin{Leaf: pinOf(a), IdentityKey: p.idKey(), Floor: 1})
	c := p.client(store)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := c.Workspaces(ctx); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	fetches := 0
	for _, path := range p.paths() {
		if path == "/api/devices/tls-pin" {
			fetches++
		}
	}
	if fetches != 1 {
		t.Fatalf("%d rollover fetches", fetches)
	}
}

type endedStore struct{}

func (endedStore) Current() (Pin, error) { return Pin{}, errors.New("ended") }
func (endedStore) Replace(Pin, Pin) bool { return false }

// An ended session sends nothing, never falling back to an unpinned request.
func TestPinEndedSessionSendsNothing(t *testing.T) {
	p := newPinServer(t)
	p.use(p.leaf())
	if _, err := p.client(endedStore{}).Workspaces(ctx); err == nil {
		t.Fatal("an ended session sent a request")
	}
	if got := p.paths(); len(got) != 0 {
		t.Fatalf("requests %v", got)
	}
}

// Redeeming a link code pins the leaf it met and keeps the identity key from the reply.
func TestRedeemPinsTheLeaf(t *testing.T) {
	p := newPinServer(t)
	a := p.leaf()
	p.use(a)
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "kyd_new", "user_id": "u1", "user": "alice", "identity_key": p.idKey()})
	})
	c := p.client(nil)
	c.Token, c.Pins = "", nil
	key, _ := GenerateSigningKey()
	before := time.Now().Unix()
	tok, err := c.Redeem(ctx, "code", "verifier", key)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Pin.Leaf != pinOf(a) || tok.Pin.IdentityKey != p.idKey() || tok.Pin.Floor < before {
		t.Fatalf("pin %+v", tok.Pin)
	}
}

// A server without an identity key is asked once per session, not on every pass.
func TestPinLearnIdentityAsksOnce(t *testing.T) {
	p := newPinServer(t)
	a := p.leaf()
	p.use(a)
	c := p.client(NewMemoryPins(Pin{Leaf: pinOf(a), Floor: 1}))
	c.LearnIdentity(ctx)
	c.LearnIdentity(ctx)
	if got := p.paths(); !eq(got, []string{"/api/drive/device"}) {
		t.Fatalf("requests %v", got)
	}
}

// Through an HTTPS proxy the proxy's own handshake is not the server's: the pin is enforced on
// the tunnelled one.
func TestPinThroughHTTPSProxy(t *testing.T) {
	p := newPinServer(t)
	a := p.leaf()
	p.use(a)
	target := strings.TrimPrefix(p.srv.URL, "https://")
	var tunnels atomic.Int32
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != target {
			http.Error(w, "no", 403)
			return
		}
		tunnels.Add(1)
		up, err := net.Dial("tcp", target)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		w.WriteHeader(200)
		down, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			up.Close()
			t.Errorf("hijack CONNECT: %v", err)
			return
		}
		// Force the ClientHello into the reader, as net/http may do before Hijack returns.
		if _, err := buffered.Peek(1); err != nil {
			up.Close()
			down.Close()
			t.Errorf("buffer tunneled handshake: %v", err)
			return
		}
		go func() { io.Copy(up, buffered); up.Close() }()
		io.Copy(down, up)
		down.Close()
	}))
	proxy.Config.ErrorLog = log.New(io.Discard, "", 0)
	proxy.TLS = &tls.Config{Certificates: []tls.Certificate{*p.leaf()}}
	proxy.StartTLS()
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(strings.Replace(proxy.URL, "127.0.0.1", "localhost", 1))
	for _, leaf := range []*tls.Certificate{a, p.leaf()} {
		held := Pin{Leaf: pinOf(leaf), Floor: 1}
		store := NewMemoryPins(held)
		c := p.client(store)
		c.HTTP.Transport.(*http.Transport).Proxy = http.ProxyURL(proxyURL)
		c.HTTP.Timeout = 10 * time.Second // bound a broken tunnel instead of hanging the suite
		_, err := c.Workspaces(ctx)
		if leaf == a && err != nil {
			t.Fatalf("pinned server through the proxy: %v", err)
		}
		if leaf != a && !errors.Is(err, ErrPinChanged) {
			t.Fatalf("wrong leaf through the proxy: %v", err)
		}
		if got, err := store.Current(); err != nil || got != held {
			t.Fatal("proxy changed the held pin", err)
		}
		var want []string
		if leaf == a {
			want = []string{"/api/drive/workspaces"}
		}
		if got := p.paths(); !eq(got, want) {
			t.Fatalf("requests through the proxy: %v, want %v", got, want)
		}
	}
	if tunnels.Load() != 2 {
		t.Fatalf("%d tunnels", tunnels.Load())
	}
}

// A host spelled differently from its SNI (a trailing dot) is still the server, never a proxy.
func TestPinTrailingDotHostStillChecked(t *testing.T) {
	p := newPinServer(t)
	a, b := p.leaf(), p.leaf()
	p.use(b)
	c := p.client(NewMemoryPins(Pin{Leaf: pinOf(a), Floor: 1}))
	c.Base = strings.Replace(p.srv.URL, "127.0.0.1", "localhost.", 1)
	c.HTTP.Transport.(*http.Transport).Proxy = http.ProxyFromEnvironment
	if _, err := c.Workspaces(ctx); !errors.Is(err, ErrPinChanged) {
		t.Fatalf("err = %v", err)
	}
	if got := p.paths(); len(got) != 0 {
		t.Fatalf("requests %v", got)
	}
}

// A pinned session learns the server's hybrid identity key (bound to its Ed25519 key),
// rolls over via ?v=2 with a kydrive-tls-pin/2 hybridsig signature, and refuses v1-only
// downgrade once the hybrid identity key ID is pinned.
func TestPinHybridLearnRolloverAndNoDowngrade(t *testing.T) {
	p := newPinServer(t)
	a, b, cLeaf := p.leaf(), p.leaf(), p.leaf()
	p.use(a)

	hk := testHybridKey(t, p.identity)
	hkB64 := base64.StdEncoding.EncodeToString(hk.Public().Bytes())
	wantHybridID := hk.Public().ID()
	if !ValidHybridKeyID(wantHybridID) {
		t.Fatalf("ValidHybridKeyID(%q) = false", wantHybridID)
	}

	// 1. A hybrid key whose Ed25519 tail does not match the held Ed25519 key is refused,
	// even if signed by the other key.
	_, otherEd, _ := ed25519.GenerateKey(rand.Reader)
	otherHK := testHybridKey(t, otherEd)
	p.device = p.idKey()
	p.deviceHybrid = base64.StdEncoding.EncodeToString(otherHK.Public().Bytes())
	p.deviceHybridSig = p.bindHybrid(otherEd, otherHK.Public())
	store := NewMemoryPins(Pin{Leaf: pinOf(a), IdentityKey: p.idKey(), Floor: 1})
	if _, err := p.client(store).DeviceAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if pin, _ := store.Current(); pin.IdentityKeyHybridID != "" {
		t.Fatalf("learned mismatched hybrid key: %+v", pin)
	}

	// 2. A matching hybrid key signed by the held Ed25519 key is learned once and never replaced.
	p.deviceHybrid = hkB64
	p.deviceHybridSig = p.bindHybrid(p.identity, hk.Public())
	if _, err := p.client(store).DeviceAccount(ctx); err != nil {
		t.Fatal(err)
	}
	pin, _ := store.Current()
	if pin.IdentityKeyHybridID != wantHybridID {
		t.Fatalf("IdentityKeyHybridID = %q, want %q", pin.IdentityKeyHybridID, wantHybridID)
	}
	p.deviceHybrid = base64.StdEncoding.EncodeToString(otherHK.Public().Bytes())
	p.deviceHybridSig = p.bindHybrid(otherEd, otherHK.Public())
	if _, err := p.client(store).DeviceAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if after, _ := store.Current(); after.IdentityKeyHybridID != wantHybridID {
		t.Fatalf("hybrid identity key was overwritten: %+v", after)
	}

	// 3. Downgrade refusal: when hybrid is pinned, a valid v1-only statement (no signature_v2) is refused.
	p.use(b)
	p.statement = func(leaf string) any { return p.sign(p.identity, "127.0.0.1", leaf, time.Now().Unix()) }
	if _, err := p.client(store).Workspaces(ctx); !errors.Is(err, ErrPinChanged) {
		t.Fatalf("v1-only rollover succeeded under hybrid pin: err = %v", err)
	}
	p.mu.Lock()
	var sawV2Query bool
	for _, req := range p.requests {
		if req.URL.Path == "/api/devices/tls-pin" && req.URL.Query().Get("v") == "2" {
			sawV2Query = true
		}
	}
	p.requests = nil
	p.mu.Unlock()
	if !sawV2Query {
		t.Fatal("client did not request /api/devices/tls-pin?v=2")
	}

	// 4. Valid kydrive-tls-pin/2 hybridsig rollover succeeds and preserves IdentityKeyHybridID.
	p.statement = func(leaf string) any { return p.signV2(p.identity, "127.0.0.1", leaf, time.Now().Unix()) }
	if _, err := p.client(store).Workspaces(ctx); err != nil {
		t.Fatalf("v2 hybridsig rollover failed: %v", err)
	}
	rolled, _ := store.Current()
	if rolled.Leaf != pinOf(b) || rolled.IdentityKeyHybridID != wantHybridID {
		t.Fatalf("rolled pin = %+v", rolled)
	}

	// 5. A valid v2 signature from a different hybrid key is refused.
	p.use(cLeaf)
	p.statement = func(leaf string) any { return p.signV2(otherEd, "127.0.0.1", leaf, time.Now().Unix()+1) }
	if _, err := p.client(store).Workspaces(ctx); !errors.Is(err, ErrPinChanged) {
		t.Fatalf("wrong hybrid key rollover succeeded: err = %v", err)
	}
}

// A compromised pinned TLS terminator that lacks the Ed25519 identity private key can graft
// an attacker-chosen ML-DSA-65 public key onto the real Ed25519 public key tail, but cannot
// produce identity_key_hybrid_sig. Both DeviceAccount and RedeemCode must refuse unsigned or
// badly-signed hybrid enrollment so the existing Ed25519 pin stays unchanged and legitimate
// certificate rollover remains available.
func TestPinUnsignedHybridEnrollmentRejected(t *testing.T) {
	p := newPinServer(t)
	a, b := p.leaf(), p.leaf()
	p.use(a)

	// Craft a forged hybrid public key whose Ed25519 tail matches p.identity, but whose
	// ML-DSA-65 component is attacker-chosen (different seed).
	attackerMLDSASeed := make([]byte, hybridsig.SeedBytes)
	attackerMLDSASeed[0] = 0x42
	forgedHK, err := hybridsig.FromSeeds(attackerMLDSASeed, p.identity.Seed())
	if err != nil {
		t.Fatal(err)
	}
	forgedB64 := base64.StdEncoding.EncodeToString(forgedHK.Public().Bytes())
	_, otherEd, _ := ed25519.GenerateKey(rand.Reader)

	origPin := Pin{Leaf: pinOf(a), IdentityKey: p.idKey(), Floor: 1}
	store := NewMemoryPins(origPin)

	// 1. Missing identity_key_hybrid_sig on GET /api/drive/device leaves existing pin unchanged.
	p.device = p.idKey()
	p.deviceHybrid = forgedB64
	p.deviceHybridSig = ""
	if _, err := p.client(store).DeviceAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Current(); got != origPin {
		t.Fatalf("unsigned hybrid enrollment mutated pin: got %+v, want %+v", got, origPin)
	}

	// 2. Signature from wrong key on GET /api/drive/device leaves existing pin unchanged.
	p.deviceHybridSig = p.bindHybrid(otherEd, forgedHK.Public())
	if _, err := p.client(store).DeviceAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Current(); got != origPin {
		t.Fatalf("forged-sig hybrid enrollment mutated pin: got %+v, want %+v", got, origPin)
	}

	// 3. Legitimate Ed25519 certificate rollover remains available after rejected attempts.
	p.use(b)
	p.statement = func(leaf string) any { return p.sign(p.identity, "127.0.0.1", leaf, time.Now().Unix()) }
	if _, err := p.client(store).Workspaces(ctx); err != nil {
		t.Fatalf("legitimate v1 rollover blocked after rejected hybrid enrollment: %v", err)
	}
	if rolled, _ := store.Current(); rolled.Leaf != pinOf(b) || rolled.IdentityKeyHybridID != "" {
		t.Fatalf("unexpected rolled pin: %+v", rolled)
	}

	// 4. Redeem (sign-in) also refuses unsigned or badly-signed hybrid keys.
	c := p.client(nil)
	c.Pins = nil
	p.deviceHybrid = forgedB64
	p.deviceHybridSig = ""
	tok, err := c.Redeem(ctx, "code", "verifier", c.Key)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Pin.IdentityKey != p.idKey() || tok.Pin.IdentityKeyHybridID != "" {
		t.Fatalf("Redeem persisted unsigned hybrid key: %+v", tok.Pin)
	}
	p.deviceHybridSig = p.bindHybrid(otherEd, forgedHK.Public())
	tok, err = c.Redeem(ctx, "code", "verifier", c.Key)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Pin.IdentityKeyHybridID != "" {
		t.Fatalf("Redeem persisted forged-sig hybrid key: %+v", tok.Pin)
	}

	// 5. Redeem accepts a validly signed hybrid key.
	realHK := testHybridKey(t, p.identity)
	p.deviceHybrid = base64.StdEncoding.EncodeToString(realHK.Public().Bytes())
	p.deviceHybridSig = p.bindHybrid(p.identity, realHK.Public())
	tok, err = c.Redeem(ctx, "code", "verifier", c.Key)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Pin.IdentityKeyHybridID != realHK.Public().ID() {
		t.Fatalf("Redeem hybrid ID = %q, want %q", tok.Pin.IdentityKeyHybridID, realHK.Public().ID())
	}
}
