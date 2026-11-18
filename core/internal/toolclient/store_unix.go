//go:build linux || darwin

package toolclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// unixStoreFS is the Linux and Darwin descriptor-relative adapter. Shared
// profile and journal operations depend only on storeFS, not Unix fd details.
type unixStoreFS struct {
	dir  int
	lock *os.File
}

func newUnixStore(dir int, lock *os.File) *Store {
	return &Store{fs: &unixStoreFS{dir: dir, lock: lock}}
}

func (s *unixStoreFS) close() error {
	if s == nil || s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	_ = unix.Close(s.dir)
	s.lock = nil
	return err
}

func (s *unixStoreFS) checkDir() error {
	if s == nil {
		return ErrProfile
	}
	return safeStat(s.dir, true)
}

func (s *unixStoreFS) openRead(name string) (*os.File, error) {
	if s == nil || !validPrivateFilename(name) || s.checkDir() != nil {
		return nil, ErrProfile
	}
	fd, err := unix.Openat(s.dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrProfile
	}
	if safeStat(fd, false) != nil {
		_ = unix.Close(fd)
		return nil, ErrProfile
	}
	return os.NewFile(uintptr(fd), "private file"), nil
}

func (s *unixStoreFS) createExclusive(name string) (*os.File, error) {
	if s == nil || !validPrivateFilename(name) || s.checkDir() != nil {
		return nil, ErrProfile
	}
	fd, err := unix.Openat(s.dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, ErrProfile
	}
	if safeStat(fd, false) != nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(s.dir, name, 0)
		return nil, ErrProfile
	}
	return os.NewFile(uintptr(fd), "temporary private file"), nil
}

func (s *unixStoreFS) syncFile(file *os.File) error {
	if s == nil || file == nil || s.checkDir() != nil {
		return ErrProfile
	}
	return syncFileDescriptor(int(file.Fd()))
}

func (s *unixStoreFS) syncDir() error {
	if s == nil || s.checkDir() != nil {
		return ErrProfile
	}
	return syncDirectoryDescriptor(s.dir)
}

func (s *unixStoreFS) rename(oldName, newName string, replace bool) error {
	if s == nil || !validPrivateFilename(oldName) || !validPrivateFilename(newName) || s.checkDir() != nil {
		return ErrProfile
	}
	if replace {
		return unix.Renameat(s.dir, oldName, s.dir, newName)
	}
	return renameAtNoReplace(s.dir, oldName, newName)
}

func (s *unixStoreFS) remove(name string) error {
	if s == nil || !validPrivateFilename(name) || s.checkDir() != nil {
		return ErrProfile
	}
	return unix.Unlinkat(s.dir, name, 0)
}

func validPrivateFilename(name string) bool {
	return name != "" && filepath.Base(name) == name && name != "." && name != ".."
}

func safeStat(fd int, dir bool) error {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil {
		return ErrProfile
	}
	mode := uint32(0600)
	kind := uint32(unix.S_IFREG)
	if dir {
		mode = 0700
		kind = unix.S_IFDIR
	}
	if uint32(stat.Uid) != uint32(os.Geteuid()) || uint32(stat.Mode)&07777 != mode || uint32(stat.Mode)&unix.S_IFMT != kind || (!dir && uint64(stat.Nlink) != 1) {
		return ErrProfile
	}
	if checkExtendedACL(fd) != nil {
		return ErrProfile
	}
	return nil
}

// openStore walks ancestors without following links. It can create the final
// directory and the immediate parent of the default shared profile.
func openStore(ctx context.Context, path string, syncParent func(int) error, mkdir func(int, string, uint32) error) (*Store, error) {
	if ctx.Err() != nil {
		return nil, errors.New("profile lock deadline or cancellation")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrProfile
	}
	createDefaultParent := isDefaultProfileDir(path)
	path = normalizeStoragePath(path)
	if syncParent == nil {
		syncParent = syncDirectoryDescriptor
	}
	if mkdir == nil {
		mkdir = unix.Mkdirat
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrProfile
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && (i == len(parts)-1 || (createDefaultParent && i == len(parts)-2)) {
			if openErr = mkdir(fd, part, 0700); openErr != nil && !errors.Is(openErr, unix.EEXIST) {
				_ = unix.Close(fd)
				return nil, ErrProfile
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if openErr == nil && i == len(parts)-2 && syncParent(fd) != nil {
				_ = unix.Close(next)
				_ = unix.Close(fd)
				return nil, ErrProfile
			}
		}

		if openErr == nil && i == len(parts)-1 {
			// Persist the final directory entry even if it became visible during an
			// earlier interrupted create or an EEXIST race.
			if syncParent(fd) != nil {
				_ = unix.Close(next)
				_ = unix.Close(fd)
				return nil, ErrProfile
			}
		}
		_ = unix.Close(fd)
		if openErr != nil {
			if errors.Is(openErr, unix.ENOENT) {
				return nil, os.ErrNotExist
			}
			return nil, ErrProfile
		}
		fd = next
	}
	if safeStat(fd, true) != nil {
		_ = unix.Close(fd)
		return nil, ErrProfile
	}
	lockFD, err := unix.Openat(fd, ".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		_ = unix.Close(fd)
		return nil, ErrProfile
	}
	if safeStat(lockFD, false) != nil {
		_ = unix.Close(lockFD)
		_ = unix.Close(fd)
		return nil, ErrProfile
	}
	lock := os.NewFile(uintptr(lockFD), "profile lock")
	for {
		if ctx.Err() != nil {
			_ = lock.Close()
			_ = unix.Close(fd)
			return nil, errors.New("profile lock deadline or cancellation")
		}
		err = unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = lock.Close()
			_ = unix.Close(fd)
			return nil, ErrProfile
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			_ = unix.Close(fd)
			return nil, errors.New("profile lock deadline or cancellation")
		case <-time.After(20 * time.Millisecond):
		}
	}
	// Revalidate after waiting: never repair unsafe existing objects implicitly.
	if safeStat(fd, true) != nil || safeStat(lockFD, false) != nil {
		_ = lock.Close()
		_ = unix.Close(fd)
		return nil, ErrProfile
	}
	return newUnixStore(fd, lock), nil
}
