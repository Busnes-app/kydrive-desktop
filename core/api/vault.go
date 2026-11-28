package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/Busnes-app/kydrive-server/pkg/vaultclient"
)

type VaultScope struct {
	User string `json:"user_id"`
	Org  string `json:"org_id"`
}

type VaultPage struct {
	Head     vaultclient.Head      `json:"head"`
	Records  []vaultclient.Record  `json:"records"`
	Receipts []vaultclient.Receipt `json:"receipts"`
}

type EditorGrant struct {
	Token   string `json:"token"`
	Expires int64  `json:"expires_ms"`
}

const (
	vaultManifestHeader   = "KyDrive-Vault-Revision"
	vaultCheckpointHeader = "KyDrive-Vault-Checkpoint"
	vaultJSONLimit        = 2 << 20
)

func (c *Client) vaultJSON(ctx context.Context, method, path string, in, out any) error {
	if c.Key == nil || c.Token == "" {
		return errors.New("vault access requires a signed device")
	}
	var body io.Reader
	header := make(http.Header)
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
		header.Set("Content-Type", "application/json")
	}
	resp, err := c.do(ctx, method, path, body, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	data, err := readVaultBounded(resp.Body, vaultJSONLimit)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func readVaultBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("vault response exceeds its size limit")
	}
	return data, nil
}

func vaultPath(id string) string { return "/api/vaults/" + id }

func (c *Client) VaultScope(ctx context.Context) (VaultScope, error) {
	var out VaultScope
	err := c.vaultJSON(ctx, http.MethodGet, "/api/vault/account", nil, &out)
	return out, err
}

func (c *Client) EnrollVaultKey(ctx context.Context, key vaultclient.MemberKey) error {
	return c.vaultJSON(ctx, http.MethodPut, "/api/vault-keys", key, nil)
}

func (c *Client) VaultKey(ctx context.Context, user string) (vaultclient.MemberKey, error) {
	var out vaultclient.MemberKey
	if !vaultclient.ValidUserID(user) {
		return out, errors.New("invalid vault member ID")
	}
	err := c.vaultJSON(ctx, http.MethodGet, "/api/vault-keys/"+user, nil, &out)
	return out, err
}

func (c *Client) VaultRecords(ctx context.Context, id string, after int64) (VaultPage, error) {
	var page VaultPage
	if !vaultclient.ValidID(id) || after < 0 {
		return page, errors.New("invalid vault history request")
	}
	err := c.vaultJSON(ctx, http.MethodGet, vaultPath(id)+"/records?after="+strconv.FormatInt(after, 10), nil, &page)
	return page, err
}

func (c *Client) CommitVaultRecord(ctx context.Context, record vaultclient.Record) error {
	if !vaultclient.ValidID(record.Vault) {
		return errors.New("invalid vault ID")
	}
	return c.vaultJSON(ctx, http.MethodPost, vaultPath(record.Vault)+"/records", record, nil)
}

func vaultFilePath(id, file string) (string, error) {
	if !vaultclient.ValidID(id) || !vaultclient.ValidID(file) {
		return "", errors.New("invalid vault or file ID")
	}
	return vaultPath(id) + "/files/" + file, nil
}

func vaultHeader(value any) string {
	data, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(data)
}

func (c *Client) SaveVaultRevision(ctx context.Context, revision vaultclient.SealedRevision, checkpoint vaultclient.Checkpoint) error {
	m := revision.Manifest
	path, err := vaultFilePath(m.Vault, m.File)
	if err != nil || c.Key == nil || c.Token == "" || m.Number < 1 ||
		len(revision.Body) > vaultclient.MaxDocumentBytes || m.Size != int64(len(revision.Body)) {
		return errors.New("invalid signed vault revision request")
	}
	headers := http.Header{
		vaultManifestHeader:   {vaultHeader(m)},
		vaultCheckpointHeader: {vaultHeader(checkpoint)},
		"Content-Type":        {"application/octet-stream"},
	}
	resp, err := c.do(ctx, http.MethodPut, path+"/versions/"+strconv.FormatInt(m.Number, 10), bytes.NewReader(revision.Body), headers)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *Client) VaultRevision(ctx context.Context, id, file string, number int64) (vaultclient.RevisionRead, error) {
	var out vaultclient.RevisionRead
	path, err := vaultFilePath(id, file)
	if err != nil || c.Key == nil || c.Token == "" || number < 0 {
		return out, errors.New("invalid signed vault revision request")
	}
	version := "latest"
	if number > 0 {
		version = strconv.FormatInt(number, 10)
	}
	resp, err := c.do(ctx, http.MethodGet, path+"/versions/"+version, nil, nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	values := resp.Header.Values(vaultManifestHeader)
	if len(values) != 1 || len(values[0]) > 16<<10 {
		return out, errors.New("missing or oversized vault revision manifest")
	}
	data, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil || json.Unmarshal(data, &out.Manifest) != nil || out.Manifest.Number < 1 {
		return out, errors.New("invalid vault revision manifest")
	}
	out.File, out.Number = file, number
	if number == 0 {
		out.Number = out.Manifest.Number
	}
	out.Body, err = readVaultBounded(resp.Body, vaultclient.MaxDocumentBytes)
	return out, err
}

func (c *Client) VaultBinding(ctx context.Context, id, file string) (vaultclient.Binding, error) {
	var binding vaultclient.Binding
	path, err := vaultFilePath(id, file)
	if err == nil {
		err = c.vaultJSON(ctx, http.MethodGet, path+"/editor", nil, &binding)
	}
	return binding, err
}

func (c *Client) BindVaultEditor(ctx context.Context, binding vaultclient.Binding, cp vaultclient.Checkpoint) error {
	path, err := vaultFilePath(binding.Vault, binding.File)
	if err != nil {
		return err
	}
	return c.vaultJSON(ctx, http.MethodPut, path+"/editor", struct {
		Binding    vaultclient.Binding    `json:"binding"`
		Checkpoint vaultclient.Checkpoint `json:"checkpoint"`
	}{binding, cp}, nil)
}

func (c *Client) VaultEditorGrant(ctx context.Context, id, file string, session vaultclient.EditorSession) (EditorGrant, error) {
	var out EditorGrant
	path, err := vaultFilePath(id, file)
	if err != nil {
		return out, err
	}
	err = c.vaultJSON(ctx, http.MethodPost, path+"/editor/session", struct {
		Checkpoint vaultclient.Checkpoint `json:"checkpoint"`
		Edit       bool                   `json:"edit"`
	}{session.Checkpoint, session.Edit}, &out)
	return out, err
}

func (c *Client) CheckVault(ctx context.Context, id string, cp vaultclient.Checkpoint) error {
	if !vaultclient.ValidID(id) {
		return errors.New("invalid vault ID")
	}
	return c.vaultJSON(ctx, http.MethodPost, vaultPath(id)+"/check", struct {
		Checkpoint vaultclient.Checkpoint `json:"checkpoint"`
	}{cp}, nil)
}
