package toolclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

func TestConnectionRecoveryGuidance(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	shared, err := DefaultProfileDir()
	if err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(root, "custom profile")
	read := v1.ToolCapability_TOOL_CAPABILITY_READ
	prepare := v1.ToolCapability_TOOL_CAPABILITY_PREPARE
	publish := v1.ToolCapability_TOOL_CAPABILITY_PUBLISH
	mark := v1.ToolCapability_TOOL_CAPABILITY_MARK_READ
	for _, tc := range []struct {
		need    v1.ToolCapability
		granted []v1.ToolCapability
		flags   string
	}{
		{read, []v1.ToolCapability{read}, ""},
		{prepare, []v1.ToolCapability{read}, " --prepare"},
		{publish, []v1.ToolCapability{read}, " --prepare --publish"},
		{mark, []v1.ToolCapability{read, prepare, publish}, " --prepare --publish"},
	} {
		t.Run(capabilityName(tc.need), func(t *testing.T) {
			profile := &Profile{Config: Config{Capabilities: tc.granted}}
			err := ConnectionError(shared, profile, actionable.DeniedCapability(tc.need))
			local := (&MissingCapability{Required: tc.need, dir: shared, granted: tc.granted}).Error()
			if err.Error() != local || !strings.HasPrefix(local, "Missing capability "+strings.ToUpper(capabilityName(tc.need))+":") ||
				!strings.HasSuffix(local, "syntra login --reauthorize"+tc.flags+" (approve in the browser).") {
				t.Fatalf("incorrect recovery command: %v", err)
			}
		})
	}
	for _, dir := range []string{shared, custom} {
		empty := ProfileError(dir, os.ErrNotExist)
		expired := ConnectionError(dir, &Profile{}, connect.NewError(connect.CodeUnauthenticated, errors.New("private upstream text")))
		for _, err := range []error{empty, expired} {
			if !IsConnectionDiagnostic(err) || strings.Contains(err.Error(), "private upstream text") ||
				strings.Contains(err.Error(), "--profile-dir") != (dir == custom) ||
				strings.Count(err.Error(), "\n") != 0 {
				t.Fatalf("wrong connection diagnostic: %v", err)
			}
		}
	}
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("permission_denied"))
	if ConnectionError(shared, &Profile{}, denied) != denied {
		t.Fatal("generic authorization denial was misclassified as a capability error")
	}
}
