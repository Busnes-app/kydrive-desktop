//go:build !windows

package engine

import (
	"os"
	"syscall"
	"testing"
)

// S-R2/S-R10: a FIFO where the server has a file neither hangs the pass nor keeps the file
// away once it is gone.
func TestSpecialFileInPlaceOfAFile(t *testing.T) {
	v := setup(t, nil)
	if err := syscall.Mkfifo(v.path("channel"), 0o644); err != nil {
		t.Fatal(err)
	}
	v.r.Put(t, v.ws, "channel", []byte("data"))
	within(t, v.sync)
	os.Remove(v.path("channel"))
	v.sync()
	if v.read("channel") != "data" {
		t.Fatal("the file did not sync once freed")
	}
}
