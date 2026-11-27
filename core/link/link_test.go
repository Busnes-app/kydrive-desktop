package link

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/hybridsig"
)

// server plays KyDrive's token endpoint, checking the PKCE verifier against the challenge the
// browser carried, and records the device key sent with it.
func server(t *testing.T, challenge, publicKey *string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte(r.FormValue("verifier")))
		if r.URL.Path != "/api/devices/token" || r.FormValue("code") != "code1" || base64.RawURLEncoding.EncodeToString(sum[:]) != *challenge {
			http.Error(w, `{"error":"bad"}`, 400)
			return
		}
		*publicKey = r.FormValue("publicKey")
		io.WriteString(w, `{"token":"kyd_tok","devices_url":"x"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLink(t *testing.T) {
	var challenge, publicKey string
	srv := server(t, &challenge, &publicKey)
	open := func(raw string) error {
		u, err := url.Parse(raw)
		q := u.Query()
		redirect, _ := url.Parse(q.Get("redirect"))
		if err != nil || u.Path != "/link" || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" ||
			q.Get("name") != "laptop" || q.Get("platform") != "linux" || len(q.Get("state")) < 20 {
			t.Errorf("bad link URL %s", raw)
		}
		challenge = q.Get("challenge")
		go func() {
			// A callback with the wrong state is refused and does not end the wait.
			res, err := http.Get(q.Get("redirect") + "?code=code1&state=forged")
			if err != nil || res.StatusCode != 400 {
				t.Errorf("forged state: %v %v", res, err)
			}
			res, err = http.Get(q.Get("redirect") + "?code=code1&state=" + url.QueryEscape(q.Get("state")))
			if err != nil || res.StatusCode != 200 {
				t.Errorf("callback: %v %v", res, err)
			}
		}()
		return nil
	}
	tok, err := Link(context.Background(), Request{Server: srv.URL, Name: "laptop", Platform: "linux", Open: open, HTTP: srv.Client()})
	if err != nil || tok.Token != "kyd_tok" || tok.Key == nil || tok.Key.Hybrid == nil {
		t.Fatalf("got %+v, %v (want hybrid key)", tok, err)
	}
	pub, err := tok.Key.PublicKeyBytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != hybridsig.PublicKeyBytes || publicKey != base64.StdEncoding.EncodeToString(pub) {
		t.Fatalf("redeemed with key len=%d, not the hybrid token key", len(pub))
	}
}

func TestLinkFallsBackToP256OnLegacyServer(t *testing.T) {
	var challenge, publicKey string
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		sum := sha256.Sum256([]byte(r.FormValue("verifier")))
		if r.URL.Path != "/api/devices/token" || r.FormValue("code") != "code1" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			http.Error(w, `{"error":"bad"}`, 400)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(r.FormValue("publicKey"))
		if err != nil {
			http.Error(w, `{"error":"Invalid device key"}`, 400)
			return
		}
		// Simulate a pre-Wave-2 server that only accepts P-256 SPKI DER and leaves the code unspent.
		parsed, err := x509.ParsePKIXPublicKey(raw)
		if p256, ok := parsed.(*ecdsa.PublicKey); err != nil || !ok || p256.Curve != elliptic.P256() {
			http.Error(w, `{"error":"Invalid device key"}`, 400)
			return
		}
		publicKey = r.FormValue("publicKey")
		io.WriteString(w, `{"token":"kyd_legacy","devices_url":"x"}`)
	}))
	t.Cleanup(srv.Close)

	open := func(raw string) error {
		u, _ := url.Parse(raw)
		q := u.Query()
		challenge = q.Get("challenge")
		go func() {
			http.Get(q.Get("redirect") + "?code=code1&state=" + url.QueryEscape(q.Get("state")))
		}()
		return nil
	}
	tok, err := Link(context.Background(), Request{Server: srv.URL, Name: "laptop", Platform: "linux", Open: open, HTTP: srv.Client()})
	if err != nil || tok.Token != "kyd_legacy" || tok.Key == nil || tok.Key.P256 == nil || attempts != 2 {
		t.Fatalf("got %+v, err=%v, attempts=%d (want P-256 fallback on attempt 2)", tok, err, attempts)
	}
	pub, _ := tok.Key.PublicKeyBytes()
	if publicKey != base64.StdEncoding.EncodeToString(pub) {
		t.Fatalf("redeemed P-256 key mismatch")
	}
}

func TestLinkTimesOut(t *testing.T) {
	defer func(d time.Duration) { timeout = d }(timeout)
	timeout = 50 * time.Millisecond
	_, err := Link(context.Background(), Request{Server: "http://127.0.0.1:1", Name: "n", Platform: "linux", Open: func(string) error { return nil }, HTTP: http.DefaultClient})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v", err)
	}
}

func TestLinkDefaultTimeoutIsTenMinutes(t *testing.T) {
	if timeout != 10*time.Minute {
		t.Fatalf("timeout is %v", timeout)
	}
}
