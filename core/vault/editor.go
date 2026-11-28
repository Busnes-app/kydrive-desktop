package vault

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-server/pkg/vaultclient"
)

// EditorCapability is passed only to the signed app's isolated, packaged CryptPad editor.
// The app derives the reserved room and checkpoint IDs before initializing a new session.
type EditorCapability struct {
	Hash   string
	Format string
	Room   string
	Rooms  []string
	Blobs  []string
}

// Editor describes an opened file. Its source is nil when a peer joins encrypted room history.
type Editor struct {
	Capability EditorCapability
	Source     []byte
	Grant      api.EditorGrant

	vault, file, binding string
}

var (
	editorHash = regexp.MustCompile(`^/2/integration/edit/[A-Za-z0-9+\-]{24}/$`)
	roomID     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	blobID     = regexp.MustCompile(`^[a-f0-9]{48}$`)
	leaseToken = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
)

type wireEditor struct {
	Hash       string   `json:"hash,omitempty"`
	EditorHash string   `json:"editorHash,omitempty"`
	Format     string   `json:"format,omitempty"`
	Room       string   `json:"room"`
	Rooms      []string `json:"rooms"`
	Blobs      []string `json:"blobs"`
}

func (e EditorCapability) valid() bool {
	switch e.Format {
	case "docx", "xlsx", "pptx":
	default:
		return false
	}
	if !editorHash.MatchString(e.Hash) || !roomID.MatchString(e.Room) || len(e.Rooms) != 3 || len(e.Blobs) != 3 {
		return false
	}
	for i := range e.Rooms {
		if !roomID.MatchString(e.Rooms[i]) || !blobID.MatchString(e.Blobs[i]) || e.Rooms[i] == e.Room ||
			slices.Contains(e.Rooms[:i], e.Rooms[i]) || slices.Contains(e.Blobs[:i], e.Blobs[i]) {
			return false
		}
	}
	return true
}

func (e EditorCapability) encode() string {
	wire := wireEditor{Room: e.Room, Rooms: e.Rooms, Blobs: e.Blobs}
	if e.Format == "docx" {
		wire.Hash = e.Hash // preserve the original DOCX binding shape
	} else {
		wire.EditorHash, wire.Format = e.Hash, e.Format
	}
	data, _ := json.Marshal(wire)
	return string(data)
}

func decodeEditor(session vaultclient.EditorSession, binding vaultclient.Binding) (EditorCapability, error) {
	var wire wireEditor
	if err := json.Unmarshal([]byte(session.Capability), &wire); err != nil {
		return EditorCapability{}, errors.New("invalid CryptPad editor capability")
	}
	if wire.Format == "" && wire.EditorHash == "" {
		wire.Format, wire.EditorHash = "docx", wire.Hash
	} else if wire.Hash != "" || wire.Format == "docx" {
		return EditorCapability{}, errors.New("ambiguous CryptPad editor capability")
	}
	e := EditorCapability{wire.EditorHash, wire.Format, wire.Room, wire.Rooms, wire.Blobs}
	if !e.valid() || len(session.Rooms) != 4 || len(binding.Blobs) != 3 || !slices.Contains(session.Rooms, e.Room) {
		return EditorCapability{}, errors.New("invalid CryptPad editor scope")
	}
	for i := range e.Rooms {
		if !slices.Contains(session.Rooms, e.Rooms[i]) || !slices.Contains(binding.Blobs, e.Blobs[i]) {
			return EditorCapability{}, errors.New("CryptPad editor scope does not match its signed binding")
		}
	}
	return e, nil
}

// OpenEditor joins an existing editor binding or initializes one from the requested
// verified revision. prepared comes from the app's local editor bundle and is only
// used for a new binding.
func (c *Client) OpenEditor(ctx context.Context, file string, sourceNumber int64, prepared *EditorCapability) (Editor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.refresh(ctx); err != nil {
		return Editor{}, err
	}
	vault := c.invite.Vault
	binding, err := c.drive.VaultBinding(ctx, vault, file)
	var source []byte
	if api.Status(err) == http.StatusNotFound && prepared != nil {
		if !prepared.valid() || sourceNumber < 1 {
			return Editor{}, errors.New("invalid initial CryptPad editor scope or revision")
		}
		_, source, err = c.readRevision(ctx, file, sourceNumber)
		if err != nil {
			return Editor{}, err
		}
		binding, err = c.session.BindEditor(vaultclient.EditorInput{
			File: file, Source: sourceNumber, Rooms: append([]string{prepared.Room}, prepared.Rooms...),
			Blobs: slices.Clone(prepared.Blobs), EditCapability: prepared.encode(),
		})
		if err == nil {
			err = c.drive.BindVaultEditor(ctx, binding, c.checkpoint)
		}
	}
	if err != nil {
		return Editor{}, err
	}
	session, err := c.session.OpenEditor(vaultclient.EditorOpen{File: file, Binding: binding})
	if err != nil {
		return Editor{}, err
	}
	if !session.Edit {
		return Editor{}, errors.New("CryptPad co-editing requires editor access")
	}
	capability, err := decodeEditor(session, binding)
	if err != nil {
		return Editor{}, err
	}
	grant, err := c.grant(ctx, file, session)
	if err != nil {
		return Editor{}, err
	}
	return Editor{Capability: capability, Source: source, Grant: grant, vault: vault, file: file, binding: session.Binding}, nil
}

// RefreshEditor renews a lease for the same signed binding. The editor host closes its
// frame when renewal fails and reopens only after the user verifies the new head.
func (c *Client) RefreshEditor(ctx context.Context, editor Editor) (api.EditorGrant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, err := c.currentEditor(ctx, editor)
	if err != nil {
		return api.EditorGrant{}, err
	}
	return c.grant(ctx, editor.file, session)
}

func (c *Client) currentEditor(ctx context.Context, editor Editor) (vaultclient.EditorSession, error) {
	if editor.vault == "" || editor.vault != c.invite.Vault || editor.binding == "" {
		return vaultclient.EditorSession{}, errors.New("the CryptPad editor is no longer open in this vault")
	}
	if err := c.refresh(ctx); err != nil {
		return vaultclient.EditorSession{}, err
	}
	binding, err := c.drive.VaultBinding(ctx, editor.vault, editor.file)
	if err != nil {
		return vaultclient.EditorSession{}, err
	}
	session, err := c.session.OpenEditor(vaultclient.EditorOpen{File: editor.file, Binding: binding})
	if err != nil {
		return vaultclient.EditorSession{}, err
	}
	if !session.Edit || session.Binding != editor.binding {
		return vaultclient.EditorSession{}, errors.New("the CryptPad editor binding changed")
	}
	return session, nil
}

func (c *Client) grant(ctx context.Context, file string, session vaultclient.EditorSession) (api.EditorGrant, error) {
	grant, err := c.drive.VaultEditorGrant(ctx, c.invite.Vault, file, session)
	if err != nil {
		return api.EditorGrant{}, err
	}
	now := time.Now()
	if len(grant.Token) > 2048 || !leaseToken.MatchString(grant.Token) ||
		grant.Expires <= now.UnixMilli() || grant.Expires > now.Add(65*time.Second).UnixMilli() {
		return api.EditorGrant{}, errors.New("invalid CryptPad relay grant")
	}
	return grant, nil
}

// SaveEditor publishes the next signed revision for the active editor binding.
// On conflict, the editor host retains the local export for explicit retry.
func (c *Client) SaveEditor(ctx context.Context, editor Editor, plaintext []byte) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.currentEditor(ctx, editor); err != nil {
		return 0, err
	}
	latest, _, err := c.readRevision(ctx, editor.file, 0)
	if err != nil {
		return 0, err
	}
	if latest >= math.MaxInt64 {
		return 0, errors.New("vault revision history is full")
	}
	if err := c.saveRevision(ctx, editor.file, latest+1, editor.binding, plaintext); err != nil {
		return 0, err
	}
	return latest + 1, nil
}
