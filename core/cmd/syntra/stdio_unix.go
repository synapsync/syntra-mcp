//go:build linux || darwin

package main

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func duplicateMCPStdin() (io.ReadCloser, error)   { return pollableDuplicate(os.Stdin) }
func duplicateMCPStdout() (io.WriteCloser, error) { return pollableDuplicate(os.Stdout) }

// The duplicate is owned by this process and registered with Go's poller, so
// closing it cancels reads and writes without closing the inherited descriptor.
func pollableDuplicate(f *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "MCP stdio")
	if file == nil {
		_ = unix.Close(fd)
		return nil, os.ErrInvalid
	}
	return file, nil
}
