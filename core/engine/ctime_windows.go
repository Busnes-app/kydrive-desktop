package engine

import (
	"io/fs"
	"os"
	"syscall"
	"unsafe"
)

var getFileInformationByHandleEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileInformationByHandleEx")

// ctime is the NTFS ChangeTime (any write or metadata change), UnixNano; 0 when unknown.
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
	return handleCtime(h)
}

func handleCtime(h syscall.Handle) int64 {
	var info struct {
		CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
		FileAttributes                                          uint32
		_                                                       uint32
	}
	const fileBasicInfo = 0
	if r, _, _ := getFileInformationByHandleEx.Call(uintptr(h), fileBasicInfo, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); r == 0 {
		return 0
	}
	if info.ChangeTime == 0 {
		return 0
	}
	return (info.ChangeTime - 116444736000000000) * 100 // from 100 ns units since 1601
}

// Read the upload's open handle, never a path an editor could replace during the upload.
func openedIdentity(f *os.File, _ fs.FileInfo) (uint64, int64) {
	h := syscall.Handle(f.Fd())
	var info syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(h, &info) != nil {
		return 0, 0
	}
	return uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), handleCtime(h)
}
