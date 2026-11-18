package toolclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "syntra.local/mcp/gen/syntra/v1"
)

func config() Config {
	return Config{APIOrigin: "https://api.example", ProviderOrigin: "https://provider.example", PublishableKey: "sb_publishable_fixtureOnlyPublicKey", Label: "Test tools", Capabilities: []v1.ToolCapability{1}, LifetimeSeconds: 300}
}
func pending(t *testing.T) *Profile {
	t.Helper()
	p, e := NewPending(config(), "11111111-1111-4111-8111-111111111111")
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPrivateStoreRoundTripAndSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	s, e := OpenStore(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p := pending(t)
	if e = s.Save(p); e != nil {
		t.Fatal(e)
	}
	got, e := s.Load()
	if e != nil || got.Secret != p.Secret {
		t.Fatal(e)
	}
	for _, path := range []string{dir, filepath.Join(dir, "profile.json"), filepath.Join(dir, ".lock")} {
		want := os.FileMode(0600)
		if path == dir {
			want = 0700
		}
		if err := assertPrivateStoreObject(s, path, want); err != nil {
			t.Fatalf("unsafe stored object %s: %v", path, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "profile.json"))
	for name, mutate := range map[string]func(string) string{
		"duplicate": func(s string) string { return strings.Replace(s, `"version":1`, `"version":1,"version":1`, 1) },
		"unknown": func(s string) string {
			return strings.Replace(s, `"version":1`, `"version":1,"refresh_token":"forbidden"`, 1)
		},
		"case alias": func(s string) string { return strings.Replace(s, `"apiOrigin"`, `"APIOrigin"`, 1) },
		"null":       func(s string) string { return strings.Replace(s, `"capabilities":[1]`, `"capabilities":null`, 1) },
		"oversize":   func(s string) string { return s + strings.Repeat(" ", maxProfileBytes) },
		"trailing":   func(s string) string { return s + "{}" },
		"missing":    func(s string) string { return strings.Replace(s, `,"state":"pending"`, ``, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			if e := os.WriteFile(filepath.Join(dir, "profile.json"), []byte(mutate(string(raw))), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Load(); e == nil {
				t.Fatal("accepted invalid schema")
			}
		})
	}
}
func TestStoreLockDeadlineAndAtomicReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	s, e := OpenStore(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	p := pending(t)
	if e = s.Save(p); e != nil {
		t.Fatal(e)
	}
	lockBefore := privateStoreLockFileInfo(t, s, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if other, e := OpenStore(ctx, dir); e == nil {
		other.Close()
		t.Fatal("lock not exclusive")
	}
	if e = s.Save(p); e != nil {
		t.Fatal(e)
	}
	lockAfter := privateStoreLockFileInfo(t, s, dir)
	if !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("lock inode changed")
	}
	s.Close()
	s, e = OpenStore(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Load(); e != nil {
		t.Fatal(e)
	}
	if e = s.Remove(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Load(); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}
func TestOriginAndCapabilityGuards(t *testing.T) {
	for _, s := range []string{"http://api.example", "https://api.example/", "https://a:b@api.example", "https://api.example?secret=x", "https://api.example#x", "https://api.example/path", "https://api.example?"} {
		if ValidateOrigin(s) == nil {
			t.Fatal("accepted", s)
		}
	}
	for _, caps := range [][]v1.ToolCapability{{}, {2}, {1, 1}, {1, 3}, {1, 5}, {2, 1}, {1, 2, 4, 3}} {
		c := config()
		c.Capabilities = caps
		if c.Validate() == nil {
			t.Fatal("accepted invalid capabilities")
		}
	}
}

func TestStoreUnicodeStrictness(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	s, e := OpenStore(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p := pending(t)
	raw, _ := json.Marshal(p)
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"Test tools"`), []byte(`"\ud800"`), 1), bytes.Replace(raw, []byte(`"Test tools"`), []byte{'"', 0xff, '"'}, 1)} {
		if e = os.WriteFile(filepath.Join(dir, "profile.json"), bad, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Load(); e == nil {
			t.Fatal("invalid Unicode accepted")
		}
	}
	if !validEscapedUnicode([]byte(`"\ud83d\ude00"`)) || validEscapedUnicode([]byte(`"\udc00"`)) {
		t.Fatal("surrogate validation error")
	}
}
