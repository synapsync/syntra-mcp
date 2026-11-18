package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBadInputBeforeProfile(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "syntra")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	root := t.TempDir()
	profile := filepath.Join(root, "missing-profile")
	bad := filepath.Join(root, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"content":{"destination":{"badKey":1}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"prepare", "link", "publish"} {
		t.Run(command, func(t *testing.T) {
			cmd := exec.Command(binary, command, "--profile-dir", profile, "--file", bad)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "invalid authored content request") || strings.Contains(string(out), "local profile") {
				t.Fatalf("%v %s", err, out)
			}
		})
	}
	out, err := exec.Command(binary, "reply", "--profile-dir", profile, "--workspace", "44444444-4444-4444-8444-444444444444", "--ticket", "77777777-7777-4777-8777-777777777777", "--summary", "hello", "--context-file", filepath.Join(root, "absent")).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "context file unavailable") || strings.Contains(string(out), "profile") {
		t.Fatalf("reply: %v %s", err, out)
	}
	semantic := filepath.Join(root, "semantic.json")
	if err := os.WriteFile(semantic, []byte(`{"requestId":"invalid","content":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command(binary, "prepare", "--profile-dir", profile, "--file", semantic).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "requestId") || strings.Contains(string(out), "local profile") {
		t.Fatalf("semantic validation: %v %s", err, out)
	}
	out, err = exec.Command(binary, "prepare", "--profile-dir", profile, "--template", "reply", "--workspace", "bad", "--ticket", "bad").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "workspace") || strings.Contains(string(out), "local profile") {
		t.Fatalf("template validation: %v %s", err, out)
	}
	if _, err = os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("input validation touched profile: %v", err)
	}
	valid := filepath.Join(root, "valid.json")
	if err = os.WriteFile(valid, []byte(`{"requestId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","content":{"formatVersion":1,"expectedAuthorUserId":"11111111-1111-4111-8111-111111111111","workspaceId":"44444444-4444-4444-8444-444444444444","title":"Title","originalDestinationIntent":"Alex","summary":"Summary","destination":{"unlinkedLabel":"Alex"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command(binary, "prepare", "--profile-dir", profile, "--file", valid).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "No Syntra connection. Run: syntra login --profile-dir") || strings.Contains(string(out), "syntra identity") {
		t.Fatalf("identity was checked before loading profile: %v %s", err, out)
	}
}

func TestUnknownCommandDoesNotEchoUnsafeValue(t *testing.T) {
	for _, v := range []string{"\x1b[31mX", "bad\nkey", strings.Repeat("x", 65), string([]byte{0xff})} {
		e := unknownCommand(v)
		if e == nil || strings.ContainsAny(e.Error(), "\x1b\r\n") || strings.Contains(e.Error(), v) {
			t.Fatalf("unsafe diagnostic: %q", e)
		}
	}
}
