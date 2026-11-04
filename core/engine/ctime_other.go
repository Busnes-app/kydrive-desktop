//go:build !linux && !darwin && !windows

package engine

import "io/fs"

func ctime(string, fs.FileInfo) int64 { return 0 }
