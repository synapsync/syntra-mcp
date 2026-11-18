//go:build windows

package toolclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func openWindowsTestStore(t *testing.T) (string, *Store) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "profile")
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatalf("open private Windows store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return dir, store
}

func TestWindowsStoreCreatesAndValidatesPrivateDACLs(t *testing.T) {
	dir, store := openWindowsTestStore(t)
	if err := store.Save(pending(t)); err != nil {
		t.Fatal(err)
	}
	backend := store.fs.(*windowsStoreFS)
	if err := checkPrivateDirectory(backend.dir, backend.userSID); err != nil {
		t.Fatalf("new profile directory failed protected-DACL check: %v", err)
	}
	if err := checkPrivateFile(backend.lock, backend.userSID); err != nil {
		t.Fatalf("lock file failed protected-DACL check: %v", err)
	}
	profile, err := backend.openRead("profile.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.syncDir(); err != nil {
		t.Fatalf("NTFS directory flush failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "profile.json")); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsStoreRejectsExtraDACLPrincipals(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		dir, store := openWindowsTestStore(t)
		if err := store.Save(pending(t)); err != nil {
			t.Fatal(err)
		}
		setWindowsTestBroadDACL(t, filepath.Join(dir, "profile.json"), false)
		if _, err := store.Load(); err == nil {
			t.Fatal("accepted a profile DACL granting access to another principal")
		}
	})

	t.Run("directory", func(t *testing.T) {
		dir, store := openWindowsTestStore(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		setWindowsTestBroadDACL(t, dir, true)
		if reopened, err := OpenStore(context.Background(), dir); err == nil {
			_ = reopened.Close()
			t.Fatal("accepted a profile directory DACL granting access to another principal")
		}
	})
}

func TestWindowsStoreRejectsHardLinkedPrivateFiles(t *testing.T) {
	dir, store := openWindowsTestStore(t)
	backend := store.fs.(*windowsStoreFS)
	seed, err := backend.createExclusive(".link-source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Write([]byte("fixture")); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := backend.syncFile(seed); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, ".link-source"), filepath.Join(dir, "profile.json")); err != nil {
		t.Fatalf("create NTFS hard-link fixture: %v", err)
	}
	if _, err := backend.openRead("profile.json"); err == nil {
		t.Fatal("accepted a private file with multiple hard links")
	}
}

func TestWindowsStoreRejectsReparseDirectoriesAndFiles(t *testing.T) {
	t.Run("parent", func(t *testing.T) {
		root := t.TempDir()
		realParent := filepath.Join(root, "real")
		if err := os.Mkdir(realParent, 0700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(realParent, alias); err != nil {
			t.Skipf("directory symlink fixture unavailable: %v", err)
		}
		if store, err := OpenStore(context.Background(), filepath.Join(alias, "profile")); err == nil {
			_ = store.Close()
			t.Fatal("followed a reparse-point ancestor")
		}
	})

	t.Run("final directory", func(t *testing.T) {
		root := t.TempDir()
		realDir := filepath.Join(root, "real")
		if err := os.Mkdir(realDir, 0700); err != nil {
			t.Fatal(err)
		}
		profilePath := filepath.Join(root, "profile")
		if err := os.Symlink(realDir, profilePath); err != nil {
			t.Skipf("directory symlink fixture unavailable: %v", err)
		}
		if store, err := OpenStore(context.Background(), profilePath); err == nil {
			_ = store.Close()
			t.Fatal("opened a reparse-point store directory")
		}
	})

	t.Run("file", func(t *testing.T) {
		dir, store := openWindowsTestStore(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "profile.json")); err != nil {
			t.Skipf("file symlink fixture unavailable: %v", err)
		}
		store, err := OpenStore(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.Load(); err == nil {
			t.Fatal("followed a reparse-point profile file")
		}
	})
}

func TestWindowsStoreLockContentionCancellationAndRelease(t *testing.T) {
	dir, store := openWindowsTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if other, err := OpenStore(ctx, dir); err == nil {
		_ = other.Close()
		t.Fatal("opened a second store while the process lock was held")
	} else if err.Error() != "profile lock deadline or cancellation" {
		t.Fatalf("lock deadline returned %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatalf("released lock did not allow a new store: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := OpenStore(canceled, dir); err == nil || !errors.Is(err, context.Canceled) && err.Error() != "profile lock deadline or cancellation" {
		t.Fatalf("already-canceled store open returned %v", err)
	}
}

func TestWindowsStoreReplacementFailsWithoutOverwritingOpenTarget(t *testing.T) {
	dir, store := openWindowsTestStore(t)
	if err := store.Save(pending(t)); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(dir, "profile.json")
	before, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	widePath, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := windows.CreateFile(widePath, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(reader)
	if err := store.Save(pending(t)); err == nil {
		t.Fatal("replaced a target held open without delete sharing")
	}
	after, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed replacement changed the prior profile bytes")
	}
}

func setWindowsTestBroadDACL(t *testing.T, path string, directory bool) {
	t.Helper()
	sid, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;;FA;;;" + sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("parse test DACL: %v", err)
	}
	widePath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	access := uint32(windows.READ_CONTROL | windows.WRITE_DAC)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(widePath, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}
