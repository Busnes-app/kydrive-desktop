package engine

import (
	"context"
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
	"syscall"
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
	var tried []string
	for _, f := range retries {
		if err := e.canceled(); err != nil {
			return err
		}
		if !e.Memory.due(w.ID, f.ID) {
			continue
		}
		tried = append(tried, f.ID)
		var err error
		switch it, _ := e.DB.Item(f.ID); {
		case f.Name == "": // a server removal that could not finish here
			if it == nil || it.Workspace != w.ID {
				err = e.DB.DropRetry(w.ID, f.ID)
			} else {
				err = e.removeLater(w, *it, e.remove(w, *it))
			}
		case f.Digest != "":
			err = e.retryLater(ctx, w, f, e.upsertFile(ctx, w, f))
		case it != nil && it.Kind == "folder":
			err = e.upsertFolder(w, api.Folder{ID: f.ID, Workspace: f.Workspace, Parent: f.Parent, Name: f.Name})
			if errors.Is(err, errSnapshot) {
				return e.snapshot(ctx, w)
			}
		default: // a folder move whose folder is gone here
			err = e.DB.DropRetry(w.ID, f.ID)
		}
		if err != nil && !errors.Is(err, errSnapshot) {
			// A pass the network ended still backs this retry off.
			return errors.Join(err, e.Memory.tried(e.DB, w.ID, tried))
		}
	}
	if err := e.Memory.tried(e.DB, w.ID, tried); err != nil {
		return err
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
			if err := e.canceled(); err != nil {
				return err
			}
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
		if err := e.DB.DropRetry(w.ID, c.ID); err != nil {
			return err
		}
		it, err := e.DB.Item(c.ID)
		if err != nil || it == nil || it.Workspace != w.ID {
			return err
		}
		return e.removeLater(w, *it, e.remove(w, *it))
	case c.Kind == "folder" && c.Folder != nil:
		it, err := e.DB.Item(c.ID)
		if err != nil || it == nil {
			return cmpErr(err, errSnapshot)
		}
		if e.echo(w, it, c.Folder.Parent, c.Folder.Name, 0, "") {
			return nil
		}
		return e.upsertFolder(w, *c.Folder)
	case c.File != nil:
		if it, err := e.DB.Item(c.ID); err != nil || e.echo(w, it, c.File.Parent, c.File.Name, c.File.Revision, c.File.Digest) {
			return err
		}
		return e.retryLater(ctx, w, *c.File, e.upsertFile(ctx, w, *c.File))
	}
	return nil
}

// echo reports a feed entry the record already holds while the local copy is missing: the
// server has nothing new (often this computer's own upload or folder coming back), so the
// missing copy is a local move or delete for the push to send. Pulling it back would undo it.
// A snapshot still restores missing copies.
func (e *Engine) echo(w Workspace, old *Item, parent, name string, rev int64, digest string) bool {
	if old == nil || old.Workspace != w.ID || old.State != "synced" || old.Parent != parent || old.Name != name ||
		old.Revision != rev || old.Digest != digest {
		return false
	}
	p, err := e.abs(w, old.Path)
	return err == nil && missing(p)
}

// removeLater keeps a server removal that cannot finish here (a file open elsewhere, no
// permission) from stopping the pass: it waits in retries, marked by an empty name.
func (e *Engine) removeLater(w Workspace, it Item, err error) error {
	if e.canceled() != nil {
		return e.canceled()
	}
	if err == nil {
		return e.DB.DropRetry(it.Workspace, it.ID)
	}
	fresh, aerr := e.DB.AddRetry(w.ID, api.File{ID: it.ID, Workspace: w.ID})
	if fresh {
		e.warn("Could not remove %s, which was deleted on the server: %v", it.Path, err)
	}
	return aerr
}

// downloadOutage identifies failures needing a shared-API reachability probe.
// A digest mismatch remains a per-file retry (the server supplied all its bytes).
func downloadOutage(err error) bool {
	return errors.As(err, new(*url.Error)) || api.Status(err) >= 500 || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

// retryLater keeps one file's local failure (a file open elsewhere, no permission, a name the
// file system refuses) from stopping the pass: the file waits in retries for the next pull.
// Server, network and cancellation errors still end the pass.
func (e *Engine) retryLater(ctx context.Context, w Workspace, f api.File, err error) error {
	if err == nil || errors.Is(err, errSnapshot) || ctx.Err() != nil || api.Status(err) != 0 || downloadOutage(err) {
		return err
	}
	fresh, aerr := e.DB.AddRetry(w.ID, f)
	if fresh {
		e.warn("Could not sync %s: %v", f.Name, err)
	}
	return aerr
}

func cmpErr(err, otherwise error) error {
	if err != nil {
		return err
	}
	return otherwise
}

// snapshot enumerates the whole workspace, applies it, removes what is gone and continues
// the feed from the snapshot's head. Only a snapshot asked for to restore (cursor -1: a new
// binding, an evict, refused deletes) brings back missing copies; one the feed forces (an
// unknown folder, 410) leaves them, as echo does, for the push to send as moves or deletes.
// A file waiting out a failed download's backoff is not fetched early.
func (e *Engine) snapshot(ctx context.Context, w Workspace) error {
	restore := w.Cursor < 0
	if err := os.MkdirAll(e.dir(w), 0o755); err != nil {
		return err
	}
	retries, err := e.DB.Retries(w.ID)
	if err != nil {
		return err
	}
	waiting := map[string]api.File{}
	for _, f := range retries {
		if err := e.canceled(); err != nil {
			return err
		}
		waiting[f.ID] = f
	}
	// held is a feed echo: the record has it and its local copy is missing.
	held := func(id, parent, name string, rev int64, digest string) (bool, error) {
		if restore {
			return false, nil
		}
		it, err := e.DB.Item(id)
		return err == nil && e.echo(w, it, parent, name, rev, digest), err
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
			if err := e.canceled(); err != nil {
				return err
			}
			if !seen[f.Parent] {
				later = append(later, f)
				continue
			}
			seen[f.ID] = true
			if h, err := held(f.ID, f.Parent, f.Name, 0, ""); err != nil || h {
				if err != nil {
					return err
				}
				continue
			}
			if err := e.upsertFolder(w, f); err != nil && !errors.Is(err, errSnapshot) {
				return err
			}
		}
		if len(later) == len(folders) {
			break // orphans: their parents are not in the snapshot
		}
		folders = later
	}
	slices.SortFunc(files, func(a, b api.File) int { return strings.Compare(a.Name, b.Name) })
	var tried []string
	for _, f := range files {
		if err := e.canceled(); err != nil {
			return err
		}
		if !seen[f.Parent] {
			continue
		}
		seen[f.ID] = true
		if r, ok := waiting[f.ID]; ok && r.Revision == f.Revision && r.Digest == f.Digest {
			if !e.Memory.due(w.ID, f.ID) {
				// Still backing off; it may have moved since, so the retry takes its new place.
				if _, err := e.DB.AddRetry(w.ID, f); err != nil {
					return err
				}
				continue
			}
			tried = append(tried, f.ID)
		}
		if h, err := held(f.ID, f.Parent, f.Name, f.Revision, f.Digest); err != nil || h {
			if err != nil {
				return err
			}
			continue
		}
		if err := e.retryLater(ctx, w, f, e.upsertFile(ctx, w, f)); err != nil && !errors.Is(err, errSnapshot) {
			return errors.Join(err, e.Memory.tried(e.DB, w.ID, tried))
		}
	}
	if err := e.Memory.tried(e.DB, w.ID, tried); err != nil {
		return err
	}
	if retries, err = e.DB.Retries(w.ID); err != nil {
		return err
	}
	for _, f := range retries {
		if err := e.canceled(); err != nil {
			return err
		}
		if !seen[f.ID] {
			if err := e.DB.DropRetry(w.ID, f.ID); err != nil {
				return err
			}
		}
	}
	items, err := e.DB.Items(w.ID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := e.canceled(); err != nil {
			return err
		}
		if seen[it.ID] {
			continue
		}
		if cur, err := e.DB.Item(it.ID); err != nil || cur == nil {
			continue // already gone with its folder
		}
		if err := e.removeLater(w, it, e.remove(w, it)); err != nil {
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
		// The very same name is another item's record the server renamed or removed (occupied
		// waits for it), not a case twin.
		if slices.ContainsFunc(sib, func(s Item) bool {
			return s.ID != id && s.State != "skipped" && s.Name != name && strings.EqualFold(s.Name, name)
		}) {
			return rel, true, caseTwin, nil
		}
	}
	if e.WinNames && shortName(name) {
		// Windows opens REPORT~1.TXT as Report for 2026.txt: a new item with such a name is not
		// placed over something already here.
		if p, err := e.abs(w, rel); err == nil && !missing(p) {
			if a, err := e.alias(w, id, rel, p); err != nil || a {
				return rel, true, "on Windows this name can stand for another item here", err
			}
		}
	}
	if _, err := e.abs(w, rel); errors.Is(err, errLinked) {
		// The folder may come back: the item waits in retries, its record untouched.
		return rel, false, "", err
	} else if err != nil {
		return refused, true, fmt.Sprintf("%s would be written outside the workspace folder", rel), nil
	}
	return rel, false, "", nil
}

// alias reports that p, a short-shaped name, opens neither a record at rel (a reused name,
// which occupied handles) nor this item's own copy (a case-only rename).
func (e *Engine) alias(w Workspace, id, rel, p string) (bool, error) {
	if o, err := e.DB.ItemAt(w.ID, rel); err != nil || o != nil && o.State != "skipped" {
		return false, err
	}
	it, err := e.DB.Item(id)
	if err != nil || it == nil || it.Workspace != w.ID || it.State == "skipped" {
		return err == nil, err
	}
	own, err := e.abs(w, it.Path)
	if err != nil {
		return true, nil
	}
	a, aerr := os.Lstat(own)
	b, berr := os.Lstat(p)
	return aerr != nil || berr != nil || !os.SameFile(a, b), nil
}

// caseTwin is why an item is skipped beside a sibling whose name differs only in case.
const caseTwin = "another item here differs only in case"

// skipPlaced records an item this computer skips. A case twin waits in retries, since the
// other item may be on its way out (removed or renamed later in the pull); a synced copy of
// it stays where it is, recorded, until then. Any other item synced elsewhere here (the
// server renamed it into a name this computer cannot hold) leaves its old place as a server
// removal would; its copy would otherwise upload again as a new item.
func (e *Engine) skipPlaced(w Workspace, it Item, why string, f api.File) error {
	if err := e.canceled(); err != nil {
		return err
	}
	old, err := e.DB.Item(it.ID)
	if err != nil {
		return err
	}
	moved := old != nil && old.Workspace == w.ID && old.State != "skipped" && old.Path != it.Path
	if why == caseTwin {
		if _, err := e.DB.AddRetry(w.ID, f); err != nil {
			return err
		}
		if moved {
			return e.DB.AddIssueOnce(w.ID, it.Path, "skipped", why)
		}
	} else if moved {
		if err := e.remove(w, *old); err != nil {
			return e.removeLater(w, *old, err)
		}
	}
	return e.skip(w, it, why)
}

func (e *Engine) skip(w Workspace, it Item, why string) error {
	if err := e.canceled(); err != nil {
		return err
	}
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
	// Another item's record holds the name (the server replaced a file with a folder of that
	// name, or the reverse, its removal later in the pull): this one takes a placeholder path
	// that never touches disk, so that record and its removal are not lost.
	if o, err := e.DB.ItemAt(w.ID, it.Path); err != nil {
		return err
	} else if o != nil && o.ID != it.ID {
		it.Path = "\x01blocked-" + it.ID // no server name holds a control character
	}
	return e.DB.PutItem(it)
}

// occupied reports that rel is still recorded as another file, which the server moved or
// removed: its own change, later in this pull or the next, frees the name. When that file
// itself waits in retries to move (two names swapped), an unchanged copy of it is evicted
// instead and downloads again in its new place; a changed one is announced and waited for.
func (e *Engine) occupied(w Workspace, id, rel string) (bool, error) {
	o, err := e.DB.ItemAt(w.ID, rel)
	if err != nil || o == nil || o.ID == id || o.State == "skipped" || o.Kind != "file" {
		return false, err
	}
	retries, err := e.DB.Retries(w.ID)
	if err != nil || !slices.ContainsFunc(retries, func(f api.File) bool {
		// A retry that puts it elsewhere (compared by path: an upload records the server's
		// new name before the local copy moves).
		dir := "."
		if p, _ := e.DB.Item(f.Parent); f.Parent != "" && p != nil {
			dir = p.Path
		}
		return f.ID == o.ID && f.Name != "" && path.Join(dir, f.Name) != o.Path
	}) {
		return true, err
	}
	p, err := e.abs(w, rel)
	if err != nil {
		return true, err
	}
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() || e.changed(*o, p, fi) || !e.sameContent(p, o.Digest) {
		return true, fmt.Errorf("%s has changes here and waits to move first", rel)
	}
	return false, e.evict(w, rel)
}

// aside moves the folder recorded at rel to a placeholder name when that folder itself waits
// in retries to move elsewhere (two folders swapped names on the server). Its retry then
// moves it on from there, so the swap converges; nothing in it changes.
func (e *Engine) aside(w Workspace, rel string) (bool, error) {
	o, err := e.DB.ItemAt(w.ID, rel)
	if err != nil || o == nil || o.Kind != "folder" || o.State == "skipped" {
		return false, err
	}
	retries, err := e.DB.Retries(w.ID)
	if err != nil || !slices.ContainsFunc(retries, func(f api.File) bool { return f.ID == o.ID && f.Name != "" && f.Digest == "" }) {
		return false, err
	}
	return e.relocate(w, rel, ".kydrive-move-"+o.ID)
}

// relocate moves the local copy of an item from one path to another. It refuses, leaving
// everything in place, when something already occupies the destination.
func (e *Engine) relocate(w Workspace, from, to string) (bool, error) {
	if err := e.canceled(); err != nil {
		return false, err
	}
	src, err := e.abs(w, from)
	if err != nil {
		return false, err
	}
	dst, err := e.abs(w, to)
	if err != nil {
		return false, err
	}
	// Another item's record holds the destination until its own change moves it.
	if o, err := e.DB.ItemAt(w.ID, to); err != nil || o != nil && o.State != "skipped" {
		return false, err
	}
	sfi, serr := os.Lstat(src)
	// A case-only rename on a case-insensitive file system finds src at dst.
	if dfi, err := os.Lstat(dst); err == nil && (serr != nil || !os.SameFile(sfi, dfi)) {
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
	if err := e.canceled(); err != nil {
		return err
	}
	rel, skip, why, err := e.place(w, f.ID, f.Parent, f.Name)
	if errors.Is(err, errLinked) {
		// A recorded folder waits in retries for the link to go; a new one is not placed.
		if old, err := e.DB.Item(f.ID); err != nil || old != nil && old.Workspace == w.ID && old.State != "skipped" {
			if err == nil {
				_, err = e.DB.AddRetry(w.ID, api.File{ID: f.ID, Workspace: f.Workspace, Parent: f.Parent, Name: f.Name})
			}
			return err
		}
		if _, err := e.DB.AddRetry(w.ID, api.File{ID: f.ID, Workspace: f.Workspace, Parent: f.Parent, Name: f.Name}); err != nil {
			return err
		}
		return e.skip(w, Item{ID: f.ID, Workspace: w.ID, Kind: "folder", Parent: f.Parent, Name: f.Name, Path: ".kydrive-refused-" + f.ID},
			rel+" is under a link")
	}
	if err != nil {
		return err
	}
	it := Item{ID: f.ID, Workspace: w.ID, Kind: "folder", Parent: f.Parent, Name: f.Name, Path: rel, State: "synced"}
	if skip {
		return e.skipPlaced(w, it, why, api.File{ID: f.ID, Workspace: f.Workspace, Parent: f.Parent, Name: f.Name})
	}
	old, err := e.DB.Item(f.ID)
	if err != nil {
		return err
	}
	if old != nil && old.State == "synced" {
		// A Finder alias in its place (moved to another disk): it and its files' records
		// wait, as for a link, wherever the server puts it.
		if src, err := e.abs(w, old.Path); err == nil && finderAlias(src) {
			_, err := e.DB.AddRetry(w.ID, api.File{ID: f.ID, Workspace: f.Workspace, Parent: f.Parent, Name: f.Name})
			return err
		}
	}
	if old != nil && old.State != "skipped" && old.Path != rel {
		ok, err := e.relocate(w, old.Path, rel)
		if err == nil && !ok {
			// Two folders swapped names: the one holding this name moves aside first.
			if ok, err = e.aside(w, rel); ok {
				ok, err = e.relocate(w, old.Path, rel)
			}
		}
		if !ok {
			// Refused locally (occupied, a file open inside, no permission): the folder stays
			// where it is and waits in retries, like a file. It must not stop the pass.
			fresh, aerr := e.DB.AddRetry(w.ID, api.File{ID: f.ID, Workspace: f.Workspace, Parent: f.Parent, Name: f.Name})
			if fresh {
				e.warn("Could not move %s to %s: %v", old.Path, rel, cmpErr(err, errors.New("something is already there")))
			}
			return aerr
		}
		if err != nil {
			return err
		}
	}
	p, err := e.abs(w, rel)
	if err != nil {
		return e.skip(w, it, err.Error())
	}
	err = os.MkdirAll(p, 0o755)
	fi, lerr := os.Lstat(p)
	if err != nil || lerr != nil || !fi.IsDir() {
		// A file or a link has the name (MkdirAll follows a link to a folder). Tried again each
		// pull; the folder syncs once it is moved away.
		if _, err := e.DB.AddRetry(w.ID, api.File{ID: f.ID, Workspace: f.Workspace, Parent: f.Parent, Name: f.Name}); err != nil {
			return err
		}
		if old != nil && old.State == "synced" && old.Path == rel && lerr == nil && fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return nil // this folder, moved elsewhere with a link left: its record and its files' wait
		}
		return e.skip(w, it, "a local file or link has this name")
	}
	it.LocalInode = inode(p, fi)
	if old != nil && old.State == "skipped" {
		// No longer blocked: what was skipped below it comes with a snapshot.
		return cmpErr(e.keep(it), errSnapshot)
	}
	return e.keep(it)
}

func (e *Engine) upsertFile(ctx context.Context, w Workspace, f api.File) error {
	if err := e.canceled(); err != nil {
		return err
	}
	rel, skip, why, err := e.place(w, f.ID, f.Parent, f.Name)
	if err != nil {
		return err
	}
	if skip {
		return e.skipPlaced(w, fileItem(w, f, rel), why, f)
	}
	old, err := e.DB.Item(f.ID)
	if err != nil {
		return err
	}
	// A record from another workspace (a file moved between two chosen ones) names a path in
	// that workspace, not this one: the file downloads fresh here.
	if old != nil && old.Workspace != w.ID {
		if source, err := e.workspace(old.Workspace); err != nil {
			return err
		} else if source.ID != "" {
			if err := e.remove(source, *old); err != nil {
				return err
			}
		}
		old = nil
	}
	if old != nil && old.State == "skipped" {
		old = nil
	}
	if wait, err := e.occupied(w, f.ID, rel); err != nil || wait {
		if err == nil {
			_, err = e.DB.AddRetry(w.ID, f) // the other item's own change frees the name
		}
		return err // announced once by retryLater
	}
	if old != nil && old.Path != rel {
		ok, err := e.relocate(w, old.Path, rel)
		if err == nil && !ok {
			err = fmt.Errorf("could not move it from %s: something is already there", old.Path)
		}
		if err != nil {
			return err // retried on the next pull
		}
		old.Path = rel
	}
	p, err := e.abs(w, rel)
	if err != nil {
		return e.skip(w, fileItem(w, f, rel), err.Error())
	}
	fi, err := os.Lstat(p)
	if err == nil {
		id, ct := inode(p, fi), ctime(p, fi)
		switch {
		case !fi.Mode().IsRegular():
			// A folder, link or special file (never opened: a FIFO would block) has the name.
			// Tried again each pull; the file syncs once it is moved away.
			if _, err := e.DB.AddRetry(w.ID, f); err != nil {
				return err
			}
			return e.skip(w, fileItem(w, f, rel), "a local folder or link has this name")
		case old != nil && old.State == "synced" && old.Revision == f.Revision && old.Digest == f.Digest:
			// The server has nothing newer (often this computer's own upload coming back); a
			// local change since is a pending upload, not a conflict.
			it := fileItem(w, f, rel)
			it.LocalSize, it.LocalMtime, it.LocalInode, it.LocalCtime = old.LocalSize, old.LocalMtime, old.LocalInode, old.LocalCtime
			return e.keep(it)
		case old != nil && old.State == "synced" && e.sameContent(p, old.Digest):
			// The synced copy, by content: an edit written in place that kept the size and
			// mtime is not replaced, and a copy back from another disk (new file IDs) is.
		case e.sameContent(p, f.Digest):
			// fi and the file ID were read before hashing: a change during the hash shows later.
			return e.record(w, f, rel, fi, id, ct)
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

// record stores f as synced with the local file, as fi and its file ID found it, as its copy.
func (e *Engine) record(w Workspace, f api.File, rel string, fi fs.FileInfo, id uint64, ct int64) error {
	it := fileItem(w, f, rel)
	it.LocalSize, it.LocalMtime, it.LocalInode, it.LocalCtime = fi.Size(), fi.ModTime().UnixNano(), id, ct
	return e.keep(it)
}

// keep stores an item that needs no download or move, ending any retry of it.
func (e *Engine) keep(it Item) error {
	if err := e.DB.DropRetry(it.Workspace, it.ID); err != nil {
		return err
	}
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
	// Only KyDrive's own answer (a JSON error) means gone; the feed says so next.
	if ae := (*api.Error)(nil); errors.As(err, &ae) && (ae.Status == 403 || ae.Status == 404) && ae.Code != "" {
		return e.DB.DropRetry(w.ID, f.ID)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		fresh, aerr := e.DB.AddRetry(w.ID, f)
		if downloadOutage(err) {
			// A broken single download must not starve unrelated sync. Probe the shared
			// API before treating a transport-shaped failure as a whole-server outage.
			if _, probe := e.API.Workspaces(ctx); probe != nil {
				return errors.Join(probe, aerr)
			}
			if e.Memory.once(w.ID + "/download-network") {
				e.warn("Some files of %s could not be downloaded; KyDrive will retry: %v", w.Name, err)
			}
			return aerr
		}
		if fresh {
			e.warn("Download of %s failed: %v", rel, err)
		}
		return aerr
	}
	e.Memory.clear(w.ID + "/download-network")
	mtime, err := time.Parse(time.RFC3339, f.Mtime)
	if err != nil {
		mtime = time.Now()
	}
	if err := os.Chtimes(tmp.Name(), mtime, mtime); err != nil {
		return err
	}
	// The verified copy as it is now; the rename below keeps its size, mtime and file ID.
	tfi, err := os.Lstat(tmp.Name())
	if err != nil {
		return err
	}
	id, ct := inode(tmp.Name(), tfi), ctime(tmp.Name(), tfi)
	if fi, err := os.Lstat(p); err == nil {
		old, err := e.DB.Item(f.ID)
		if err != nil {
			return err
		}
		// Never opened unless a regular file: a FIFO put there would block the hash.
		if old == nil || old.Path != rel || old.State != "synced" || !fi.Mode().IsRegular() || !e.sameContent(p, old.Digest) {
			_, err := e.DB.AddRetry(w.ID, f) // changed meanwhile; the next pass decides again
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	return e.record(w, f, rel, tfi, id, ct)
}

// remove applies a server-side removal: synced copies go, unuploaded edits move to Unsynced/.
func (e *Engine) remove(w Workspace, it Item) error {
	if it.State == "skipped" {
		return e.DB.DropTree(w.ID, it.Path)
	}
	return e.evict(w, it.Path)
}
