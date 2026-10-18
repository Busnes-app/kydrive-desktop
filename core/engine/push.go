package engine

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Busnes-app/kydrive-desktop/core/api"
)

type entry struct {
	rel   string // slash-separated, relative to the workspace folder
	p     string
	fi    fs.FileInfo
	inode uint64
}

func (n entry) dir() bool { return n.fi.IsDir() }

// scan lists the workspace folder's folders and regular files, parents first, skipping
// ignored names and symlinks.
func (e *Engine) scan(w Workspace) ([]entry, error) {
	root := e.dir(w)
	var out []entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if ignored(d.Name()) || !(d.IsDir() || d.Type().IsRegular()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, entry{filepath.ToSlash(rel), p, fi, inode(p, fi)})
		return nil
	})
	return out, err
}

// push sends local changes, parents first: moves, new folders, new files and versions, then
// deletes.
func (e *Engine) push(ctx context.Context, w Workspace) error {
	if _, err := os.Lstat(e.dir(w)); errors.Is(err, fs.ErrNotExist) {
		return e.deletes(ctx, w, nil, true)
	}
	entries, err := e.scan(w)
	if err != nil {
		return err
	}
	gone, err := e.gone(w, entries)
	if err != nil {
		return err
	}
	for _, n := range entries {
		it, err := e.DB.ItemAt(w.ID, n.rel)
		if err != nil {
			return err
		}
		if it == nil {
			if m, ok := gone[n.inode]; ok && n.dir() == (m.Kind == "folder") &&
				(n.dir() || (n.fi.Size() == m.LocalSize && n.fi.ModTime().UnixNano() == m.LocalMtime)) {
				err := e.move(ctx, w, n, m)
				if err == nil {
					if gone, err = e.gone(w, entries); err != nil {
						return err
					}
					continue
				}
				// Not movable: it becomes a create here and a delete below.
				if err := e.itemErr(n.rel, err); err != nil {
					return err
				}
			}
		}
		switch {
		case it != nil && it.State == "skipped":
		case n.dir() && it == nil:
			err = e.addFolder(ctx, w, n)
		case n.dir():
		case it != nil && it.Kind != "file":
		case it != nil && !e.changed(*it, n.p, n.fi):
		case time.Since(n.fi.ModTime()) < e.Settle:
			e.Unsettled = true
		default:
			err = e.upload(ctx, w, n, it)
		}
		if err := e.itemErr(n.rel, err); err != nil {
			return err
		}
	}
	return e.deletes(ctx, w, entries, false)
}

// itemErr keeps one item's failure from stopping the pass, unless the credentials or the
// network failed.
func (e *Engine) itemErr(rel string, err error) error {
	if err == nil {
		return nil
	}
	if api.Status(err) == 401 || errors.As(err, new(*url.Error)) {
		return err
	}
	e.warn("Could not sync %s: %v", rel, err)
	return nil
}

// parentID finds the server folder holding rel; ok is false while that folder is not synced.
func (e *Engine) parentID(w Workspace, rel string) (string, bool, error) {
	dir := path.Dir(rel)
	if dir == "." {
		return "", true, nil
	}
	it, err := e.DB.ItemAt(w.ID, dir)
	if err != nil || it == nil || it.Kind != "folder" || it.State != "synced" {
		return "", false, err
	}
	return it.ID, true, nil
}

// serverName mirrors the server's name rule, so a refused name is listed instead of retried.
func serverName(name string) bool {
	if name == "" || len(name) > 255 || strings.TrimSpace(name) != name || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}

func (e *Engine) placeable(w Workspace, rel string) (string, bool, error) {
	if !serverName(path.Base(rel)) {
		return "", false, e.DB.AddIssue(w.ID, rel, "skipped", "KyDrive does not accept this name")
	}
	return e.parentID(w, rel)
}

func (e *Engine) addFolder(ctx context.Context, w Workspace, n entry) error {
	if ok, err := e.unreported(w, n.rel); !ok || err != nil {
		return err
	}
	pid, ok, err := e.placeable(w, n.rel)
	if !ok || err != nil {
		return err
	}
	f, err := e.API.AddFolder(ctx, w.ID, pid, path.Base(n.rel))
	if api.Status(err) == 409 {
		return nil // the server has one; the next pull adopts this folder
	}
	if err != nil {
		return err
	}
	return e.DB.PutItem(Item{ID: f.ID, Workspace: w.ID, Kind: "folder", Parent: pid, Name: path.Base(n.rel), Path: n.rel, LocalInode: n.inode, State: "synced"})
}

// unreported is false for a path already listed as refused, so it is not listed every pass.
func (e *Engine) unreported(w Workspace, rel string) (bool, error) {
	if serverName(path.Base(rel)) {
		return true, nil
	}
	issues, err := e.DB.Issues()
	for _, i := range issues {
		if i.Workspace == w.ID && i.Path == rel && i.Kind == "skipped" {
			return false, err
		}
	}
	return true, err
}

// upload sends a new file (it nil) or a new version of it. A refusal keeps both copies.
func (e *Engine) upload(ctx context.Context, w Workspace, n entry, it *Item) error {
	if ok, err := e.unreported(w, n.rel); !ok || err != nil {
		return err
	}
	u := api.Upload{}
	if it != nil {
		u.File, u.Revision = it.ID, it.Revision
	} else {
		pid, ok, err := e.placeable(w, n.rel)
		if !ok || err != nil {
			return err
		}
		u.Parent, u.Name = pid, path.Base(n.rel)
	}
	f, err := os.Open(n.p)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	u.Size, u.Mtime = fi.Size(), fi.ModTime()
	sf, err := e.API.Upload(ctx, w.ID, u, f)
	f.Close()
	if api.Status(err) == 409 {
		if it != nil {
			// The server moved on; the next pull downloads its version beside this one.
			it.State = "stale"
			if err := e.DB.PutItem(*it); err != nil {
				return err
			}
		}
		return e.conflict(w, n.rel)
	}
	if err != nil {
		return err
	}
	item := fileItem(w, sf, n.rel)
	item.LocalSize, item.LocalMtime, item.LocalInode = fi.Size(), fi.ModTime().UnixNano(), inode(n.p, fi)
	return e.DB.PutItem(item)
}

// gone maps the inode or file ID of each synced item whose path vanished to that item.
func (e *Engine) gone(w Workspace, entries []entry) (map[uint64]Item, error) {
	items, err := e.DB.Items(w.ID)
	if err != nil {
		return nil, err
	}
	at := map[string]bool{}
	for _, n := range entries {
		at[n.rel] = n.dir()
	}
	out := map[uint64]Item{}
	for _, it := range items {
		if dir, ok := at[it.Path]; it.State == "synced" && it.LocalInode != 0 && (!ok || dir != (it.Kind == "folder")) {
			out[it.LocalInode] = it
		}
	}
	return out, nil
}

// move sends a local move or rename of m to n's path.
func (e *Engine) move(ctx context.Context, w Workspace, n entry, m Item) error {
	pid, ok, err := e.placeable(w, n.rel)
	if err != nil || !ok {
		return cmpErr(err, errors.New("its folder is not synced yet"))
	}
	name := path.Base(n.rel)
	if m.Kind == "folder" {
		_, err = e.API.MoveFolder(ctx, m.ID, pid, name)
	} else {
		var f api.File
		f, err = e.API.MoveFile(ctx, m.ID, m.Revision, w.ID, pid, name)
		m.Revision = f.Revision
	}
	if err != nil {
		return err
	}
	if err := e.DB.MoveTree(w.ID, m.Path, n.rel); err != nil {
		return err
	}
	m.Path, m.Parent, m.Name = n.rel, pid, name
	return e.DB.PutItem(m)
}

// deletes trashes synced items that vanished locally. A pass that would remove more than half
// of a workspace and more than 20 items, or the whole workspace folder, waits for the user.
func (e *Engine) deletes(ctx context.Context, w Workspace, entries []entry, folderGone bool) error {
	at := map[string]bool{}
	for _, n := range entries {
		at[n.rel] = n.dir()
	}
	items, err := e.DB.Items(w.ID)
	if err != nil {
		return err
	}
	var top []Item
	count, total := 0, 0
	for _, it := range items {
		if it.State == "skipped" {
			continue
		}
		total++
		if dir, ok := at[it.Path]; it.State != "synced" || (ok && dir == (it.Kind == "folder")) {
			continue
		}
		count++
		if len(top) == 0 || !strings.HasPrefix(it.Path, top[len(top)-1].Path+"/") {
			top = append(top, it)
		}
	}
	if count == 0 {
		return nil
	}
	if !w.AllowDeletes && (folderGone || (count > 20 && count*2 > total)) {
		if w.Deletes != count {
			e.warn("%d items were deleted from %s on this computer. Confirm to delete them from KyDrive too", count, w.Name)
		}
		w.Deletes = count
		return e.DB.PutWorkspace(w)
	}
	for _, it := range top {
		if it.Kind == "folder" {
			err = e.API.TrashFolder(ctx, it.ID)
		} else {
			err = e.API.TrashFile(ctx, it.ID, it.Revision)
		}
		switch s := api.Status(err); {
		case err == nil || s == 404:
			if err := e.DB.DropTree(w.ID, it.Path); err != nil {
				return err
			}
		case s == 409:
			// Changed on the server since; its version comes back with the next pull.
			it.State = "stale"
			if err := e.DB.PutItem(it); err != nil {
				return err
			}
		case s == 0 || s == 401:
			return err
		default:
			e.warn("KyDrive refused to delete %s: %v", it.Path, err)
			w.Cursor = -1 // a fresh snapshot restores it
		}
	}
	w.Deletes, w.AllowDeletes = 0, false
	return e.DB.PutWorkspace(w)
}
