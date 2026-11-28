package engine

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/vault"
	"github.com/zalando/go-keyring"
)

func startVaultWitness(t *testing.T, bin string) (vault.Config, *http.Client, []string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "witness-state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	output := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("witness setup failed: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	publisher := filepath.Join(dir, "publisher")
	witness := filepath.Join(dir, "witness")
	publisherKey := output("-generate-key", "-key-file", publisher)
	witnessKey := output("-generate-key", "-key-file", witness)
	args := []string{"-state-dir", dir, "-key-file", witness, "-publisher-key", publisherKey}
	output(append(args, "-initialize")...)

	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	cert := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	cmd := exec.CommandContext(t.Context(), bin, append(args, "-listen", addr, "-tls-cert", certFile, "-tls-key", keyFile)...)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			t.Log(log.String())
		}
	})
	base := "https://" + addr
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	t.Cleanup(hc.CloseIdleConnections)
	for tries := 0; ; tries++ {
		resp, err := hc.Get(base + "/")
		if err == nil {
			resp.Body.Close()
			break
		}
		if tries == 60 {
			t.Fatalf("witness did not start: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return vault.Config{WitnessOrigin: base, WitnessKey: witnessKey}, hc, []string{publisher, witnessKey}
}

func TestRealServerNativeVault(t *testing.T) {
	serverBin, witnessBin := os.Getenv("KYDRIVE_SERVER_BIN"), os.Getenv("KYDRIVE_VAULT_WITNESS_BIN")
	if serverBin == "" || witnessBin == "" {
		t.Skip("needs the KyDrive server and witness binaries")
	}
	keyring.MockInit()
	config, httpClient, settings := startVaultWitness(t, witnessBin)
	t.Setenv("KYDRIVE_VAULT_PUBLISHER_KEY_FILE", settings[0])
	t.Setenv("KYDRIVE_VAULT_WITNESS_KEY", settings[1])
	r := startServer(t, serverBin)
	ctx := t.Context()
	device := api.Client{Base: r.base, Token: r.token, Key: r.key, HTTP: httpClient}
	account, err := device.DeviceAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wrongAccount := account
	wrongAccount.UserID = "usr_another_account"
	if _, err := vault.Connect(ctx, device, wrongAccount, config); err == nil {
		t.Fatal("native vault opened the wrong signed device account")
	}
	owner, err := vault.Connect(ctx, device, account, config)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := owner.CreateVault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const file = "f1266921-b8f2-43ee-a476-0721df575e7a"
	source := []byte("synthetic initial office bytes")
	if err := owner.SaveRevision(ctx, file, 1, source); err != nil {
		t.Fatal(err)
	}
	wire, err := device.VaultRevision(ctx, invite.Vault, file, 1)
	if err != nil || bytes.Contains(wire.Body, source) || !bytes.HasPrefix(wire.Body, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("source was not stored as age ciphertext: %v", err)
	}
	capability := vault.EditorCapability{
		Hash: "/2/integration/edit/" + strings.Repeat("a", 24) + "/", Format: "xlsx",
		Room:  strings.Repeat("0", 32),
		Rooms: []string{strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)},
		Blobs: []string{strings.Repeat("4", 48), strings.Repeat("5", 48), strings.Repeat("6", 48)},
	}
	editor, err := owner.OpenEditor(ctx, file, 1, &capability)
	if err != nil || editor.Capability.Format != "xlsx" || !bytes.Equal(editor.Source, source) || editor.Grant.Token == "" {
		t.Fatalf("editor initialization failed: %v", err)
	}
	owner.Close()
	second := r.linkComputer(t, httpClient, "second laptop")
	otherDevice := api.Client{Base: r.base, Token: second.Token, Key: second.Key, HTTP: httpClient}
	reopened, err := vault.Connect(ctx, otherDevice, account, config)
	if err != nil || reopened.Member() != owner.Member() {
		t.Fatalf("native keyring identity did not survive reopen: %v", err)
	}
	wrong := invite
	wrong.Owner.Fingerprint = strings.Repeat("0", 64)
	if _, err := reopened.OpenVault(ctx, wrong); err == nil {
		t.Fatal("forged invitation was accepted")
	}
	if _, err := reopened.OpenVault(ctx, invite); err != nil {
		t.Fatal(err)
	}
	editor, err = reopened.OpenEditor(ctx, file, 1, nil)
	if err != nil || editor.Capability.Format != "xlsx" || editor.Source != nil {
		t.Fatalf("signed editor binding did not reopen: %v", err)
	}
	if _, err := reopened.RefreshEditor(ctx, editor); err != nil {
		t.Fatal(err)
	}
	edited := []byte("synthetic office export with a new edit")
	if n, err := reopened.SaveEditor(ctx, editor, edited); err != nil || n != 2 {
		t.Fatalf("signed encrypted editor export: revision %d, %v", n, err)
	}
	if actual, err := reopened.ReadRevision(ctx, file, 2); err != nil || !bytes.Equal(actual, edited) {
		t.Fatalf("reopen verified editor export: %v", err)
	}
	if _, err := owner.OpenVault(ctx, invite); err != nil {
		t.Fatal(err)
	}
	if err := otherDevice.RevokeSelf(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.RefreshEditor(ctx, editor); err == nil {
		t.Fatal("editor renewed after device revocation")
	}
	if _, err := reopened.SaveEditor(ctx, editor, edited); err == nil {
		t.Fatal("editor saved after device revocation")
	}
	if actual, err := owner.ReadRevision(ctx, file, 2); err != nil || !bytes.Equal(actual, edited) {
		t.Fatalf("the original device lost access after the other device was revoked: %v", err)
	}

}
