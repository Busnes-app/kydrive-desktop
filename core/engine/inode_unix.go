//go:build !windows

package engine

import (
	"io/fs"
	"syscall"
)

// inode identifies a file across renames on the same file system; 0 when unknown.
func inode(_ string, fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
