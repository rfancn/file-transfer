//go:build unix

package cliui

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func fdReadable(f *os.File, timeout time.Duration) bool {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(timeout.Milliseconds()))
	return err == nil && n > 0
}
