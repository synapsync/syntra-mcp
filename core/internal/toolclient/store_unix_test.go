//go:build linux || darwin

package toolclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func unixStoreFD(t *testing.T, store *Store) int {
	t.Helper()
	backend, ok := store.fs.(*unixStoreFS)
	if !ok || backend == nil {
		t.Fatal("store does not use the Unix descriptor adapter")
	}
	return backend.dir
}

func TestFIFOPrivateObjectRejected(t *testing.T) {
	root := t.TempDir()
	dir := root + "/profile"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(dir+"/profile.json", 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.Load(); err == nil {
		t.Fatal("accepted a FIFO as the active profile")
	}
}

// Probe the actual parent descriptor and fail before lock/profile/issuance work.
// The EEXIST branch represents a competing/interrupted creator whose entry is
// visible but is not yet known durable; retry must sync even when the next open
// succeeds.
func TestFinalDirectoryParentSyncOrderFailureAndRetry(t *testing.T) {
	for _, scenario := range []string{"new", "existing", "mkdir_EEXIST"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail_%v", scenario, fail), func(t *testing.T) {
				root := t.TempDir()
				dir := root + "/profile"
				if scenario == "existing" {
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				parentInfo, err := os.Stat(root)
				if err != nil {
					t.Fatal(err)
				}
				parentStat := parentInfo.Sys().(*syscall.Stat_t)
				var events []string
				syncs := 0
				mkdir := func(fd int, name string, mode uint32) error {
					events = append(events, "mkdir")
					if scenario == "existing" {
						t.Fatal("existing directory unexpectedly recreated")
					}
					if err := unix.Mkdirat(fd, name, mode); err != nil {
						return err
					}
					if scenario == "mkdir_EEXIST" {
						return unix.EEXIST
					}
					return nil
				}
				syncParent := func(fd int) error {
					syncs++
					events = append(events, "parent-sync")
					var stat unix.Stat_t
					if err := unix.Fstat(fd, &stat); err != nil || stat.Ino != parentStat.Ino || stat.Dev != parentStat.Dev {
						t.Fatal("parent sync did not target the still-open final parent")
					}
					if _, err := os.Stat(dir); err != nil {
						t.Fatal("sync happened before final directory existed")
					}
					if _, err := os.Lstat(dir + "/.lock"); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("lock opened before parent durability")
					}
					if _, err := os.Lstat(dir + "/profile.json"); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("profile written before parent durability")
					}
					if fail {
						return unix.EIO
					}
					return unix.Fsync(fd)
				}
				store, err := openStore(context.Background(), dir, syncParent, mkdir)
				if syncs != 1 {
					t.Fatal("final parent sync missing or repeated")
				}
				want := []string{"parent-sync"}
				if scenario != "existing" {
					want = []string{"mkdir", "parent-sync"}
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatal("incorrect final parent durability order")
				}
				if fail {
					if err == nil || store != nil {
						if store != nil {
							store.Close()
						}
						t.Fatal("parent sync failure returned a usable store")
					}
					if _, err := os.Lstat(dir + "/.lock"); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("parent sync failure advanced to lock creation")
					}
					// Retry syncs the entry left visible by the first attempt.
					fail = false
					store, err = openStore(context.Background(), dir, syncParent, func(int, string, uint32) error {
						t.Fatal("retry unexpectedly recreated the existing directory")
						return unix.EIO
					})
					if err != nil || syncs != 2 {
						t.Fatal("retry did not repeat parent sync", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if err = store.Save(pending(t)); err != nil {
					t.Fatal(err)
				}
				if _, err = store.Load(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestUnsafeFilesystemRejected(t *testing.T) {
	for _, kind := range []string{"symlink-file", "hardlink", "directory-file", "unsafe-file", "unsafe-lock", "symlink-lock", "unsafe-dir", "symlink-dir", "symlink-parent"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := root + "/profile"
			os.Mkdir(dir, 0700)
			target := dir + "/profile.json"
			p := pending(t)
			raw, _ := json.Marshal(p)
			switch kind {
			case "symlink-file":
				os.WriteFile(root+"/target", raw, 0600)
				os.Symlink(root+"/target", target)
			case "hardlink":
				os.WriteFile(root+"/target", raw, 0600)
				os.Link(root+"/target", target)
			case "directory-file":
				os.Mkdir(target, 0600)
			case "unsafe-file":
				os.WriteFile(target, raw, 0644)
			case "unsafe-lock":
				os.WriteFile(dir+"/.lock", nil, 0644)
			case "symlink-lock":
				os.WriteFile(root+"/target", nil, 0600)
				os.Symlink(root+"/target", dir+"/.lock")
			case "unsafe-dir":
				os.Chmod(dir, 0755)
			case "symlink-dir":
				os.Remove(dir)
				os.Mkdir(root+"/target", 0700)
				os.Symlink(root+"/target", dir)
			case "symlink-parent":
				os.Mkdir(root+"/actual", 0700)
				os.Mkdir(root+"/actual/profile", 0700)
				os.Symlink(root+"/actual", root+"/alias")
				dir = root + "/alias/profile"
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s, e := OpenStore(ctx, dir)
			if e == nil {
				defer s.Close()
				_, e = s.Load()
			}
			if e == nil {
				t.Fatal("accepted unsafe filesystem")
			}
		})
	}
}
