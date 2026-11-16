package api

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Device request signing, kydrive-server internal/api/devicesig.go: a device that enrolled a
// P-256 key at link signs every request, so its token alone authorises nothing.

// signedHeaders are the headers the server signs over, lowercase, in signing order.
var signedHeaders = []string{"content-range", "depth", "destination", "if", "if-match", "if-none-match", "lock-token", "overwrite", "timeout", "x-oc-mtime"}

// sign sets req's signing headers; body is exactly the bytes sent.
func sign(req *http.Request, key *ecdsa.PrivateKey, origin string, body []byte) error {
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
	sum := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
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
