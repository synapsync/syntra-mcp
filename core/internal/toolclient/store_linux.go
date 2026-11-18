//go:build linux

package toolclient

import (
	"context"

	"golang.org/x/sys/unix"
)

// openPlatformStore opens the private profile directory with a descriptor-relative
// no-follow walk and holds an exclusive flock until Close.
func openPlatformStore(ctx context.Context, path string) (*Store, error) {
	return openStore(ctx, path, syncDirectoryDescriptor, unix.Mkdirat)
}

func normalizeStoragePath(path string) string { return path }

func syncFileDescriptor(fd int) error      { return unix.Fsync(fd) }
func syncDirectoryDescriptor(fd int) error { return unix.Fsync(fd) }

func renameAtNoReplace(dir int, oldName, newName string) error {
	return unix.Renameat2(dir, oldName, dir, newName, unix.RENAME_NOREPLACE)
}

func checkExtendedACL(int) error { return nil }
