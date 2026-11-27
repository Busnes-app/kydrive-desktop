package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/hybridsig"
)

// Device request signing, kydrive-server internal/api/devicesig.go: a device that enrolled a
// signing key at link signs every request, so its token alone authorises nothing. Newly linked
// desktop installs enroll an ML-DSA-65+Ed25519 hybrid key (ky-primitives/hybridsig); existing
// P-256 installs keep working without relinking.

const (
	deviceSigLabel        = "kydrive-request-v1"
	keyringHybridPrefix   = hybridsig.Alg + ":"
	hybridKeyringSeedSize = 64 // 32-byte ML-DSA-65 seed || 32-byte Ed25519 seed
)

// SigningKey holds a device's enrolled private signing key: either an ML-DSA-65+Ed25519 hybrid
// key or a legacy ECDSA P-256 key. Exactly one field is non-nil.
type SigningKey struct {
	Hybrid *hybridsig.PrivateKey
	P256   *ecdsa.PrivateKey
}

// GenerateSigningKey generates a fresh ML-DSA-65+Ed25519 hybrid device signing key.
func GenerateSigningKey() (*SigningKey, error) {
	k, err := hybridsig.Generate()
	if err != nil {
		return nil, err
	}
	return &SigningKey{Hybrid: k}, nil
}

// GenerateP256SigningKey generates a P-256 device signing key for fallback against servers
// that have not yet been upgraded to accept ML-DSA-65+Ed25519 device keys.
func GenerateP256SigningKey() (*SigningKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &SigningKey{P256: k}, nil
}

// SigningKeyFromP256 wraps a legacy P-256 private key.
func SigningKeyFromP256(k *ecdsa.PrivateKey) *SigningKey {
	if k == nil {
		return nil
	}
	return &SigningKey{P256: k}
}

// SigningKeyFromHybrid wraps an ML-DSA-65+Ed25519 hybrid private key.
func SigningKeyFromHybrid(k *hybridsig.PrivateKey) *SigningKey {
	if k == nil {
		return nil
	}
	return &SigningKey{Hybrid: k}
}

// PublicKeyBytes returns the wire-encoded public key sent as publicKey during device link:
// self-describing hybridsig bytes (2000 bytes) for hybrid keys, or SPKI DER for P-256 keys.
func (k *SigningKey) PublicKeyBytes() ([]byte, error) {
	if k == nil {
		return nil, errors.New("nil signing key")
	}
	switch {
	case k.Hybrid != nil:
		return k.Hybrid.Public().Bytes(), nil
	case k.P256 != nil:
		return x509.MarshalPKIXPublicKey(&k.P256.PublicKey)
	default:
		return nil, errors.New("empty signing key")
	}
}

// Sign signs msg for device request authentication.
func (k *SigningKey) Sign(msg []byte) ([]byte, error) {
	if k == nil {
		return nil, errors.New("nil signing key")
	}
	switch {
	case k.Hybrid != nil:
		return k.Hybrid.Sign(deviceSigLabel, msg)
	case k.P256 != nil:
		sum := sha256.Sum256(msg)
		return ecdsa.SignASN1(rand.Reader, k.P256, sum[:])
	default:
		return nil, errors.New("empty signing key")
	}
}

// Equal reports whether k and other represent the same private key.
func (k *SigningKey) Equal(other *SigningKey) bool {
	if k == nil || other == nil {
		return k == other
	}
	switch {
	case k.Hybrid != nil && other.Hybrid != nil:
		return bytes.Equal(k.Hybrid.Public().Bytes(), other.Hybrid.Public().Bytes())
	case k.P256 != nil && other.P256 != nil:
		return k.P256.Equal(other.P256)
	default:
		return false
	}
}

// MarshalKeyring encodes k as a single space-free token suitable for the OS keyring item.
// Legacy P-256 keys keep their std base64 PKCS#8 DER format; hybrid keys use
// "mldsa65+ed25519:<std base64 of 32B ML-DSA seed || 32B Ed25519 seed>".
func (k *SigningKey) MarshalKeyring() (string, error) {
	if k == nil {
		return "", errors.New("nil signing key")
	}
	switch {
	case k.Hybrid != nil:
		mldsaSeed, edSeed := k.Hybrid.Seeds()
		buf := make([]byte, hybridKeyringSeedSize)
		copy(buf[:32], mldsaSeed)
		copy(buf[32:], edSeed)
		return keyringHybridPrefix + base64.StdEncoding.EncodeToString(buf), nil
	case k.P256 != nil:
		der, err := x509.MarshalPKCS8PrivateKey(k.P256)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(der), nil
	default:
		return "", errors.New("empty signing key")
	}
}

// ParseKeyringSigningKey parses either a hybrid seed keyring entry or a legacy P-256 PKCS#8 DER entry.
func ParseKeyringSigningKey(enc string) (*SigningKey, error) {
	if strings.HasPrefix(enc, keyringHybridPrefix) {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, keyringHybridPrefix))
		if err != nil || len(raw) != hybridKeyringSeedSize {
			return nil, errors.New("invalid hybrid signing key")
		}
		h, err := hybridsig.FromSeeds(raw[:32], raw[32:])
		if err != nil {
			return nil, err
		}
		return &SigningKey{Hybrid: h}, nil
	}
	der, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	p256, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || p256.Curve != elliptic.P256() {
		return nil, errors.New("invalid P-256 signing key")
	}
	return &SigningKey{P256: p256}, nil
}

// signedHeaders are the headers the server signs over, lowercase, in signing order.
var signedHeaders = []string{"content-range", "depth", "destination", "if", "if-match", "if-none-match", "lock-token", "overwrite", "timeout", "x-oc-mtime"}

// sign sets req's signing headers; body is exactly the bytes sent.
func sign(req *http.Request, key *SigningKey, origin string, body []byte) error {
	nonce := make([]byte, 16)
	rand.Read(nonce)
	date := strconv.FormatInt(time.Now().UnixMilli(), 10)
	n := base64.RawURLEncoding.EncodeToString(nonce)
	digest := sha256.Sum256(body)
	req.Header.Set("KyDrive-Date", date)
	req.Header.Set("KyDrive-Nonce", n)
	req.Header.Set("KyDrive-Content-SHA256", hex.EncodeToString(digest[:]))
	msg, err := message(origin, req.Method, req.URL.RequestURI(), date, n, req.Header, hex.EncodeToString(digest[:]))
	if err != nil {
		return err
	}
	sig, err := key.Sign([]byte(msg))
	if err != nil {
		return err
	}
	req.Header.Set("KyDrive-Signature", base64.StdEncoding.EncodeToString(sig))
	return nil
}

// message is the signed string. A field holding "|", CR or LF, or a signed header sent more
// than once, would make it ambiguous and is refused.
func message(origin, method, uri, date, nonce string, h http.Header, digest string) (string, error) {
	hs := sha256.New()
	for _, name := range signedHeaders {
		switch v := h.Values(name); len(v) {
		case 0:
		case 1:
			hs.Write([]byte(name + ":" + v[0] + "\n"))
		default:
			return "", errors.New("signed header " + name + " sent more than once")
		}
	}
	fields := []string{"kydrive-request-v1", origin, method, uri, date, nonce, hex.EncodeToString(hs.Sum(nil)), digest}
	for _, f := range fields {
		if f == "" || strings.ContainsAny(f, "|\r\n") {
			return "", errors.New("request cannot be signed: a field is empty or holds |, CR or LF")
		}
	}
	return strings.Join(fields, "|"), nil
}

// canonicalOrigin is scheme://host[:port], lowercased, without a default port.
func canonicalOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}
