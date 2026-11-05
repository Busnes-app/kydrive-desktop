package engine

import (
	"io/fs"
	"syscall"
)

// ctime is the file's change time (any write or metadata change), UnixNano; 0 when unknown.
func ctime(_ string, fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctimespec.Nano()
	}
	return 0
}
