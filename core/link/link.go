// Package link pairs this computer with a KyDrive account: a loopback redirect plus PKCE
// (RFC 8252), approved by the user in their browser.
package link

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
)

var timeout = 10 * time.Minute

type Request struct {
	Server   string // KyDrive origin
	Name     string // device name shown on the approval page
	Platform string
	Open     func(url string) error // opens the system browser
	HTTP     *http.Client
}

func random() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Link opens the approval page and waits up to ten minutes for the browser to come back to
// the loopback listener with a code, which it exchanges for a device token.
func Link(ctx context.Context, r Request) (api.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return api.Token{}, err
	}
	defer ln.Close()
	verifier, state := random(), random()
	sum := sha256.Sum256([]byte(verifier))
	redirect := fmt.Sprintf("http://%s/cb", ln.Addr())
	codes := make(chan string, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got := req.URL.Query().Get("state")
		if req.URL.Path != "/cb" || subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 || req.URL.Query().Get("code") == "" {
			http.Error(w, "This is not the link request this computer started.", 400)
			return
		}
		select {
		case codes <- req.URL.Query().Get("code"):
			io.WriteString(w, "KyDrive is linked. You can close this window.")
		default:
			http.Error(w, "This link request was already answered.", 400)
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	q := url.Values{"redirect": {redirect}, "challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "state": {state}, "name": {r.Name}, "platform": {r.Platform}}
	if err := r.Open(strings.TrimRight(r.Server, "/") + "/link?" + q.Encode()); err != nil {
		return api.Token{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case code := <-codes:
		c := &api.Client{Base: r.Server, HTTP: r.HTTP}
		return c.Redeem(ctx, code, verifier)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return api.Token{}, errors.New("linking timed out; start it again")
		}
		return api.Token{}, ctx.Err()
	}
}
