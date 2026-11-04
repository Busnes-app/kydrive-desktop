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
	"net/url"
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
	Memory   *Memory // kept by the daemon across passes; nil in tests that need none

	// Unsettled is set by a pass that skipped a file still being written.
	Unsettled bool
}

// errOutside refuses a path that would leave the workspace folder.
var errOutside = errors.New("path leaves the workspace folder")

// errLinked refuses a path under a link (a folder moved elsewhere, a link left in its place).
var errLinked = errors.New("a folder above it is a link")

// dir is the workspace's folder under the root.
func (e *Engine) dir(w Workspace) string { return filepath.Join(e.Root, w.Dir) }

// abs resolves rel inside the workspace folder. It refuses a rel that is not local, and one
// with an ancestor that is a symlink or a Windows junction, so no server name or local link
// can lead outside.
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
		// A Windows junction (a folder moved to another disk) is irregular, not a symlink.
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return "", errLinked
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
				// What could not go stays on disk, no longer synced, rather than everything
				// already removed coming back with a snapshot.
				e.warn("Some files of %s could not be removed and were left in %s: %v", w.Name, w.Dir, err)
			}
			if err := e.DB.DropWorkspace(w.ID); err != nil {
				return err
			}
			continue
		}
		taken = append(taken, w.Dir)
	}
	// Retained files may belong to a previous account; a new workspace gets a fresh folder.
	entries, err := os.ReadDir(e.Root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		taken = append(taken, entry.Name())
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
			dir = fitName(folderName(s.Name), fmt.Sprintf(" (%d)", i), "")
		}
		taken = append(taken, dir)
		if err := e.DB.PutWorkspace(Workspace{ID: s.ID, Name: s.Name, Dir: dir, Cursor: -1}); err != nil {
			return err
		}
	}
	return nil
}

// missing reports that p is known not to exist.
func missing(p string) bool { _, err := os.Lstat(p); return errors.Is(err, fs.ErrNotExist) }

func folderName(name string) string {
	name = strings.Trim(strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\<>:"|?*`, r) {
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
	// Upgrade a previously linked computer using its existing device credential. Keep
	// the old refusal when an older server has no account route; other errors fail closed.
	if e.DB.Get("user_id") == "" {
		account, err := e.API.DeviceAccount(ctx)
		if err == nil {
			if err := e.DB.Set("user_id", account.UserID); err != nil {
				return err
			}
			if err := e.DB.Set("user", account.User); err != nil {
				return err
			}
		} else if api.Status(err) != 404 {
			return err
		}
	}
	ws, err := e.DB.Workspaces()
	if err != nil {
		return err
	}
	var failed error
	for _, w := range ws {
		i := slices.IndexFunc(list, func(s api.Workspace) bool { return s.ID == w.ID })
		if i < 0 {
			// A file that cannot go yet keeps the binding for the next pass, not the others.
			if err := e.lose(w); err != nil {
				failed = errors.Join(failed, fmt.Errorf("%s: %w", w.Name, err))
			}
			continue
		}
		// The workspace folder moved elsewhere with a link (a Windows junction) left in its place:
		// nothing syncs through it, or what the scan cannot see would be trashed.
		if fi, err := os.Lstat(e.dir(w)); err == nil && fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			failed = errors.Join(failed, fmt.Errorf("%s: %s is a link; KyDrive does not sync through links. Move the folder back to sync it", w.Name, e.dir(w)))
			continue
		}
		if w.Cursor >= 0 && !w.AllowDeletes && list[i].Role != "reader" && missing(e.dir(w)) {
			// A pull would recreate the folder and the push then trash what is missing from it,
			// so the guard is armed first. Confirmed deletes still pull first: the server may
			// have new files in the folders about to be trashed.
			if !local {
				continue
			}
			if err := e.deletes(ctx, w, nil, true); err != nil {
				return err
			}
			if w, err = e.workspace(w.ID); err != nil {
				return err
			}
			if w.ID == "" || w.Deletes > 0 {
				continue // held for ConfirmDeletes
			}
		}
		err := e.pull(ctx, w, list[i].Head)
		// Only KyDrive's own answer (a JSON error) means lost access; a proxy's page does not.
		if ae := (*api.Error)(nil); errors.As(err, &ae) && (ae.Status == 403 || ae.Status == 404) && ae.Code != "" {
			if err := e.lose(w); err != nil {
				failed = errors.Join(failed, fmt.Errorf("%s: %w", w.Name, err))
			}
			continue
		}
		if err != nil {
			return err
		}
		if !local {
			continue
		}
		if list[i].Role == "reader" {
			// A reader sends nothing; its local edits are listed so they are not lost unseen.
			if err := e.readerEdits(w); err != nil {
				failed = errors.Join(failed, fmt.Errorf("%s: %w", w.Name, err))
			}
			continue
		}
		if w, err := e.workspace(w.ID); err != nil {
			return err
		} else if w.ID != "" {
			if err := e.push(ctx, w); err != nil {
				if ctx.Err() != nil || api.Status(err) != 0 || errors.As(err, new(*url.Error)) {
					return err
				}
				// One workspace's local trouble (an unreadable folder) does not keep the others.
				failed = errors.Join(failed, fmt.Errorf("%s: %w", w.Name, err))
			}
		}
	}
	return failed
}

// readerEdits lists files added or edited in a reader workspace, once each: they never upload.
func (e *Engine) readerEdits(w Workspace) error {
	entries, err := e.scan(w)
	if err != nil {
		return err
	}
	for _, n := range entries {
		if n.dir() {
			continue
		}
		it, err := e.DB.ItemAt(w.ID, n.rel)
		if err != nil {
			return err
		}
		if it != nil && (it.State == "skipped" || it.Kind != "file" || !e.changed(*it, n.p, n.fi)) {
			continue
		}
		if err := e.DB.AddIssueOnce(w.ID, n.rel, "readonly", "read-only workspace; not uploaded"); err != nil {
			return err
		}
	}
	// A synced copy deleted here comes back with a restore snapshot: kept as a pending
	// delete, it would trash the file on the server once the user can edit.
	if w, err = e.workspace(w.ID); err != nil || w.ID == "" {
		return err
	}
	items, err := e.DB.Items(w.ID)
	if err != nil {
		return err
	}
	retries, err := e.DB.Retries(w.ID)
	if err != nil {
		return err
	}
	for _, it := range items {
		// One waiting in retries (a download backing off) comes with its retry instead.
		if slices.ContainsFunc(retries, func(f api.File) bool { return f.ID == it.ID }) {
			continue
		}
		if p, err := e.abs(w, it.Path); err == nil && it.State == "synced" && missing(p) {
			w.Cursor = -1
			return e.DB.PutWorkspace(w)
		}
	}
	return nil
}

// workspace re-reads a chosen workspace; ID is empty once it is gone.
func (e *Engine) workspace(id string) (Workspace, error) {
	ws, err := e.DB.Workspaces()
	if i := slices.IndexFunc(ws, func(x Workspace) bool { return x.ID == id }); i >= 0 {
		return ws[i], err
	}
	return Workspace{}, err
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
		return ErrSignedOut
	}
	if ae.Wipe {
		if err := e.DB.Set(wipeKey, "1"); err != nil {
			return errors.Join(ErrRevoked, err)
		}
		if werr := e.ResumeWipe(); werr != nil {
			return errors.Join(ErrRevoked, werr)
		}
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
	var failed error
	for _, w := range ws {
		if err := e.evict(w, ""); err != nil {
			failed = errors.Join(failed, err) // the other workspaces still go
			continue
		}
		if err := e.DB.DropWorkspace(w.ID); err != nil {
			return err
		}
	}
	return failed
}

// StartWipe records a wipe and runs it, so what cannot go yet is removed by a later ResumeWipe.
func (e *Engine) StartWipe() error {
	if err := e.DB.Set(wipeKey, "1"); err != nil {
		return err
	}
	return e.ResumeWipe()
}

// wipeKey marks a wipe the server asked for that has not finished; it survives a restart.
const wipeKey = "wipe"

// ResumeWipe finishes a pending wipe. It needs no credentials, so it runs after a revoke.
func (e *Engine) ResumeWipe() error {
	if e.DB.Get(wipeKey) == "" {
		return nil
	}
	if err := e.Wipe(); err != nil {
		return err
	}
	e.warn("This computer's synced files were removed; unuploaded edits are in %s", Unsynced)
	return e.DB.Set(wipeKey, "")
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
	if rel == "" {
		// Until it ends, a fresh snapshot restores what this evict removed, even after a crash.
		w.Cursor = -1
		if err := e.DB.PutWorkspace(w); err != nil {
			return err
		}
	}
	var dirs []string
	var failed error
	err = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			// An unreadable folder does not keep the evict from its siblings.
			failed = errors.Join(failed, err)
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		// One file that cannot go (open elsewhere, no permission) does not keep the rest.
		failed = errors.Join(failed, e.evictFile(w, p, d))
		return nil
	})
	if err = errors.Join(err, failed); err != nil {
		return err
	}
	for _, d := range slices.Backward(dirs) {
		os.Remove(d)
	}
	return e.DB.DropTree(w.ID, rel)
}

func (e *Engine) evictFile(w Workspace, p string, d fs.DirEntry) error {
	r, _ := filepath.Rel(e.dir(w), p)
	r = filepath.ToSlash(r)
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if strings.HasPrefix(d.Name(), ".kydrive-") && strings.HasSuffix(d.Name(), ".tmp") || osJunk(d.Name()) {
		return os.Remove(p)
	}
	// Each file's record goes with it, so a partial evict leaves no record of a file that is
	// gone, which the next push would read as a local delete.
	if it, _ := e.DB.ItemAt(w.ID, r); it != nil && it.State == "synced" && !e.changed(*it, p, fi) {
		// Moved aside first, the copy deleted is the one checked: an editor's save landing at
		// p after the check is never deleted.
		// Not a .tmp name: one left by a crash is kept like any unsynced file, never discarded.
		aside := filepath.Join(filepath.Dir(p), fmt.Sprintf(".kydrive-aside-%d", time.Now().UnixNano()))
		if err := os.Rename(p, aside); err != nil {
			return err
		}
		p = aside
		// Hashed too: an edit written in place that kept the size and mtime is kept.
		if fi, err := os.Lstat(p); err == nil && !e.changed(*it, p, fi) && sameContent(p, it.Digest) {
			if err := os.Remove(p); err != nil {
				return err
			}
			return e.DB.DropTree(w.ID, r)
		}
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
	if err := e.DB.DropTree(w.ID, r); err != nil {
		return err
	}
	rd, _ := filepath.Rel(e.Root, dst)
	return e.DB.AddIssue(w.ID, filepath.ToSlash(rd), "unsynced", "never uploaded; kept here")
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
	device := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"|?*`, r) {
			return '-'
		}
		return r
	}, e.Device)
	name := fitName(stem, fmt.Sprintf(" (conflict from %s %s)", device, e.Now().Format("2006-01-02")), ext)
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
// osJunk names files the system writes on its own: an evict deletes them, never keeping them as
// unuploaded work.
func osJunk(name string) bool {
	l := strings.ToLower(name)
	return l == ".ds_store" || l == "thumbs.db" || l == "desktop.ini"
}

func ignored(name string) bool {
	l := strings.ToLower(name)
	return l == ".ds_store" || l == "thumbs.db" || l == "desktop.ini" ||
		strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".~lock.") || strings.HasSuffix(l, ".tmp")
}

// shortName reports a name shaped like a Windows short (8.3) name, such as REPORT~1.TXT.
func shortName(name string) bool {
	stem, ext := splitName(name)
	i := strings.LastIndex(stem, "~")
	return i > 0 && i < len(stem)-1 && len(stem) <= 8 && len(ext) <= 4 && strings.Trim(stem[i+1:], "0123456789") == ""
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
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³", "CONIN$", "CONOUT$":
		return false
	}
	return true
}
