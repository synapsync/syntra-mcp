//go:build windows

package toolclient

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func privateStoreLockFileInfo(t *testing.T, store *Store, _ string) os.FileInfo {
	t.Helper()
	lock := store.fs.(*windowsStoreFS).lock
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(process, lock, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(duplicate), ".lock")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func symlinkPermissionUnavailable(err error) bool {
	return errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) || errors.Is(err, os.ErrPermission)
}

func assertPrivateStoreObject(store *Store, path string, want os.FileMode) error {
	backend, ok := store.fs.(*windowsStoreFS)
	if !ok || backend == nil {
		return ErrProfile
	}
	if want == 0700 {
		return checkPrivateDirectory(backend.dir, backend.userSID)
	}
	if filepath.Base(path) == ".lock" {
		if backend.lock == 0 || backend.lock == windows.InvalidHandle {
			return ErrProfile
		}
		return checkPrivateFile(backend.lock, backend.userSID)
	}
	file, err := backend.openRead(filepath.Base(path))
	if err != nil {
		return err
	}
	return file.Close()
}
