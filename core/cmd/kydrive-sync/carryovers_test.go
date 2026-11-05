package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestCarryoverControlsInterruptRunningPass(t *testing.T) {
	for _, method := range []string{"Choose", "ConfirmDeletes", "Unlink", "Pause"} {
		t.Run(method, func(t *testing.T) {
			inner := server(t)
			target, _ := url.Parse(inner.URL)
			proxy := httputil.NewSingleHostReverseProxy(target)
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/snapshot") {
					select {
					case entered <- struct{}{}:
					default:
					}
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				proxy.ServeHTTP(w, r)
			}))
			defer srv.Close()
			defer once.Do(func() { close(release) })
			d, ctl := start(t, srv)
			var out struct{}
			if err := ctl("Link", map[string]string{"Server": srv.URL}, &out); err != nil {
				t.Fatal(err)
			}
			if err := ctl("Choose", map[string][]string{"IDs": {"w1"}}, &out); err != nil {
				t.Fatal(err)
			}
			passDone := make(chan struct{})
			go func() { defer close(passDone); d.pass(context.Background(), true); d.pass(context.Background(), false) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("pass did not enter snapshot")
			}
			params := map[string]any{}
			switch method {
			case "Choose":
				params["IDs"] = []string{}
			case "ConfirmDeletes":
				params["Workspace"] = "w1"
				params["Allow"] = false
			case "Pause":
				params["Paused"] = true
			}
			done := make(chan error, 1)
			go func() { var out struct{}; done <- ctl(method, params, &out) }()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(2 * time.Second):
				t.Error("control waited behind running pass")
			}
			once.Do(func() { close(release) })
			<-passDone
			var status Status
			if err := ctl("Status", struct{}{}, &status); err != nil || status.State == "error" {
				t.Fatalf("status %+v %v", status, err)
			}
		})
	}
}

type exhaustedListener struct {
	net.Listener
	mu     sync.Mutex
	failed bool
}

func (l *exhaustedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.failed {
		l.failed = true
		l.mu.Unlock()
		return nil, &net.OpError{Op: "accept", Net: "unix", Err: syscall.EMFILE}
	}
	l.mu.Unlock()
	return l.Listener.Accept()
}

func TestCarryoverAcceptRecoversDescriptorExhaustion(t *testing.T) {
	srv := server(t)
	d, _ := start(t, srv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.serve(ctx, &exhaustedListener{Listener: ln})
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	conn.Write([]byte("{\"method\":\"Core.Status\",\"params\":[{}],\"id\":1}\n"))
	b := make([]byte, 4096)
	if n, err := conn.Read(b); err != nil || !strings.Contains(string(b[:n]), "Version") {
		t.Fatalf("IPC did not recover: %q %v", b[:n], err)
	}
}

func TestCarryoverMacWatcherKeepsDescriptorsAvailable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS descriptor proof")
	}
	root := t.TempDir()
	for i := 0; i < 512; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprint(i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon{root: root, kick: make(chan bool, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); d.watch(ctx) }()
	select {
	case <-done:
	case <-d.kick:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("watch initialization stalled")
	}
	after, err := os.ReadDir("/dev/fd")
	cancel()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before)+32 {
		t.Fatalf("watcher pinned %d descriptors for 512 files", len(after)-len(before))
	}
}
