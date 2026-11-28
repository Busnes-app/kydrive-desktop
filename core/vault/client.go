// Package vault runs the witnessed vault protocol inside the desktop process.
package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/ky-primitives/hybridsig"
	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-server/pkg/vaultclient"
	"github.com/zalando/go-keyring"
)

const keyringService = "KyDrive Vault"

// Config comes from native setup. Establish the witness key independently of KyDrive.
type Config struct {
	WitnessOrigin string
	WitnessKey    string // standard base64 hybridsig public key
}

// Client is bound to an enrolled native device and one open vault at a time.
type Client struct {
	mu         sync.Mutex
	drive      api.Client
	witness    http.Client
	config     Config
	identity   vaultclient.MemberPin
	session    *vaultclient.Session
	invite     vaultclient.Invitation
	checkpoint vaultclient.Checkpoint
}

// Connect opens/creates the local account identity in the OS keyring. It fails closed if
// the server already knows an account key that this device does not hold.
func Connect(ctx context.Context, drive api.Client, linked api.Account, config Config) (*Client, error) {
	driveOrigin, err := origin(drive.Base)
	if err != nil || drive.Key == nil || drive.Token == "" || linked.UserID == "" || drive.HTTP == nil ||
		(strings.HasPrefix(driveOrigin, "https://") && (drive.Pins == nil || drive.Pins.Store == nil)) {
		return nil, errors.New("a signed, pinned KyDrive device is required for vault access")
	}
	witness, err := origin(config.WitnessOrigin)
	if err != nil || witness == driveOrigin {
		return nil, errors.New("choose an independent vault witness")
	}
	key, err := base64.StdEncoding.DecodeString(config.WitnessKey)
	if err != nil {
		return nil, errors.New("invalid pinned witness key")
	}
	if _, err := hybridsig.ParsePublicKey(key); err != nil {
		return nil, errors.New("invalid pinned witness key")
	}
	nativeHTTP := *drive.HTTP
	nativeHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	nativeHTTP.Jar = nil
	drive.Base, drive.HTTP = driveOrigin, &nativeHTTP
	scope, err := drive.VaultScope(ctx)
	if err != nil {
		return nil, err
	}
	if scope.User != linked.UserID || (linked.OrgID != "" && linked.OrgID != scope.Org) || !vaultclient.ValidID(scope.Org) {
		return nil, errors.New("the vault scope did not match the linked account")
	}
	identity, err := loadIdentity(ctx, &drive, driveOrigin, scope)
	if err != nil {
		return nil, err
	}
	session, err := vaultclient.NewSession(scope.User, scope.Org, identity)
	if err != nil {
		return nil, err
	}
	config.WitnessOrigin = witness
	witnessHTTP := http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	witnessHTTP.Transport = drive.HTTP.Transport
	return &Client{
		drive: drive, config: config, witness: witnessHTTP, session: session,
		identity: vaultclient.MemberPin{User: scope.User, Fingerprint: identity.PublicKey().Fingerprint()},
	}, nil
}

func origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return "", errors.New("expected an HTTPS origin")
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && (u.Scheme != "http" || (host != "localhost" && (ip == nil || !ip.IsLoopback()))) {
		return "", errors.New("expected an HTTPS origin")
	}
	if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}

type identitySecret struct {
	Recipient string
	ML, Ed    []byte
}

// Only one in-process Connect can enroll or write a missing account key.
var enrollMu sync.Mutex

func loadIdentity(ctx context.Context, drive *api.Client, origin string, scope api.VaultScope) (*vaultclient.Identity, error) {
	enrollMu.Lock()
	defer enrollMu.Unlock()
	account, _ := json.Marshal([]string{origin, scope.Org, scope.User})
	digest := sha256.Sum256(account)
	slot := hex.EncodeToString(digest[:])
	stored, err := keyring.Get(keyringService, slot)
	var key *vaultclient.Identity
	switch {
	case err == nil:
		var secret identitySecret
		if json.Unmarshal([]byte(stored), &secret) != nil {
			return nil, errors.New("invalid vault identity in the OS keyring")
		}
		key, err = vaultclient.RestoreIdentity(secret.Recipient, secret.ML, secret.Ed)
		if err != nil {
			return nil, err
		}
	case errors.Is(err, keyring.ErrNotFound):
		if _, err := drive.VaultKey(ctx, scope.User); api.Status(err) != http.StatusNotFound {
			if err == nil {
				return nil, errors.New("this account already has a vault key; recover it before opening a vault")
			}
			return nil, err
		}
		key, err = vaultclient.GenerateIdentity()
		if err != nil {
			return nil, err
		}
		recipient, ml, ed := key.Secrets()
		encoded, _ := json.Marshal(identitySecret{recipient, ml, ed})
		if err := keyring.Set(keyringService, slot, string(encoded)); err != nil {
			return nil, errors.New("could not store the vault identity in the OS keyring")
		}
	default:
		return nil, errors.New("could not read the vault identity from the OS keyring")
	}
	// Store first. A failed enrollment can retry the same key on this device.
	if err := drive.EnrollVaultKey(ctx, key.PublicKey()); err != nil {
		return nil, err
	}
	registered, err := drive.VaultKey(ctx, scope.User)
	if err != nil {
		return nil, err
	}
	if registered.Fingerprint() != key.PublicKey().Fingerprint() {
		return nil, errors.New("the registered vault key did not match this device")
	}
	return key, nil
}

func (c *Client) Member() vaultclient.MemberPin { return c.identity }

func (c *Client) clearVault() {
	c.session.Close()
	c.invite = vaultclient.Invitation{}
	c.checkpoint = vaultclient.Checkpoint{}
}

// Close clears the open vault and prevents future editor or revision operations until opened again.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearVault()
}

// CreateVault returns the invitation even if a later witness step fails, so the
// caller can reopen the same vault after an interrupted publish.
func (c *Client) CreateVault(ctx context.Context) (vaultclient.Invitation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, err := c.session.CreateVault()
	if err != nil {
		return vaultclient.Invitation{}, err
	}
	invite := vaultclient.Invitation{Vault: record.Vault, Owner: c.identity}
	if err := c.drive.CommitVaultRecord(ctx, record); err != nil {
		return invite, err
	}
	_, err = c.openVault(ctx, invite)
	return invite, err
}

func (c *Client) OpenVault(ctx context.Context, invite vaultclient.Invitation) (vaultclient.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.openVault(ctx, invite)
}

func (c *Client) openVault(ctx context.Context, invite vaultclient.Invitation) (vaultclient.State, error) {
	c.clearVault()
	input := vaultclient.HistoryInput{Invite: invite, WitnessKey: c.config.WitnessKey, Keys: make(map[string]vaultclient.MemberKey)}
	var batches [][]vaultclient.Receipt
	var serverHead vaultclient.Head
	for {
		after := int64(len(input.Records))
		page, err := c.drive.VaultRecords(ctx, invite.Vault, after)
		if err != nil {
			return vaultclient.State{}, err
		}
		if len(page.Records) == 0 || len(page.Records) > 32 || len(page.Records) != len(page.Receipts) ||
			after+int64(len(page.Records)) > page.Head.Sequence || page.Head.Sequence > 1024 ||
			(after > 0 && serverHead != page.Head) {
			return vaultclient.State{}, errors.New("vault history changed or exceeded the staging limit")
		}
		input.Records = append(input.Records, page.Records...)
		batches = append(batches, page.Receipts)
		serverHead = page.Head
		if int64(len(input.Records)) == serverHead.Sequence {
			break
		}
	}
	for _, record := range input.Records {
		for _, member := range record.State.Members {
			if _, ok := input.Keys[member.User]; ok {
				continue
			}
			key, err := c.drive.VaultKey(ctx, member.User)
			if err != nil {
				return vaultclient.State{}, err
			}
			input.Keys[member.User] = key
		}
	}
	head, err := c.session.VerifyHistory(input)
	if err != nil {
		return vaultclient.State{}, err
	}
	if head != serverHead {
		return vaultclient.State{}, errors.New("vault history did not match its advertised head")
	}
	checkpoint, status, err := c.witnessRequest(ctx, head.Log, nil)
	if err != nil {
		return vaultclient.State{}, err
	}
	if status != http.StatusOK && status != http.StatusNotFound {
		return vaultclient.State{}, fmt.Errorf("witness read returned %d", status)
	}
	if status == http.StatusOK && checkpoint.Head.Sequence > head.Sequence {
		return vaultclient.State{}, errors.New("witness has a newer vault head")
	}
	if status == http.StatusNotFound || checkpoint.Head.Sequence < head.Sequence {
		for _, batch := range batches {
			for len(batch) > 0 && batch[0].Link.Sequence <= checkpoint.Head.Sequence {
				batch = batch[1:]
			}
			if len(batch) == 0 {
				continue
			}
			next, status, err := c.witnessRequest(ctx, head.Log, batch)
			if err != nil {
				return vaultclient.State{}, err
			}
			if status == http.StatusConflict {
				break // Another linked client may have advanced the log.
			}
			if status != http.StatusOK {
				return vaultclient.State{}, fmt.Errorf("witness append returned %d", status)
			}
			checkpoint = next
		}
		checkpoint, status, err = c.witnessRequest(ctx, head.Log, nil)
		if err != nil || status != http.StatusOK {
			return vaultclient.State{}, errors.New("could not read the committed witness checkpoint")
		}
	}
	input.Checkpoint = checkpoint
	state, err := c.session.OpenHistory(input)
	if err != nil {
		return vaultclient.State{}, err
	}
	if err := c.drive.CheckVault(ctx, invite.Vault, checkpoint); err != nil {
		c.clearVault()
		return vaultclient.State{}, err
	}
	c.invite, c.checkpoint = invite, checkpoint
	return state, nil
}

func (c *Client) witnessRequest(ctx context.Context, log string, receipts []vaultclient.Receipt) (vaultclient.Checkpoint, int, error) {
	var checkpoint vaultclient.Checkpoint
	method := http.MethodGet
	var body io.Reader
	if receipts != nil {
		data, err := json.Marshal(receipts)
		if err != nil {
			return checkpoint, 0, err
		}
		method, body = http.MethodPost, bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.config.WitnessOrigin+"/v1/logs/"+log, body)
	if err != nil {
		return checkpoint, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.witness.Do(req)
	if err != nil {
		return checkpoint, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return checkpoint, resp.StatusCode, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<10)+1))
	if err != nil || len(data) > 16<<10 || json.Unmarshal(data, &checkpoint) != nil {
		return checkpoint, resp.StatusCode, errors.New("invalid witness response")
	}
	return checkpoint, resp.StatusCode, nil
}

func (c *Client) refresh(ctx context.Context) error {
	if c.invite.Vault == "" {
		return errors.New("open a verified vault first")
	}
	checkpoint, status, err := c.witnessRequest(ctx, c.checkpoint.Head.Log, nil)
	if err != nil || status != http.StatusOK {
		return errors.New("could not refresh the vault witness checkpoint")
	}
	if err := c.session.RefreshCheckpoint(checkpoint); err != nil {
		c.clearVault()
		return err
	}
	if err := c.drive.CheckVault(ctx, c.invite.Vault, checkpoint); err != nil {
		c.clearVault()
		return err
	}
	c.checkpoint = checkpoint
	return nil
}

func (c *Client) ChangeMembership(ctx context.Context, action string, target vaultclient.MemberPin) (vaultclient.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.refresh(ctx); err != nil {
		return vaultclient.State{}, err
	}
	var key vaultclient.MemberKey
	if action == "add" {
		var err error
		key, err = c.drive.VaultKey(ctx, target.User)
		if err != nil {
			return vaultclient.State{}, err
		}
	}
	record, err := c.session.ChangeMembership(vaultclient.MembershipChange{Action: action, Target: target, Key: key})
	if err != nil {
		return vaultclient.State{}, err
	}
	if err := c.drive.CommitVaultRecord(ctx, record); err != nil {
		return vaultclient.State{}, err
	}
	return c.openVault(ctx, c.invite)
}

func (c *Client) ReadRevision(ctx context.Context, file string, number int64) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	_, plaintext, err := c.readRevision(ctx, file, number)
	return plaintext, err
}

func (c *Client) readRevision(ctx context.Context, file string, number int64) (int64, []byte, error) {
	revision, err := c.drive.VaultRevision(ctx, c.invite.Vault, file, number)
	if err != nil {
		return 0, nil, err
	}
	plaintext, err := c.session.ReadRevision(revision)
	return revision.Number, plaintext, err
}

func (c *Client) SaveRevision(ctx context.Context, file string, number int64, plaintext []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.refresh(ctx); err != nil {
		return err
	}
	return c.saveRevision(ctx, file, number, "", plaintext)
}

func (c *Client) saveRevision(ctx context.Context, file string, number int64, binding string, plaintext []byte) error {
	rev, err := c.session.SealRevision(vaultclient.RevisionInput{File: file, Number: number, Body: plaintext, Binding: binding})
	if err != nil {
		return err
	}
	return c.drive.SaveVaultRevision(ctx, rev, c.checkpoint)
}
