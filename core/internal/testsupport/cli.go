// Package testsupport builds a disposable CLI for compiled-process tests.
// The release executable never includes the fixture trust configuration.
package testsupport

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func BuildCLI(t *testing.T, packageDir string, cert *x509.Certificate) string {
	t.Helper()
	packageDir, err := filepath.Abs(packageDir)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	source := `package main
import "crypto/x509"

func init() {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(` + strconv.Quote(string(certificate)) + `)) {
		panic("invalid test fixture certificate")
	}
	x509.SetFallbackRoots(roots)
}
`
	backing := filepath.Join(root, "fixture_trust.go")
	writeFile(t, backing, []byte(source))
	overlay, err := json.Marshal(struct {
		Replace map[string]string
	}{Replace: map[string]string{filepath.Join(packageDir, "fixture_trust.go"): backing}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(root, "overlay.json")
	writeFile(t, overlayPath, overlay)
	binary := filepath.Join(root, "syntra")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goTool += ".exe"
	}
	build := exec.Command(goTool, "build", "-mod=readonly", "-overlay", overlayPath, "-o", binary, ".")
	build.Dir = packageDir
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture CLI: %v\n%s", err, out)
	}
	return binary
}

func FixtureCLIEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR", "GODEBUG", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GODEBUG=x509usefallbackroots=1")
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
