package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// On Windows the core's state and socket live in %LOCALAPPDATA%, which never roams: a roaming
// state.db would follow the user to another computer without its keyring token.
func TestPathsWindowsLocalAppData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", t.TempDir())
	dir, _, sock, err := paths()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(local, "kydrive"); dir != want {
		t.Fatalf("dir %s, want %s", dir, want)
	}
	if want := filepath.Join(local, "kydrive", "sync.sock"); sock != want {
		t.Fatalf("socket %s, want %s", sock, want)
	}
	t.Setenv("LOCALAPPDATA", `AppData\Local`)
	if _, _, _, err := paths(); err == nil {
		t.Fatal("a relative %LOCALAPPDATA% was accepted")
	}
}

func write(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMoveState(t *testing.T) {
	t.Run("moves the database and its WAL once", func(t *testing.T) {
		old, dir := filepath.Join(t.TempDir(), "kydrive"), filepath.Join(t.TempDir(), "kydrive")
		for _, n := range []string{"state.db", "state.db-wal", "state.db-shm"} {
			write(t, filepath.Join(old, n), n)
		}
		if err := moveState(old, dir); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"state.db", "state.db-wal", "state.db-shm"} {
			if read(t, filepath.Join(dir, n)) != n {
				t.Fatalf("%s not moved", n)
			}
			if _, err := os.Stat(filepath.Join(old, n)); !os.IsNotExist(err) {
				t.Fatalf("%s left behind", n)
			}
		}
		if err := moveState(old, dir); err != nil {
			t.Fatalf("second run: %v", err)
		}
	})
	t.Run("never overwrites existing state", func(t *testing.T) {
		old, dir := filepath.Join(t.TempDir(), "kydrive"), filepath.Join(t.TempDir(), "kydrive")
		write(t, filepath.Join(old, "state.db"), "old")
		write(t, filepath.Join(dir, "state.db"), "new")
		if err := moveState(old, dir); err != nil {
			t.Fatal(err)
		}
		if read(t, filepath.Join(dir, "state.db")) != "new" || read(t, filepath.Join(old, "state.db")) != "old" {
			t.Fatal("existing state was overwritten or the old copy lost")
		}
	})
	t.Run("nothing to move", func(t *testing.T) {
		if err := moveState(filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "kydrive")); err != nil {
			t.Fatal(err)
		}
	})
}
