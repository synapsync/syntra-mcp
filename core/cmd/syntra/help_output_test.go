package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpOutputAllCommands(t *testing.T) {
	for _, name := range helpOrder {
		for _, args := range [][]string{{"help", name}, {name, "--help"}, {name, "-h"}} {
			r, w, e := os.Pipe()
			if e != nil {
				t.Fatal(e)
			}
			old := os.Stdout
			os.Stdout = w
			e = run(context.Background(), args)
			w.Close()
			os.Stdout = old
			data, _ := io.ReadAll(r)
			r.Close()
			if e != nil || len(data) == 0 || !strings.Contains(string(data), "Example:") {
				t.Fatalf("%v: %v %q", args, e, data)
			}
		}
	}
}

func TestCompiledOfflineHelp(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "syntra")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	root := t.TempDir()
	for _, name := range helpOrder {
		for _, args := range [][]string{{"help", name}, {name, "--help"}, {name, "-h"}} {
			cmd := exec.Command(binary, args...)
			cmd.Env = append(os.Environ(), "HOME="+root, "XDG_CONFIG_HOME="+root, "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "NO_PROXY=")
			out, err := cmd.CombinedOutput()
			if err != nil || len(out) == 0 || !strings.Contains(string(out), "Example:") {
				t.Fatalf("%v: %v %s", args, err, out)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "syntra")); !os.IsNotExist(err) {
		t.Fatalf("help accessed a profile: %v", err)
	}
}
