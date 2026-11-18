//go:build !windows

package toolclient

import (
	"os"
	"path/filepath"
	"testing"
)

func privateStoreLockFileInfo(t *testing.T, _ *Store, dir string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func symlinkPermissionUnavailable(error) bool { return false }

func assertPrivateStoreObject(_ *Store, path string, want os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != want {
		return ErrProfile
	}
	return nil
}
