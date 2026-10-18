package engine

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"

	_ "modernc.org/sqlite"
)

// Item is one server folder or file as last synced.
type Item struct {
	ID, Workspace, Kind, Parent, Name string
	Revision                          int64
	Digest                            string
	Size                              int64
	Mtime                             string // server mtime, RFC 3339
	Path                              string // relative to the workspace folder, slash-separated
	LocalSize                         int64
	LocalMtime                        int64 // UnixNano
	LocalInode                        uint64
	State                             string // synced, stale (local copy not ours) or skipped
}

// Workspace is a chosen workspace and the folder under the sync root that holds it.
type Workspace struct {
	ID, Name, Dir string
	Cursor        int64 // change feed position; -1 means snapshot first
	Deletes       int   // local deletes held back by the large-delete guard
	AllowDeletes  bool  // the user confirmed the held deletes
}

type Issue struct {
	ID                            int64
	Workspace, Path, Kind, Detail string
	Created                       int64
}

type Activity struct {
	ID      int64
	Created int64
	Level   string // info or warn
	Message string
}

type DB struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS config(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workspaces(id TEXT PRIMARY KEY, name TEXT NOT NULL, dir TEXT NOT NULL UNIQUE, cursor INTEGER NOT NULL, deletes INTEGER NOT NULL DEFAULT 0, allow_deletes INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS items(id TEXT PRIMARY KEY, workspace TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('file','folder')), parent TEXT NOT NULL, name TEXT NOT NULL,
 revision INTEGER NOT NULL, digest TEXT NOT NULL, size INTEGER NOT NULL, mtime TEXT NOT NULL, path TEXT NOT NULL,
 local_size INTEGER NOT NULL, local_mtime INTEGER NOT NULL, local_inode INTEGER NOT NULL, state TEXT NOT NULL, UNIQUE(workspace, path));
CREATE INDEX IF NOT EXISTS items_parent ON items(workspace, parent);
CREATE TABLE IF NOT EXISTS issues(id INTEGER PRIMARY KEY, workspace TEXT NOT NULL, path TEXT NOT NULL, kind TEXT NOT NULL, detail TEXT NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS retries(id TEXT PRIMARY KEY, workspace TEXT NOT NULL, file TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS activity(id INTEGER PRIMARY KEY, created INTEGER NOT NULL, level TEXT NOT NULL, message TEXT NOT NULL);`

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) Get(key string) string {
	var v string
	d.db.QueryRow(`SELECT value FROM config WHERE key=?`, key).Scan(&v)
	return v
}

func (d *DB) Set(key, value string) error {
	_, err := d.db.Exec(`INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (d *DB) Workspaces() ([]Workspace, error) {
	rows, err := d.db.Query(`SELECT id,name,dir,cursor,deletes,allow_deletes FROM workspaces ORDER BY dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Dir, &w.Cursor, &w.Deletes, &w.AllowDeletes); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (d *DB) PutWorkspace(w Workspace) error {
	_, err := d.db.Exec(`INSERT INTO workspaces(id,name,dir,cursor,deletes,allow_deletes) VALUES(?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET name=excluded.name, dir=excluded.dir, cursor=excluded.cursor, deletes=excluded.deletes, allow_deletes=excluded.allow_deletes`,
		w.ID, w.Name, w.Dir, w.Cursor, w.Deletes, w.AllowDeletes)
	return err
}

// DropWorkspace forgets a workspace and every item in it.
func (d *DB) DropWorkspace(id string) error {
	for _, q := range []string{`DELETE FROM items WHERE workspace=?`, `DELETE FROM retries WHERE workspace=?`} {
		if _, err := d.db.Exec(q, id); err != nil {
			return err
		}
	}
	_, err := d.db.Exec(`DELETE FROM workspaces WHERE id=?`, id)
	return err
}

const itemCols = `id,workspace,kind,parent,name,revision,digest,size,mtime,path,local_size,local_mtime,local_inode,state`

func scanItems(rows *sql.Rows, err error) ([]Item, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.ID, &i.Workspace, &i.Kind, &i.Parent, &i.Name, &i.Revision, &i.Digest, &i.Size, &i.Mtime, &i.Path, &i.LocalSize, &i.LocalMtime, &i.LocalInode, &i.State); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func one(items []Item, err error) (*Item, error) {
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}

func (d *DB) Item(id string) (*Item, error) {
	return one(scanItems(d.db.Query(`SELECT `+itemCols+` FROM items WHERE id=?`, id)))
}

func (d *DB) ItemAt(workspace, path string) (*Item, error) {
	return one(scanItems(d.db.Query(`SELECT `+itemCols+` FROM items WHERE workspace=? AND path=?`, workspace, path)))
}

// Items lists a workspace's items, parents before children.
func (d *DB) Items(workspace string) ([]Item, error) {
	return scanItems(d.db.Query(`SELECT `+itemCols+` FROM items WHERE workspace=? ORDER BY path`, workspace))
}

func (d *DB) Siblings(workspace, parent string) ([]Item, error) {
	return scanItems(d.db.Query(`SELECT `+itemCols+` FROM items WHERE workspace=? AND parent=?`, workspace, parent))
}

func (d *DB) PutItem(i Item) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO items(`+itemCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		i.ID, i.Workspace, i.Kind, i.Parent, i.Name, i.Revision, i.Digest, i.Size, i.Mtime, i.Path, i.LocalSize, i.LocalMtime, i.LocalInode, i.State)
	return err
}

const under = `workspace=? AND (path=? OR substr(path,1,length(?)+1)=?||'/')`

// DropTree forgets the item at path and everything below it.
func (d *DB) DropTree(workspace, path string) error {
	_, err := d.db.Exec(`DELETE FROM items WHERE `+under, workspace, path, path, path)
	return err
}

// Tree lists the item at path and everything below it, parents first.
func (d *DB) Tree(workspace, path string) ([]Item, error) {
	return scanItems(d.db.Query(`SELECT `+itemCols+` FROM items WHERE `+under+` ORDER BY path`, workspace, path, path, path))
}

// MoveTree rewrites the paths of the item at from and everything below it.
func (d *DB) MoveTree(workspace, from, to string) error {
	_, err := d.db.Exec(`UPDATE items SET path=?||substr(path,length(?)+1) WHERE `+under, to, from, workspace, from, from, from)
	return err
}

// AddRetry remembers a download that failed, so the next pull tries it again.
func (d *DB) AddRetry(workspace string, f api.File) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT OR REPLACE INTO retries(id,workspace,file) VALUES(?,?,?)`, f.ID, workspace, string(b))
	return err
}

func (d *DB) DropRetry(id string) error {
	_, err := d.db.Exec(`DELETE FROM retries WHERE id=?`, id)
	return err
}

func (d *DB) Retries(workspace string) ([]api.File, error) {
	rows, err := d.db.Query(`SELECT file FROM retries WHERE workspace=?`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.File
	for rows.Next() {
		var s string
		var f api.File
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(s), &f) == nil {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}

func (d *DB) AddIssue(workspace, path, kind, detail string) error {
	_, err := d.db.Exec(`INSERT INTO issues(workspace,path,kind,detail,created) VALUES(?,?,?,?,?)`, workspace, path, kind, detail, time.Now().Unix())
	return err
}

func (d *DB) Issues() ([]Issue, error) {
	rows, err := d.db.Query(`SELECT id,workspace,path,kind,detail,created FROM issues ORDER BY id DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		var i Issue
		if err := rows.Scan(&i.ID, &i.Workspace, &i.Path, &i.Kind, &i.Detail, &i.Created); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (d *DB) Log(level, message string) error {
	if _, err := d.db.Exec(`INSERT INTO activity(created,level,message) VALUES(?,?,?)`, time.Now().Unix(), level, message); err != nil {
		return err
	}
	_, err := d.db.Exec(`DELETE FROM activity WHERE id <= (SELECT max(id) FROM activity) - 1000`)
	return err
}

func (d *DB) Activity(after int64) ([]Activity, error) {
	rows, err := d.db.Query(`SELECT id,created,level,message FROM activity WHERE id>? ORDER BY id DESC LIMIT 200`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.Created, &a.Level, &a.Message); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
