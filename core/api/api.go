// Package api is the KyDrive server's REST API as the sync core uses it. Request and response
// shapes follow the server; see core/API-ASSUMPTIONS.md for the parts not yet verified.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role"`
	Head int64  `json:"head"`
}

type Folder struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
	Parent    string `json:"parent"`
	Name      string `json:"name"`
}

type File struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
	Parent    string `json:"parent"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"` // hex SHA-256 of the content
	Mtime     string `json:"mtime"`  // RFC 3339 client mtime
}

type Change struct {
	Kind    string  `json:"kind"` // "file" or "folder"
	ID      string  `json:"id"`
	Removed bool    `json:"removed"`
	File    *File   `json:"file"`
	Folder  *Folder `json:"folder"`
}

type Changes struct {
	Changes []Change `json:"changes"`
	Next    int64    `json:"next"`
	More    bool     `json:"more"`
}

type Snapshot struct {
	Folders []Folder `json:"folders"`
	Files   []File   `json:"files"`
	Head    int64    `json:"head"`
	Next    string   `json:"next"` // empty on the last page
}

type Token struct {
	Token      string `json:"token"`
	DevicesURL string `json:"devices_url"`
}

// Error is a non-2xx answer.
type Error struct {
	Status int
	Code   string `json:"error"`
	Wipe   bool   `json:"wipe"`
}

func (e *Error) Error() string { return fmt.Sprintf("server answered %d: %s", e.Status, e.Code) }

// Revoked reports that this device was revoked; Wipe then says whether to remove its files.
func (e *Error) Revoked() bool { return e.Status == 401 && e.Code == "device_revoked" }

// Status is err's HTTP status, or 0 when err is not a server answer.
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

type Client struct {
	Base  string // server origin, e.g. https://drive.example
	Token string
	HTTP  *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Base, "/")+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		e := &Error{}
		json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(e)
		e.Status = res.StatusCode
		return nil, e
	}
	return res, nil
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	h := http.Header{}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
		h.Set("Content-Type", "application/json")
	}
	res, err := c.do(ctx, method, path, body, h)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	var out []Workspace
	return out, c.call(ctx, "GET", "/api/drive/workspaces", nil, &out)
}

// Changes reads the workspace's change feed after seq; a 410 means re-snapshot.
func (c *Client) Changes(ctx context.Context, workspace string, after int64) (Changes, error) {
	var out Changes
	q := url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {"1000"}}
	return out, c.call(ctx, "GET", "/api/drive/workspaces/"+url.PathEscape(workspace)+"/changes?"+q.Encode(), nil, &out)
}

// Snapshot reads one page of the workspace's live folders and files after the item ID after.
func (c *Client) Snapshot(ctx context.Context, workspace, after string) (Snapshot, error) {
	var out Snapshot
	q := url.Values{"after": {after}, "limit": {"1000"}}
	return out, c.call(ctx, "GET", "/api/drive/workspaces/"+url.PathEscape(workspace)+"/snapshot?"+q.Encode(), nil, &out)
}

// Download writes f's content to w and fails unless it matches f.Size and f.Digest.
func (c *Client) Download(ctx context.Context, f File, w io.Writer) error {
	if len(f.Digest) != 64 {
		return fmt.Errorf("file %s has no digest", f.ID)
	}
	res, err := c.do(ctx, "GET", "/api/drive/files/"+url.PathEscape(f.ID)+"/download", nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(res.Body, f.Size+1))
	if err != nil {
		return err
	}
	if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.Digest {
		return fmt.Errorf("file %s: content does not match its digest", f.ID)
	}
	return nil
}

// Upload describes a new file (File empty) or a new version of File at Revision.
type Upload struct {
	Parent, Name, File string
	Revision           int64
	Size               int64
	Mtime              time.Time
}

// chunkSize stays under the server's 64 MiB chunk limit.
var chunkSize int64 = 8 << 20

// Upload sends r through a resumable session. A failed chunk resumes from the server's
// offset; five failures in a row give up.
func (c *Client) Upload(ctx context.Context, workspace string, u Upload, r io.ReaderAt) (File, error) {
	var s struct {
		ID       string `json:"id"`
		Received int64  `json:"received"`
	}
	start := map[string]any{"parent": u.Parent, "name": u.Name, "file": u.File, "revision": u.Revision, "size": u.Size, "mtime": u.Mtime.UTC().Format(time.RFC3339)}
	if err := c.call(ctx, "POST", "/api/drive/workspaces/"+url.PathEscape(workspace)+"/upload-sessions", start, &s); err != nil {
		return File{}, err
	}
	path := "/api/drive/upload-sessions/" + url.PathEscape(s.ID)
	for off, fails := int64(0), 0; off < u.Size; {
		end := min(off+chunkSize, u.Size)
		h := http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", off, end-1, u.Size)}}
		res, err := c.do(ctx, "PUT", path, io.NewSectionReader(r, off, end-off), h)
		if err == nil {
			res.Body.Close()
			off = end
			fails = 0
			continue
		}
		if ctx.Err() != nil || (Status(err) != 0 && Status(err) != 409 && Status(err) < 500) {
			return File{}, err
		}
		if fails++; fails == 5 {
			return File{}, err
		}
		if err := c.call(ctx, "GET", path, nil, &s); err != nil {
			return File{}, err
		}
		off = s.Received
	}
	var f File
	return f, c.call(ctx, "POST", path+"/complete", nil, &f)
}

func (c *Client) AddFolder(ctx context.Context, workspace, parent, name string) (Folder, error) {
	var f Folder
	return f, c.call(ctx, "POST", "/api/drive/workspaces/"+url.PathEscape(workspace)+"/folders", map[string]string{"parent": parent, "name": name}, &f)
}

func (c *Client) MoveFile(ctx context.Context, id string, revision int64, workspace, parent, name string) (File, error) {
	var f File
	in := map[string]any{"revision": revision, "workspace": workspace, "parent": parent, "name": name}
	return f, c.call(ctx, "PUT", "/api/drive/files/"+url.PathEscape(id)+"/location", in, &f)
}

func (c *Client) MoveFolder(ctx context.Context, id, parent, name string) (Folder, error) {
	var f Folder
	return f, c.call(ctx, "PUT", "/api/drive/folders/"+url.PathEscape(id)+"/location", map[string]string{"parent": parent, "name": name}, &f)
}

func (c *Client) TrashFile(ctx context.Context, id string, revision int64) error {
	return c.call(ctx, "PUT", "/api/drive/files/"+url.PathEscape(id)+"/trash", map[string]any{"revision": revision, "trashed": true}, nil)
}

func (c *Client) TrashFolder(ctx context.Context, id string) error {
	return c.call(ctx, "PUT", "/api/drive/folders/"+url.PathEscape(id)+"/trash", map[string]bool{"trashed": true}, nil)
}

// RevokeSelf revokes this device on the server without a wipe. An already revoked device
// counts as done.
func (c *Client) RevokeSelf(ctx context.Context) error {
	err := c.call(ctx, "DELETE", "/api/devices/self", nil, nil)
	var e *Error
	if errors.As(err, &e) && e.Revoked() {
		return nil
	}
	return err
}

// Redeem exchanges a link code and its PKCE verifier for a device token.
func (c *Client) Redeem(ctx context.Context, code, verifier string) (Token, error) {
	form := url.Values{"code": {code}, "verifier": {verifier}}
	h := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	res, err := c.do(ctx, "POST", "/api/devices/token", strings.NewReader(form.Encode()), h)
	if err != nil {
		return Token{}, err
	}
	defer res.Body.Close()
	var t Token
	if err := json.NewDecoder(res.Body).Decode(&t); err != nil || !strings.HasPrefix(t.Token, "kyd_") {
		return Token{}, fmt.Errorf("token exchange returned no device token")
	}
	return t, nil
}
