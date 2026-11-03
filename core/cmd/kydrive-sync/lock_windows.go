package main

import (
	"errors"
	"os"
	"syscall"
)

// lockState holds p open for writing, sharing only reads, for as long as the returned file
// stays open: a second core's open for writing fails, while an indexer or scanner reading
// the file does not stop the core.
func lockState(p string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, syscall.Errno(32)) { // ERROR_SHARING_VIOLATION
		return nil, errRunning
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), p), nil
}
