package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
)

// errSnapshot asks for a full re-enumeration: the feed named a folder or parent this
// computer has never seen.
var errSnapshot = errors.New("unknown folder; snapshot needed")

// pull applies the workspace's change feed from its cursor up to head.
func (e *Engine) pull(ctx context.Context, w Workspace, head int64) error {
	if w.Cursor < 0 {
		return e.snapshot(ctx, w)
	}
	retries, err := e.DB.Retries(w.ID)
	if err != nil {
		return err
	}
	for _, f := range retries {
		if err := e.upsertFile(ctx, w, f); err != nil && !errors.Is(err, errSnapshot) {
			return err
		}
	}
	for w.Cursor != head {
		ch, err := e.API.Changes(ctx, w.ID, w.Cursor)
		if api.Status(err) == 410 {
			return e.snapshot(ctx, w)
		}
		if err != nil {
			return err
		}
		// Upserts parents first, removals last, so a move out of a removed folder survives.
		slices.SortStableFunc(ch.Changes, func(a, b api.Change) int { return rank(a) - rank(b) })
		for _, c := range ch.Changes {
			err := e.apply(ctx, w, c)
			if errors.Is(err, errSnapshot) {
				return e.snapshot(ctx, w)
			}
			if err != nil {
				return err
			}
		}
		w.Cursor = ch.Next
		if err := e.DB.PutWorkspace(w); err != nil {
			return err
		}
		if !ch.More {
			break
		}
	}
	return nil
}

func rank(c api.Change) int {
	switch {
	case c.Removed:
		return 2
	case c.Kind == "folder":
		return 0
	}
	return 1
}

func (e *Engine) apply(ctx context.Context, w Workspace, c api.Change) error {
	switch {
	case c.Removed:
		if err := e.DB.DropRetry(c.ID); err != nil {
			return err
		}
		it, err := e.DB.Item(c.ID)
		if err != nil || it == nil || it.Workspace != w.ID {
			return err
		}
		return e.remove(w, *it)
	case c.Kind == "folder" && c.Folder != nil:
		if it, err := e.DB.Item(c.ID); err != nil || it == nil {
			return cmpErr(err, errSnapshot)
		}
		return e.upsertFolder(w, *c.Folder)
	case c.File != nil:
		return e.upsertFile(ctx, w, *c.File)
	}
	return nil
}

func cmpErr(err, otherwise error) error {
	if err != nil {
		return err
	}
	return otherwise
}

// snapshot enumerates the whole workspace, applies it, removes what is gone and continues
// the feed from the snapshot's head.
func (e *Engine) snapshot(ctx context.Context, w Workspace) error {
	if err := os.MkdirAll(e.dir(w), 0o755); err != nil {
		return err
	}
	var folders []api.Folder
	var files []api.File
	head, after := int64(-1), ""
	for {
		s, err := e.API.Snapshot(ctx, w.ID, after)
		if err != nil {
			return err
		}
		if head < 0 {
			head = s.Head
		}
		folders, files = append(folders, s.Folders...), append(files, s.Files...)
		if s.Next == "" {
			break
		}
		after = s.Next
	}
	seen := map[string]bool{"": true}
	for len(folders) > 0 {
		var later []api.Folder
		for _, f := range folders {
			if !seen[f.Parent] {
				later = append(later, f)
				continue
			}
			if err := e.upsertFolder(w, f); err != nil && !errors.Is(err, errSnapshot) {
				return err
			}
			seen[f.ID] = true
		}
		if len(later) == len(folders) {
			break // orphans: their parents are not in the snapshot
		}
		folders = later
	}
	slices.SortFunc(files, func(a, b api.File) int { return strings.Compare(a.Name, b.Name) })
	for _, f := range files {
		if !seen[f.Parent] {
			continue
		}
		if err := e.upsertFile(ctx, w, f); err != nil && !errors.Is(err, errSnapshot) {
			return err
		}
		seen[f.ID] = true
	}
	retries, err := e.DB.Retries(w.ID)
	if err != nil {
		return err
	}
	for _, f := range retries {
		if !seen[f.ID] {
			if err := e.DB.DropRetry(f.ID); err != nil {
				return err
			}
		}
	}
	items, err := e.DB.Items(w.ID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if seen[it.ID] {
			continue
		}
		if cur, err := e.DB.Item(it.ID); err != nil || cur == nil {
			continue // already gone with its folder
		}
		if err := e.remove(w, it); err != nil {
			return err
		}
	}
	w.Cursor = head
	return e.DB.PutWorkspace(w)
}

// place resolves where an item named name under parent goes locally. It returns skip, with a
// reason when the user should hear of it, if the item must not be synced here.
func (e *Engine) place(w Workspace, id, parent, name string) (rel string, skip bool, why string, err error) {
	// A name the server should never have sent gets a placeholder path that cannot touch
	// disk or collide with a real item.
	refused := ".kydrive-refused-" + id
	if !serverName(name) {
		return refused, true, fmt.Sprintf("the server sent the name %q, which is not a valid file name", name), nil
	}
	dir := ""
	if parent != "" {
		p, err := e.DB.Item(parent)
		if err != nil {
			return "", false, "", err
		}
		if p == nil || p.Workspace != w.ID || p.Kind != "folder" {
			return "", false, "", errSnapshot
		}
		if p.State == "skipped" {
			return path.Join(p.Path, name), true, "", nil
		}
		dir = p.Path
	}
	rel = path.Join(dir, name)
	switch {
	case ignored(name):
		return rel, true, "", nil
	case e.WinNames && !windowsName(name):
		return rel, true, "this name is not valid on Windows", nil
	case e.CaseFold:
		sib, err := e.DB.Siblings(w.ID, parent)
		if err != nil {
			return "", false, "", err
		}
		if slices.ContainsFunc(sib, func(s Item) bool { return s.ID != id && s.State != "skipped" && strings.EqualFold(s.Name, name) }) {
			return rel, true, "another item here differs only in case", nil
		}
	}
	if _, err := e.abs(w, rel); err != nil {
		return refused, true, fmt.Sprintf("%s would be written outside the workspace folder", rel), nil
	}
	return rel, false, "", nil
}

func (e *Engine) skip(w Workspace, it Item, why string) error {
	old, err := e.DB.Item(it.ID)
	if err != nil {
		return err
	}
	if why != "" && (old == nil || old.State != "skipped") {
		if err := e.DB.AddIssue(w.ID, it.Path, "skipped", why); err != nil {
			return err
		}
	}
	it.State = "skipped"
	return e.DB.PutItem(it)
}

// relocate moves the local copy of an item from one path to another. It refuses, leaving
// everything in place, when something already occupies the destination.
func (e *Engine) relocate(w Workspace, from, to string) (bool, error) {
	src, err := e.abs(w, from)
	if err != nil {
		return false, err
	}
	dst, err := e.abs(w, to)
	if err != nil {
		return false, err
	}
	sfi, serr := os.Lstat(src)
	// A case-only rename on a case-insensitive file system finds src at dst.
	if dfi, err := os.Lstat(dst); err == nil && (serr != nil || !os.SameFile(sfi, dfi)) {
		e.warn("Could not move %s to %s: something is already there", from, to)
		return false, nil
	}
	if serr == nil {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return false, err
		}
		if err := os.Rename(src, dst); err != nil {
			return false, err
		}
	}
	return true, e.DB.MoveTree(w.ID, from, to)
}

func (e *Engine) upsertFolder(w Workspace, f api.Folder) error {
	rel, skip, why, err := e.place(w, f.ID, f.Parent, f.Name)
	if err != nil {
		return err
	}
	it := Item{ID: f.ID, Workspace: w.ID, Kind: "folder", Parent: f.Parent, Name: f.Name, Path: rel, State: "synced"}
	if skip {
		return e.skip(w, it, why)
	}
	old, err := e.DB.Item(f.ID)
	if err != nil {
		return err
	}
	if old != nil && old.State != "skipped" && old.Path != rel {
		if ok, err := e.relocate(w, old.Path, rel); !ok || err != nil {
			return err
		}
	}
	p, err := e.abs(w, rel)
	if err != nil {
		return e.skip(w, it, err.Error())
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return e.skip(w, it, "a local file has this name")
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	it.LocalInode = inode(p, fi)
	return e.DB.PutItem(it)
}

func (e *Engine) upsertFile(ctx context.Context, w Workspace, f api.File) error {
	rel, skip, why, err := e.place(w, f.ID, f.Parent, f.Name)
	if err != nil {
		return err
	}
	if skip {
		return e.skip(w, fileItem(w, f, rel), why)
	}
	old, err := e.DB.Item(f.ID)
	if err != nil {
		return err
	}
	if old != nil && old.State == "skipped" {
		old = nil
	}
	if old != nil && old.Path != rel {
		if ok, err := e.relocate(w, old.Path, rel); !ok || err != nil {
			return err
		}
		old.Path = rel
	}
	p, err := e.abs(w, rel)
	if err != nil {
		return e.skip(w, fileItem(w, f, rel), err.Error())
	}
	fi, err := os.Lstat(p)
	if err == nil {
		switch {
		case fi.IsDir():
			return e.skip(w, fileItem(w, f, rel), "a local folder has this name")
		case old != nil && !e.changed(*old, p, fi):
			if old.Revision == f.Revision && old.Digest == f.Digest {
				it := fileItem(w, f, rel)
				it.LocalSize, it.LocalMtime, it.LocalInode = old.LocalSize, old.LocalMtime, old.LocalInode
				return e.DB.PutItem(it)
			}
		case sameContent(p, f.Digest):
			return e.record(w, f, rel, p)
		default:
			// A local edit the server has not seen meets a server version this computer
			// has not seen: keep both.
			if err := e.conflict(w, rel); err != nil {
				return err
			}
		}
	}
	return e.download(ctx, w, f, rel)
}

func fileItem(w Workspace, f api.File, rel string) Item {
	return Item{ID: f.ID, Workspace: w.ID, Kind: "file", Parent: f.Parent, Name: f.Name, Revision: f.Revision, Digest: f.Digest, Size: f.Size, Mtime: f.Mtime, Path: rel, State: "synced"}
}

// record stores f as synced with the local file at p as its copy.
func (e *Engine) record(w Workspace, f api.File, rel, p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	it := fileItem(w, f, rel)
	it.LocalSize, it.LocalMtime, it.LocalInode = fi.Size(), fi.ModTime().UnixNano(), inode(p, fi)
	return e.DB.PutItem(it)
}

// download fetches f into a temporary file beside its destination, checks it against the
// server's digest, and renames it into place only if the destination is still free or still
// the unchanged copy of f.
func (e *Engine) download(ctx context.Context, w Workspace, f api.File, rel string) error {
	p, err := e.abs(w, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".kydrive-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = e.API.Download(ctx, f, tmp)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if s := api.Status(err); s == 403 || s == 404 {
		return e.DB.DropRetry(f.ID) // gone; the feed says so next
	}
	if err != nil {
		e.warn("Download of %s failed: %v", rel, err)
		return e.DB.AddRetry(w.ID, f)
	}
	mtime, err := time.Parse(time.RFC3339, f.Mtime)
	if err != nil {
		mtime = time.Now()
	}
	if err := os.Chtimes(tmp.Name(), mtime, mtime); err != nil {
		return err
	}
	if fi, err := os.Lstat(p); err == nil {
		old, err := e.DB.Item(f.ID)
		if err != nil {
			return err
		}
		if old == nil || old.Path != rel || e.changed(*old, p, fi) {
			return e.DB.AddRetry(w.ID, f) // changed meanwhile; the next pass decides again
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	if err := e.DB.DropRetry(f.ID); err != nil {
		return err
	}
	return e.record(w, f, rel, p)
}

// remove applies a server-side removal: synced copies go, unuploaded edits move to Unsynced/.
func (e *Engine) remove(w Workspace, it Item) error {
	if it.State == "skipped" {
		return e.DB.DropTree(w.ID, it.Path)
	}
	return e.evict(w, it.Path)
}
