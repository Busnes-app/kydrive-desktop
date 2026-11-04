package engine

import (
	"io/fs"
	"syscall"
	"unsafe"
)

var getFileInformationByHandleEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileInformationByHandleEx")

// ctime is the NTFS ChangeTime (any write or metadata change), in 100 ns units; 0 when unknown.
func ctime(p string, _ fs.FileInfo) int64 {
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return 0
	}
	const fileReadAttributes = 0x80
	h, err := syscall.CreateFile(name, fileReadAttributes, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(h)
	var info struct {
		CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
		FileAttributes                                          uint32
		_                                                       uint32
	}
	const fileBasicInfo = 0
	if r, _, _ := getFileInformationByHandleEx.Call(uintptr(h), fileBasicInfo, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); r == 0 {
		return 0
	}
	return info.ChangeTime
}
