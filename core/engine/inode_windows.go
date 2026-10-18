package engine

import (
	"io/fs"
	"syscall"
)

// inode returns the NTFS file ID, which survives renames on the same volume; 0 when unknown.
func inode(p string, _ fs.FileInfo) uint64 {
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return 0
	}
	h, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(h)
	var d syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(h, &d) != nil {
		return 0
	}
	return uint64(d.FileIndexHigh)<<32 | uint64(d.FileIndexLow)
}
