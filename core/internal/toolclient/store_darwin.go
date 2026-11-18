//go:build darwin

package toolclient

import (
	"context"
	"encoding/binary"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// OpenStore uses descriptor-relative no-follow operations and holds an
// exclusive flock until Close. The Unix adapter handles its shared state.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	return openStore(ctx, path, syncDirectoryDescriptor, unix.Mkdirat)
}

// macOS exposes /tmp and /var as system symlink aliases to their real
// directories under /private. Resolve only those fixed OS aliases before the
// no-follow descriptor walk; arbitrary symlink components remain rejected.
func normalizeStoragePath(path string) string {
	for _, alias := range []string{"/tmp", "/var"} {
		if path == alias {
			return "/private" + alias
		}
		if strings.HasPrefix(path, alias+"/") {
			return "/private" + path
		}
	}
	return path
}

// F_FULLFSYNC asks the storage stack to flush file data and metadata to
// permanent media. Unsupported filesystems fail closed; fsync is not used as a
// silent durability downgrade.
func syncFileDescriptor(fd int) error {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_FULLFSYNC, 0)
	return err
}

func syncDirectoryDescriptor(fd int) error { return syncFileDescriptor(fd) }

func renameAtNoReplace(dir int, oldName, newName string) error {
	return unix.RenameatxNp(dir, oldName, dir, newName, unix.RENAME_EXCL)
}

type darwinAttrList struct {
	count     uint16
	reserved  uint16
	common    uint32
	volume    uint32
	directory uint32
	file      uint32
	fork      uint32
}

// checkExtendedACL reads the returned-attribute bitmap through the already
// opened descriptor. Any syscall or response ambiguity is unsafe and rejected.
func checkExtendedACL(fd int) error {
	attrs := darwinAttrList{
		count:  unix.ATTR_BIT_MAP_COUNT,
		common: unix.ATTR_CMN_RETURNED_ATTRS | unix.ATTR_CMN_EXTENDED_SECURITY,
	}
	var result [64]byte
	_, _, errno := unix.Syscall6(
		unix.SYS_FGETATTRLIST,
		uintptr(fd),
		uintptr(unsafe.Pointer(&attrs)),
		uintptr(unsafe.Pointer(&result[0])),
		uintptr(len(result)),
		0,
		0,
	)
	runtime.KeepAlive(attrs)
	runtime.KeepAlive(result)
	if errno != 0 {
		return errno
	}
	length := binary.NativeEndian.Uint32(result[0:4])
	if length < 24 || length > uint32(len(result)) {
		return ErrProfile
	}
	returnedCommon := binary.NativeEndian.Uint32(result[4:8])
	if returnedCommon != uint32(unix.ATTR_CMN_RETURNED_ATTRS) {
		return ErrProfile
	}
	return nil
}
