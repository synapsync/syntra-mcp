//go:build linux

package main

import (
	"context"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syntra.local/mcp/fixtures"
)

func TestCompiledBrowserNoBrowserSupportsPipedCrossDeviceLogin(t *testing.T) {
	bin := buildCLI(t)
	root := t.TempDir()
	fixture, err := fixtures.NewDeviceFixtureForTestHarness(nil, fixtures.TestHarnessOptions{
		AcceptFirstValidRequestID: true,
		VerificationURI:           "https://syntra.example.test/auth/device",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(fixture.Handler())
	t.Cleanup(server.Close)
	ca := filepath.Join(root, "fixture-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "browser-opened")
	binDir := filepath.Join(root, "bin")
	if err = os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	openCommand := filepath.Join(binDir, "xdg-open")
	if err = os.WriteFile(openCommand, []byte("#!/bin/sh\nprintf opened > \"$SYNTRA_TEST_MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Join(root, "profile")
	args := []string{
		"login", "--profile-dir", profileDir, "--api", server.URL,
		"--browser-origin", "https://syntra.example.test", "--no-browser", "--timeout", "1s",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(fixtureEnvironment(), "XDG_CONFIG_HOME="+root, "SSL_CERT_FILE="+ca, "SSL_CERT_DIR="+filepath.Join(root, "no-cert-dir"), "PATH="+binDir, "SYNTRA_TEST_MARKER="+marker)
	cmd.Stdin = strings.NewReader("")
	output, runErr := cmd.CombinedOutput()
	if runErr == nil || !strings.Contains(string(output), "Verification URL: https://syntra.example.test/auth/device") || !strings.Contains(string(output), "Code: "+fixtures.UserCode) {
		t.Fatalf("piped --no-browser login did not display cross-device instructions: err=%v output=%s", runErr, output)
	}
	if strings.Contains(string(output), "requires a human terminal") || strings.Contains(string(output), "localhost") {
		t.Fatalf("browser login imposed a TTY/local-listener requirement: %s", output)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("--no-browser invoked the browser opener")
	}
	if _, err = os.Stat(filepath.Join(profileDir, "browser-attempt.json")); err != nil {
		t.Fatal("browser attempt was not persisted before Begin", err)
	}
}
