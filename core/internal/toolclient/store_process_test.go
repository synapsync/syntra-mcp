package toolclient

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const storeLockChildPathEnv = "SYNTRA_TEST_STORE_LOCK_CHILD_PATH"
const storeLockChildModeEnv = "SYNTRA_TEST_STORE_LOCK_CHILD_MODE"

func TestStoreLockChildProcess(t *testing.T) {
	dir, ok := os.LookupEnv(storeLockChildPathEnv)
	if !ok {
		return
	}
	mode := os.Getenv(storeLockChildModeEnv)
	if mode == "blocked" {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		defer cancel()
		if store, err := OpenStore(ctx, dir); err == nil {
			store.Close()
			t.Fatal("acquired a profile lock held by another process")
		} else if err.Error() != "profile lock deadline or cancellation" {
			t.Fatalf("lock contention returned an unexpected error: %v", err)
		}
		return
	}
	if mode != "free" {
		t.Fatalf("unexpected child mode %q", mode)
	}
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatalf("could not acquire released profile lock: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLockExcludesCompetingProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	runChild := func(mode string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockChildProcess$")
		cmd.Env = append(os.Environ(), storeLockChildPathEnv+"="+dir, storeLockChildModeEnv+"="+mode)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("storage child (%s) failed: %v: %s", mode, err, output)
		}
	}

	runChild("blocked")
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	runChild("free")
}
