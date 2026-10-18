package link

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// server plays KyDrive's token endpoint, checking the PKCE verifier against the challenge the
// browser carried.
func server(t *testing.T, challenge *string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte(r.FormValue("verifier")))
		if r.URL.Path != "/api/devices/token" || r.FormValue("code") != "code1" || base64.RawURLEncoding.EncodeToString(sum[:]) != *challenge {
			http.Error(w, `{"error":"bad"}`, 400)
			return
		}
		io.WriteString(w, `{"token":"kyd_tok","devices_url":"x"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLink(t *testing.T) {
	var challenge string
	srv := server(t, &challenge)
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
	if err != nil || tok.Token != "kyd_tok" {
		t.Fatalf("got %+v, %v", tok, err)
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
