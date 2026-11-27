package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/ky-primitives/hybridsig"
)

// TLS leaf pinning with signed rollover, KyDrive-server internal/api/tls_pin.go: a session
// trusts one leaf key; after a renewal it repins only to a leaf the server's identity key
// signed for this host. Sessions that learned a hybrid identity key (ML-DSA-65+Ed25519)
// require a kydrive-tls-pin/2 hybridsig signature and never downgrade to v1 Ed25519.

// Pin is a session's pinned leaf, the Ed25519 identity key (std base64, "" when unknown),
// the optional hybridsig public key ID ("mldsa65+ed25519:<64hex>", "" when unknown), and
// the floor a successor's statement must be newer than (Unix seconds).
type Pin struct {
	Leaf, IdentityKey, IdentityKeyHybridID string
	Floor                                  int64
}

// PinStore keeps a session's pin. Current errors once the session has ended (unlinked,
// revoked); Replace is compare-and-swap and writes only while old is still stored.
type PinStore interface {
	Current() (Pin, error)
	Replace(old, new Pin) bool
}

// MemoryPins is a PinStore for a credential no session holds yet.
type MemoryPins struct {
	mu  sync.Mutex
	pin Pin
}

func NewMemoryPins(p Pin) *MemoryPins { return &MemoryPins{pin: p} }

func (m *MemoryPins) Current() (Pin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pin, nil
}

func (m *MemoryPins) Replace(old, new Pin) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pin != old {
		return false
	}
	m.pin = new
	return true
}

// Pins enforces a PinStore for every Client sharing one base http.Client.
type Pins struct {
	Store PinStore
	Now   func() time.Time // nil: time.Now

	roll    sync.Mutex // one rollover fetch per renewal
	mu      sync.Mutex
	clients map[string]*http.Client // one per leaf, so a pooled connection never crosses pins
	asked   bool                    // identity key requested once
}

// ErrPinChanged: the server presented another leaf and no signed rollover vouched for it.
var ErrPinChanged = errors.New("KyDrive's certificate changed and the server did not vouch for the new one; sign in again")

var errPinMismatch = errors.New("TLS leaf does not match the pin")

const (
	maxSkew         = 24 * 60 * 60
	tlsPinContextV2 = "kydrive-tls-pin/2"
)

// SPKIPin is sha256/ plus std base64 of the SHA-256 of cert's SubjectPublicKeyInfo DER.
func SPKIPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256/" + base64.StdEncoding.EncodeToString(sum[:])
}

func pinStatement(host, pin string, issuedAt int64) []byte {
	return []byte("kydrive-tls-pin/1\n" + host + "\n" + pin + "\n" + strconv.FormatInt(issuedAt, 10))
}

func pinStatementBodyV2(host, pin string, issuedAt int64) []byte {
	return []byte(host + "\n" + pin + "\n" + strconv.FormatInt(issuedAt, 10))
}

// identityKey decodes a std-base64 raw Ed25519 public key, or nil.
func identityKey(s string) ed25519.PublicKey {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil
	}
	return b
}

// parseHybridIdentityKey decodes a std-base64 hybridsig public key. When wantEd is non-nil,
// the embedded Ed25519 tail must match wantEd so a server's hybrid key stays bound to its v1 key.
func parseHybridIdentityKey(s string, wantEd ed25519.PublicKey) (*hybridsig.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	pub, err := hybridsig.ParsePublicKey(b)
	if err != nil {
		return nil, err
	}
	if wantEd != nil && !bytes.Equal(b[len(b)-ed25519.PublicKeySize:], wantEd) {
		return nil, errors.New("hybrid identity key does not match Ed25519 identity key")
	}
	return pub, nil
}

// identityKeyHybridID returns the canonical hybridsig public key ID ("mldsa65+ed25519:<64hex>")
// for s, or "" if malformed or mismatched with wantEd.
func identityKeyHybridID(s string, wantEd ed25519.PublicKey) string {
	pub, err := parseHybridIdentityKey(s, wantEd)
	if err != nil {
		return ""
	}
	return pub.ID()
}

// ValidHybridKeyID reports whether id has the canonical "mldsa65+ed25519:<64 lowercase hex>" shape.
func ValidHybridKeyID(id string) bool {
	prefix := hybridsig.Alg + ":"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	h := id[len(prefix):]
	if len(h) != 64 || strings.ToLower(h) != h {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

type tlsPinStatement struct {
	Host              string `json:"host"`
	Pin               string `json:"pin"`
	IssuedAt          int64  `json:"issued_at"`
	Signature         string `json:"signature"`
	SignatureV2       string `json:"signature_v2"`
	IdentityKeyHybrid string `json:"identity_key_hybrid"`
}

// accept is the pin s installs in place of stored, or false. All must hold: it names the
// observed leaf, for host, within a day of now, newer than stored's floor, for a leaf not
// already pinned, signed by stored's identity key. When stored.IdentityKeyHybridID is set,
// a valid kydrive-tls-pin/2 hybridsig signature is required (never downgraded to v1 Ed25519).
func accept(stored Pin, host, observed string, s tlsPinStatement, now int64) (Pin, bool) {
	if s.Pin != observed || s.Host != host || observed == stored.Leaf ||
		s.IssuedAt <= stored.Floor || now-s.IssuedAt > maxSkew || s.IssuedAt-now > maxSkew {
		return Pin{}, false
	}
	if stored.IdentityKeyHybridID != "" {
		wantEd := identityKey(stored.IdentityKey)
		pub, err := parseHybridIdentityKey(s.IdentityKeyHybrid, wantEd)
		if err != nil {
			return Pin{}, false
		}
		if subtle.ConstantTimeCompare([]byte(pub.ID()), []byte(stored.IdentityKeyHybridID)) != 1 {
			return Pin{}, false
		}
		sigV2, err := base64.StdEncoding.DecodeString(s.SignatureV2)
		if err != nil {
			return Pin{}, false
		}
		if err := pub.Verify(tlsPinContextV2, pinStatementBodyV2(host, s.Pin, s.IssuedAt), sigV2); err != nil {
			return Pin{}, false
		}
		return Pin{
			Leaf:                observed,
			IdentityKey:         stored.IdentityKey,
			IdentityKeyHybridID: stored.IdentityKeyHybridID,
			Floor:               s.IssuedAt,
		}, true
	}
	key := identityKey(stored.IdentityKey)
	sig, err := base64.StdEncoding.DecodeString(s.Signature)
	if key == nil || err != nil || !ed25519.Verify(key, pinStatement(host, s.Pin, s.IssuedAt), sig) {
		return Pin{}, false
	}
	return Pin{Leaf: observed, IdentityKey: stored.IdentityKey, Floor: s.IssuedAt}, true
}

func (p *Pins) now() int64 {
	if p.Now != nil {
		return p.Now().Unix()
	}
	return time.Now().Unix()
}

// leafPin is the pin of the leaf res arrived over, or "" without TLS.
func leafPin(res *http.Response) string {
	if res.TLS == nil || len(res.TLS.PeerCertificates) == 0 {
		return ""
	}
	return SPKIPin(res.TLS.PeerCertificates[0])
}

func pinHost(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// sniName is name as crypto/tls sends it in SNI, lowercased: no trailing dot, "" for an IP.
func sniName(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if net.ParseIP(name) != nil {
		return ""
	}
	return name
}

// client is base restricted to leaf. VerifyConnection runs after chain and hostname checks
// and before any request byte is written.
func (p *Pins) client(base *http.Client, host, leaf string) (*http.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.clients[leaf]; c != nil {
		return c, nil
	}
	tr, ok := base.Transport.(*http.Transport)
	if base.Transport == nil {
		tr, ok = http.DefaultTransport.(*http.Transport)
	}
	if !ok {
		return nil, errors.New("this HTTP client cannot pin TLS")
	}
	tr = tr.Clone()
	// Hybrid ML-DSA-65+Ed25519 request signatures are ~4.5 KiB base64 in KyDrive-Signature,
	// exceeding net/http's 4 KiB default write buffer. Keeping the full request header block
	// in a single write preserves net/http's nothingWrittenError/read retry when a server
	// closes an idle keep-alive connection (e.g. during TLS certificate rollover on Windows).
	if tr.WriteBufferSize < 16*1024 {
		tr.WriteBufferSize = 16 * 1024
	}
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{}
	}
	// The transport also handshakes with an HTTPS proxy, under the same config. Only a name
	// the proxy function chose skips the leaf check; the tunnelled handshake is still checked.
	var proxies sync.Map
	if proxy := tr.Proxy; proxy != nil {
		tr.Proxy = func(req *http.Request) (*url.URL, error) {
			u, err := proxy(req)
			if u != nil && u.Scheme == "https" {
				if name := sniName(u.Hostname()); name != "" && name != sniName(host) {
					proxies.Store(name, true)
				}
			}
			return u, err
		}
	}
	prev := tr.TLSClientConfig.VerifyConnection
	tr.TLSClientConfig.VerifyConnection = func(cs tls.ConnectionState) error {
		if prev != nil {
			if err := prev(cs); err != nil {
				return err
			}
		}
		if _, proxy := proxies.Load(sniName(cs.ServerName)); proxy {
			return nil
		}
		if len(cs.PeerCertificates) == 0 || SPKIPin(cs.PeerCertificates[0]) != leaf {
			return errPinMismatch
		}
		return nil
	}
	c := *base
	c.Transport = tr
	if p.clients == nil {
		p.clients = map[string]*http.Client{}
	}
	p.clients[leaf] = &c
	return &c, nil
}

func (p *Pins) drop(leaf string) {
	p.mu.Lock()
	c := p.clients[leaf]
	delete(p.clients, leaf)
	p.mu.Unlock()
	if c != nil {
		c.CloseIdleConnections()
	}
}

// wantIdentity is true once for a session pinned without its identity key(s).
func (p *Pins) wantIdentity() bool {
	pin, err := p.Store.Current()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asked || err != nil || pin.Leaf == "" || (pin.IdentityKey != "" && pin.IdentityKeyHybridID != "") {
		return false
	}
	p.asked = true
	return true
}

// learn stores key and hybridKey as the session's identity keys when res came over the pinned
// leaf and they are not yet stored. Write-once: whoever holds a leaf's private key (a TLS proxy)
// must not replace an existing identity key. When an Ed25519 key is already held, a hybrid key
// is accepted only if its embedded Ed25519 half matches the held key.
func (p *Pins) learn(res *http.Response, key, hybridKey string) {
	stored, err := p.Store.Current()
	if err != nil || stored.Leaf == "" || stored.Leaf != leafPin(res) {
		return
	}
	next := stored
	if next.IdentityKey == "" && identityKey(key) != nil {
		next.IdentityKey = key
	}
	if next.IdentityKeyHybridID == "" && hybridKey != "" {
		if id := identityKeyHybridID(hybridKey, identityKey(next.IdentityKey)); id != "" {
			next.IdentityKeyHybridID = id
		}
	}
	if next == stored {
		return
	}
	p.Store.Replace(stored, next)
}

// pinned sends through the session's pin. With nothing pinned yet the leaf of the first 2xx
// answer is pinned (trust on first use). A wrong leaf costs one signed rollover and one retry.
func (c *Client) pinned(ctx context.Context, send func(*http.Client) (*http.Response, error)) (*http.Response, error) {
	p := c.Pins
	if p == nil {
		return send(c.HTTP)
	}
	pin, err := p.Store.Current()
	if err != nil {
		return nil, err
	}
	host := pinHost(c.Base)
	if pin.Leaf == "" {
		res, err := send(c.HTTP)
		if err == nil && res.StatusCode/100 == 2 {
			if leaf := leafPin(res); leaf != "" {
				p.Store.Replace(Pin{}, Pin{Leaf: leaf, Floor: p.now()})
			}
		}
		return res, err
	}
	hc, err := p.client(c.HTTP, host, pin.Leaf)
	if err != nil {
		return nil, err
	}
	res, err := send(hc)
	if !errors.Is(err, errPinMismatch) {
		return res, err
	}
	moved, rerr := c.roll(ctx, pin)
	if !moved {
		return nil, errors.Join(ErrPinChanged, err, rerr)
	}
	if pin, err = p.Store.Current(); err != nil {
		return nil, err
	}
	if hc, err = p.client(c.HTTP, host, pin.Leaf); err != nil {
		return nil, err
	}
	// The one retry: a server flapping between leaves fails here instead of rolling again.
	if res, err = send(hc); errors.Is(err, errPinMismatch) {
		return nil, errors.Join(ErrPinChanged, err)
	}
	return res, err
}

// roll, after failed met another leaf, fetches the signed pin once, credential-free over
// system trust. True when the stored pin moved, here or by a concurrent request.
func (c *Client) roll(ctx context.Context, failed Pin) (bool, error) {
	p := c.Pins
	p.roll.Lock()
	defer p.roll.Unlock()
	stored, err := p.Store.Current()
	if err != nil {
		return false, err
	}
	if stored.Leaf != failed.Leaf {
		return stored.Leaf != "", nil
	}
	if stored.IdentityKey == "" && stored.IdentityKeyHybridID == "" {
		return false, nil
	}
	endpoint := strings.TrimRight(c.Base, "/") + "/api/devices/tls-pin"
	if stored.IdentityKeyHybridID != "" {
		endpoint += "?v=2"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	observed := leafPin(res)
	if res.StatusCode != http.StatusOK || observed == "" {
		return false, nil
	}
	var s tlsPinStatement
	if err := json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&s); err != nil {
		return false, nil
	}
	next, ok := accept(stored, pinHost(c.Base), observed, s, p.now())
	if !ok || !p.Store.Replace(stored, next) { // an unlink during the fetch is never undone
		return false, nil
	}
	p.drop(stored.Leaf)
	return true, nil
}

// Reach makes one credential-free request through the session's pin: it fails unless the
// server holds the pinned leaf or a successor the identity key signed.
func (c *Client) Reach(ctx context.Context) error {
	res, err := c.pinned(ctx, func(hc *http.Client) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.Base, "/")+"/api/devices/tls-pin", nil)
		if err != nil {
			return nil, err
		}
		return hc.Do(req)
	})
	if err != nil {
		return err
	}
	return res.Body.Close()
}
