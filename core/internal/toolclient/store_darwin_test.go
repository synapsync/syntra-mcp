//go:build darwin

package toolclient

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinExtendedACLRejected(t *testing.T) {
	t.Run("store-directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "profile")
		store, err := OpenStore(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		addDarwinReadACL(t, dir)
		if reopened, err := OpenStore(context.Background(), dir); err == nil {
			reopened.Close()
			t.Fatal("accepted a store directory with an extended ACL")
		}
	})

	t.Run("lock-file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "profile")
		store, err := OpenStore(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		addDarwinReadACL(t, filepath.Join(dir, ".lock"))
		if reopened, err := OpenStore(context.Background(), dir); err == nil {
			reopened.Close()
			t.Fatal("accepted a lock file with an extended ACL")
		}
	})

	t.Run("profile-file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "profile")
		store, err := OpenStore(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Save(pending(t)); err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		profile := filepath.Join(dir, "profile.json")
		addDarwinReadACL(t, profile)
		assertDarwinPrivateFileACLRejected(t, dir, "profile.json")
	})

	t.Run("retained-record", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "profile")
		store, err := OpenStore(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		name := "attempt-11111111-1111-4111-8111-111111111111.json"
		if err = store.writeOnce(name, []byte("fixture")); err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		addDarwinReadACL(t, filepath.Join(dir, name))
		assertDarwinPrivateFileACLRejected(t, dir, name)
	})
}

func addDarwinReadACL(t *testing.T, path string) {
	t.Helper()
	command := exec.Command("/bin/chmod", "+a", "everyone allow read", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("filesystem cannot create the Darwin ACL fixture: %v: %s", err, output)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := os.FileMode(0600)
	if info.IsDir() {
		want = 0700
	}
	if info.Mode().Perm() != want {
		t.Fatalf("ACL fixture changed the basic mode: got %04o, want %04o", info.Mode().Perm(), want)
	}
}

func assertDarwinPrivateFileACLRejected(t *testing.T, dir, name string) {
	t.Helper()
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.readRecord(name); err == nil {
		t.Fatal("accepted a private file with an extended ACL")
	}
}
