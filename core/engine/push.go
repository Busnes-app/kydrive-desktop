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
		if errors.Is(err, fs.ErrNotExist) {
			return nil // gone since the folder was read
		}
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
				if err := e.itemErr(w, n.rel, err); err != nil {
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
			var again bool
			if again, err = e.rewritten(*it, n); err == nil && again {
				err = e.upload(ctx, w, n, it)
			}
		case unsettled(n.fi.ModTime(), e.Settle):
			e.Unsettled = true
		default:
			err = e.upload(ctx, w, n, it)
		}
		if err := e.itemErr(w, n.rel, err); err != nil {
			return err
		}
	}
	return e.deletes(ctx, w, entries, false)
}

// rewritten reports a file written in place keeping its size and mtime (VeraCrypt does so by
// default for its containers): its change time moved and its content is not what synced. A
// change time that moved alone (permissions, a rename) is recorded, as is a first one.
func (e *Engine) rewritten(it Item, n entry) (bool, error) {
	c := ctime(n.p, n.fi)
	if c == it.LocalCtime {
		return false, nil
	}
	if it.LocalCtime != 0 && !sameContent(n.p, it.Digest) {
		return true, nil
	}
	it.LocalCtime = c
	return false, e.DB.PutItem(it)
}

// unsettled reports a file written within settle. A future mtime (an archive from another
// time zone, a skewed clock) says nothing about writing, so it never waits.
func unsettled(mtime time.Time, settle time.Duration) bool {
	age := time.Since(mtime)
	return age >= 0 && age < settle
}

// itemErr keeps one item's failure from stopping the pass, unless the credentials or the
// network failed.
func (e *Engine) itemErr(w Workspace, rel string, err error) error {
	key := w.ID + "/" + rel
	if err == nil {
		e.Memory.clear(key)
		return nil
	}
	if api.Status(err) == 401 || errors.As(err, new(*url.Error)) {
		return err
	}
	if e.Memory.once(key) { // announced again only after it once succeeds
		e.warn("Could not sync %s: %v", rel, err)
	}
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
	item.LocalSize, item.LocalMtime, item.LocalInode, item.LocalCtime = fi.Size(), fi.ModTime().UnixNano(), inode(n.p, fi), ctime(n.p, fi)
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
		dir, ok := at[it.Path]
		if it.State != "synced" || (ok && dir == (it.Kind == "folder")) {
			continue
		}
		if !ok && !folderGone {
			// Not listed but not missing: a link or special file took its place (a folder moved
			// to another disk, a link left behind), or a link above it. Not a local delete.
			p, err := e.abs(w, it.Path)
			if err != nil {
				continue
			}
			// A regular file or folder answering here is a case variant on a case-insensitive
			// disk (Budget.xlsx deleted, budget.xlsx saved): that is a delete.
			if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() && !fi.IsDir() {
				if err := e.DB.AddIssueOnce(w.ID, it.Path, "skipped", "a link or special file is in its place; not synced"); err != nil {
					return err
				}
				continue
			}
		}
		count++
		if len(top) == 0 || !strings.HasPrefix(it.Path, top[len(top)-1].Path+"/") {
			top = append(top, it)
		}
	}
	if count == 0 {
		if w.Deletes == 0 && !w.AllowDeletes {
			return nil
		}
		// What was held came back: the guard applies afresh to the next deletes.
		w.Deletes, w.AllowDeletes = 0, false
		return e.DB.PutWorkspace(w)
	}
	// Once held, deletes stay held until confirmed or gone: files added on the server since
	// must not dilute them below the threshold.
	if !w.AllowDeletes && (folderGone || w.Deletes > 0 || (count > 20 && count*2 > total)) {
		if w.Deletes != count {
			e.warn("%d items were deleted from %s on this computer. Confirm to delete them from KyDrive too", count, w.Name)
		}
		w.Deletes = count
		return e.DB.PutWorkspace(w)
	}
	busy, err := e.changedUnder(ctx, w, items, top)
	if err != nil {
		return err
	}
	for _, it := range top {
		if busy[it.ID] {
			continue
		}
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
	// A confirmed delete that waits stays confirmed.
	w.Deletes, w.AllowDeletes = 0, w.AllowDeletes && len(busy) > 0
	return e.DB.PutWorkspace(w)
}

// changedUnder lists the folders about to be trashed that changed on the server since the
// pull: the server trashes a folder with everything in it, so a file a colleague added during
// the push would go too. Such a folder waits for the next pass, whose pull brings the change.
func (e *Engine) changedUnder(ctx context.Context, w Workspace, items, top []Item) (map[string]bool, error) {
	under := map[string]string{} // an item, or a folder created since, -> the folder to trash
	for _, t := range top {
		if t.Kind != "folder" {
			continue
		}
		under[t.ID] = t.ID
		for _, it := range items {
			if strings.HasPrefix(it.Path, t.Path+"/") {
				under[it.ID] = t.ID
			}
		}
	}
	busy := map[string]bool{}
	for after := w.Cursor; len(under) > 0; {
		ch, err := e.API.Changes(ctx, w.ID, after)
		if api.Status(err) == 410 {
			for _, t := range under {
				busy[t] = true // the next pull snapshots
			}
			break
		}
		if err != nil {
			return nil, err
		}
		for _, c := range ch.Changes {
			parent := ""
			if c.File != nil {
				parent = c.File.Parent
			} else if c.Folder != nil {
				parent = c.Folder.Parent
			}
			t, ok := under[c.ID]
			if !ok {
				t, ok = under[parent]
			}
			if ok {
				busy[t] = true
				under[c.ID] = t
			}
		}
		if !ch.More {
			break
		}
		after = ch.Next
	}
	return busy, nil
}
