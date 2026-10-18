// Package engine keeps chosen KyDrive workspaces in folders under a local sync root. One pass
// (Sync) pulls the server's changes, then pushes local ones. No path deletes a local edit
// that never uploaded: such files are renamed to a conflict copy or moved to Unsynced/.
package engine

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Busnes-app/kydrive-desktop/core/api"
)

var (
	ErrRevoked   = errors.New("this computer's link was revoked")
	ErrSignedOut = errors.New("KyDrive no longer accepts this computer's credentials")
)

// Unsynced is the folder under the root that keeps edits the server never received.
const Unsynced = "Unsynced"

type Engine struct {
	Root     string
	Device   string // names conflict copies
	DB       *DB
	API      *api.Client
	Settle   time.Duration // a local file uploads once unchanged this long
	CaseFold bool          // the file system ignores case (Windows, macOS)
	WinNames bool          // skip names Windows cannot hold
	Now      func() time.Time

	// Unsettled is set by a pass that skipped a file still being written.
	Unsettled bool
}

// errOutside refuses a path that would leave the workspace folder.
var errOutside = errors.New("path leaves the workspace folder")

// dir is the workspace's folder under the root.
func (e *Engine) dir(w Workspace) string { return filepath.Join(e.Root, w.Dir) }

// abs resolves rel inside the workspace folder. It refuses a rel that is not local, and one
// with an ancestor that is a symlink, so no server name or local link can lead outside.
func (e *Engine) abs(w Workspace, rel string) (string, error) {
	root := e.dir(w)
	if rel == "" {
		return root, nil
	}
	r := filepath.FromSlash(rel)
	if !filepath.IsLocal(w.Dir) || !filepath.IsLocal(r) || strings.ContainsRune(rel, '\\') {
		return "", errOutside
	}
	p := root
	for _, part := range strings.Split(filepath.Dir(r), string(filepath.Separator)) {
		if part == "." {
			break
		}
		p = filepath.Join(p, part)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return "", errOutside
		}
	}
	return filepath.Join(root, r), nil
}

func (e *Engine) warn(format string, a ...any) { e.DB.Log("warn", fmt.Sprintf(format, a...)) }

// Choose sets the synced workspaces. A newly chosen one gets a folder named after it; one no
// longer chosen is evicted.
func (e *Engine) Choose(ctx context.Context, ids []string) error {
	list, err := e.API.Workspaces(ctx)
	if err != nil {
		return e.fail(err)
	}
	current, err := e.DB.Workspaces()
	if err != nil {
		return err
	}
	taken := []string{Unsynced}
	for _, w := range current {
		if !slices.Contains(ids, w.ID) {
			if err := e.evict(w, ""); err != nil {
				return err
			}
			if err := e.DB.DropWorkspace(w.ID); err != nil {
				return err
			}
			continue
		}
		taken = append(taken, w.Dir)
	}
	slices.SortFunc(list, func(a, b api.Workspace) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	for _, s := range list {
		if !slices.Contains(ids, s.ID) || slices.ContainsFunc(current, func(w Workspace) bool { return w.ID == s.ID }) {
			continue
		}
		dir := folderName(s.Name)
		for i := 2; slices.ContainsFunc(taken, func(t string) bool { return strings.EqualFold(t, dir) }); i++ {
			dir = fmt.Sprintf("%s (%d)", folderName(s.Name), i)
		}
		taken = append(taken, dir)
		if err := e.DB.PutWorkspace(Workspace{ID: s.ID, Name: s.Name, Dir: dir, Cursor: -1}); err != nil {
			return err
		}
	}
	return nil
}

func folderName(name string) string {
	name = strings.Trim(strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '_'
		}
		return r
	}, name), " .")
	if name == "" || !windowsName(name) {
		return "Workspace"
	}
	return name
}

// ConfirmDeletes answers the large-delete guard. Refusing re-downloads what was deleted.
func (e *Engine) ConfirmDeletes(workspace string, allow bool) error {
	ws, err := e.DB.Workspaces()
	if err != nil {
		return err
	}
	for _, w := range ws {
		if w.ID == workspace {
			if allow {
				w.AllowDeletes = true
			} else {
				w.Deletes, w.Cursor = 0, -1
			}
			return e.DB.PutWorkspace(w)
		}
	}
	return fmt.Errorf("workspace %s is not synced", workspace)
}

// Sync runs one full pass over every chosen workspace: pull, then a scan and push.
func (e *Engine) Sync(ctx context.Context) error { return e.fail(e.sync(ctx, true)) }

// Poll pulls server changes only.
func (e *Engine) Poll(ctx context.Context) error { return e.fail(e.sync(ctx, false)) }

func (e *Engine) sync(ctx context.Context, local bool) error {
	list, err := e.API.Workspaces(ctx)
	if err != nil {
		return err
	}
	ws, err := e.DB.Workspaces()
	if err != nil {
		return err
	}
	for _, w := range ws {
		i := slices.IndexFunc(list, func(s api.Workspace) bool { return s.ID == w.ID })
		if i < 0 {
			if err := e.lose(w); err != nil {
				return err
			}
			continue
		}
		if w.Deletes > 0 && !w.AllowDeletes {
			continue
		}
		err := e.pull(ctx, w, list[i].Head)
		if s := api.Status(err); s == 403 || s == 404 {
			if err := e.lose(w); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		ws, err := e.DB.Workspaces()
		if err != nil {
			return err
		}
		if !local || list[i].Role == "reader" {
			continue // a reader has nothing to send
		}
		if i := slices.IndexFunc(ws, func(x Workspace) bool { return x.ID == w.ID }); i >= 0 {
			if err := e.push(ctx, ws[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// fail turns a revoked or refused credential into ErrRevoked or ErrSignedOut, wiping first
// when the server says so.
func (e *Engine) fail(err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Status != 401 {
		return err
	}
	if !ae.Revoked() {
		e.warn("KyDrive refused this computer's credentials; syncing stopped")
		return ErrSignedOut
	}
	if ae.Wipe {
		if werr := e.Wipe(); werr != nil {
			return errors.Join(ErrRevoked, werr)
		}
		e.warn("This computer was revoked. Synced files were removed; unuploaded edits are in %s", Unsynced)
	} else {
		e.warn("This computer was revoked; syncing stopped and files were kept")
	}
	return ErrRevoked
}

// Wipe evicts every workspace: unuploaded edits move to Unsynced/, the rest is deleted.
func (e *Engine) Wipe() error {
	ws, err := e.DB.Workspaces()
	if err != nil {
		return err
	}
	for _, w := range ws {
		if err := e.evict(w, ""); err != nil {
			return err
		}
		if err := e.DB.DropWorkspace(w.ID); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) lose(w Workspace) error {
	if err := e.evict(w, ""); err != nil {
		return err
	}
	e.warn("Lost access to %s. Its synced files were removed; unuploaded edits are in %s", w.Name, path.Join(Unsynced, w.Dir))
	return e.DB.DropWorkspace(w.ID)
}

// evict removes rel and everything below it from disk, moving each file that is not a
// synced, unchanged copy to Unsynced/<workspace folder>/rel.
func (e *Engine) evict(w Workspace, rel string) error {
	top, err := e.abs(w, rel)
	if err != nil {
		e.warn("Kept %s on disk: %v", rel, err)
		return e.DB.DropTree(w.ID, rel)
	}
	var dirs []string
	err = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		r, _ := filepath.Rel(e.dir(w), p)
		r = filepath.ToSlash(r)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".kydrive-") && strings.HasSuffix(d.Name(), ".tmp") {
			return os.Remove(p)
		}
		if it, _ := e.DB.ItemAt(w.ID, r); it != nil && it.State == "synced" && !e.changed(*it, p, fi) {
			return os.Remove(p)
		}
		dst := filepath.Join(e.Root, Unsynced, w.Dir, filepath.FromSlash(r))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if dst, err = freePath(dst); err != nil {
			return err
		}
		if err := os.Rename(p, dst); err != nil {
			return err
		}
		rd, _ := filepath.Rel(e.Root, dst)
		return e.DB.AddIssue(w.ID, filepath.ToSlash(rd), "unsynced", "never uploaded; kept here")
	})
	if err != nil {
		return err
	}
	for _, d := range slices.Backward(dirs) {
		os.Remove(d)
	}
	return e.DB.DropTree(w.ID, rel)
}

// changed reports whether the local file differs from what the last sync left.
func (e *Engine) changed(it Item, p string, fi fs.FileInfo) bool {
	return it.State != "synced" || fi.Size() != it.LocalSize || fi.ModTime().UnixNano() != it.LocalMtime || inode(p, fi) != it.LocalInode
}

// maxName is the longest file name in bytes: the limit on Linux and macOS and the server's, and
// within Windows' 255 UTF-16 units.
const maxName = 255

// splitName splits name into stem and extension; a dotfile is all stem.
func splitName(name string) (stem, ext string) {
	ext = filepath.Ext(name)
	if stem = strings.TrimSuffix(name, ext); stem == "" {
		return name, ""
	}
	return stem, ext
}

// fitName joins stem, suffix and ext, shortening stem (at a rune boundary) so the result fits
// maxName. An extension too long to keep becomes part of the stem.
func fitName(stem, suffix, ext string) string {
	if len(suffix)+len(ext) >= maxName {
		stem, ext = stem+ext, ""
	}
	for len(stem)+len(suffix)+len(ext) > maxName {
		_, n := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-n]
	}
	return stem + suffix + ext
}

// freePath returns p, or p with " (2)", " (3)"… before its extension if p exists. Any error
// other than not-existing (a name too long, no permission) is returned, never retried.
func freePath(p string) (string, error) {
	dir, name := filepath.Split(p)
	stem, ext := splitName(name)
	for i := 2; ; i++ {
		_, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return p, nil
		}
		if err != nil {
			return "", err
		}
		p = filepath.Join(dir, fitName(stem, fmt.Sprintf(" (%d)", i), ext))
	}
}

// conflict renames the local file at rel to a conflict copy, which the next push uploads as
// a new file, and lists it.
func (e *Engine) conflict(w Workspace, rel string) error {
	stem, ext := splitName(path.Base(rel))
	name := fitName(stem, fmt.Sprintf(" (conflict from %s %s)", e.Device, e.Now().Format("2006-01-02")), ext)
	src, err := e.abs(w, rel)
	if err != nil {
		return err
	}
	dst, err := freePath(filepath.Join(filepath.Dir(src), name))
	if err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	r, _ := filepath.Rel(e.dir(w), dst)
	e.warn("%s was edited here and on the server; your copy is %s", rel, filepath.Base(dst))
	return e.DB.AddIssue(w.ID, filepath.ToSlash(r), "conflict", "edited here and on the server; both kept")
}

func sameContent(p, digest string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == digest
}

// ignored names are never uploaded, and never downloaded either, so that ignoring them
// locally cannot read as a delete.
func ignored(name string) bool {
	l := strings.ToLower(name)
	return l == ".ds_store" || l == "thumbs.db" || l == "desktop.ini" ||
		strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".~lock.") || strings.HasSuffix(l, ".tmp")
}

// windowsName reports whether Windows can hold name.
func windowsName(name string) bool {
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || strings.ContainsAny(name, `<>:"|?*`) {
		return false
	}
	for _, r := range name {
		if r < 32 {
			return false
		}
	}
	stem, _, _ := strings.Cut(strings.ToUpper(name), ".")
	switch strings.TrimRight(stem, " ") {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}
