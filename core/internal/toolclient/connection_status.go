package toolclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"connectrpc.com/connect"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

func DefaultProfileDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil || !filepath.IsAbs(root) {
		return "", errors.New("set --profile-dir to a private absolute directory")
	}
	return filepath.Join(root, "syntra"), nil
}

func isDefaultProfileDir(dir string) bool {
	shared, err := DefaultProfileDir()
	if runtime.GOOS == "windows" {
		return err == nil && strings.EqualFold(shared, dir)
	}
	return err == nil && shared == dir
}

func profileFlag(dir string) string {
	if dir == "" || isDefaultProfileDir(dir) {
		return ""
	}
	// Keep the diagnostic on one line and protect spaces and shell metacharacters.
	if runtime.GOOS == "windows" {
		return " --profile-dir '" + strings.ReplaceAll(dir, "'", "''") + "'"
	}
	if strings.ContainsFunc(dir, unicode.IsControl) {
		escaped := strconv.Quote(dir)
		return " --profile-dir $'" + strings.ReplaceAll(escaped[1:len(escaped)-1], "'", `\'`) + "'"
	}
	return " --profile-dir '" + strings.ReplaceAll(dir, "'", "'\\''") + "'"
}

// ConnectionUnavailable carries the same actionable text through CLI and MCP.
type ConnectionUnavailable struct {
	dir   string
	renew bool
}

func (e *ConnectionUnavailable) Error() string {
	if e.renew {
		return "Syntra connection is expired or unavailable. Run: syntra login --reauthorize" + profileFlag(e.dir) + " (approve in the browser)."
	}
	return "No Syntra connection. Run: syntra login" + profileFlag(e.dir) + " (choose permissions in the browser; use --prepare for prepared_messages, or --prepare --publish to send)."
}

// IsConnectionDiagnostic reports whether err contains shared human guidance.
func IsConnectionDiagnostic(err error) bool {
	var missing *MissingCapability
	var unavailable *ConnectionUnavailable
	return errors.As(err, &missing) || errors.As(err, &unavailable)
}

// ProfileError attaches login guidance only to a missing local profile.
func ProfileError(dir string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return &ConnectionUnavailable{dir: dir}
	}
	return err
}

func capabilityName(capability v1.ToolCapability) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(capability.String(), "TOOL_CAPABILITY_")), "_", "-")
}

func capabilityNames(caps []v1.ToolCapability) []string {
	names := make([]string, 0, len(caps))
	for _, cap := range caps {
		names = append(names, capabilityName(cap))
	}
	return names
}

// MissingCapability reports a missing permission on a verified connection.
type MissingCapability struct {
	Required v1.ToolCapability
	granted  []v1.ToolCapability
	dir      string
}

func (e *MissingCapability) Error() string {
	const (
		prepare = v1.ToolCapability_TOOL_CAPABILITY_PREPARE
		publish = v1.ToolCapability_TOOL_CAPABILITY_PUBLISH
	)
	command := "syntra login --reauthorize"
	if e.Required == prepare || e.Required == publish || slices.Contains(e.granted, prepare) {
		command += " --prepare"
	}
	if e.Required == publish || slices.Contains(e.granted, publish) {
		command += " --publish"
	}
	permission := strings.ToUpper(capabilityName(e.Required))
	state := "the server denied this permission for the connection"
	if len(e.granted) != 0 && !slices.Contains(e.granted, e.Required) {
		state = "this connection allows " + strings.ToUpper(strings.Join(capabilityNames(e.granted), ", ")) + " only"
	}
	return fmt.Sprintf("Missing capability %s: %s. Run: %s%s (approve in the browser).", permission, state, command, profileFlag(e.dir))
}

// ConnectionError maps only explicit capability and authentication failures.
// Ordinary permission_denied responses can also mean resource access was refused.
func ConnectionError(dir string, p *Profile, err error) error {
	if cap := actionable.CapabilityFromConnect(err); cap != v1.ToolCapability_TOOL_CAPABILITY_UNSPECIFIED {
		return &MissingCapability{Required: cap, granted: p.Config.Capabilities, dir: dir}
	}
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		return &ConnectionUnavailable{dir: dir, renew: true}
	}
	return err
}

type ConnectionStatus struct {
	ProfileDir           string   `json:"profileDir"`
	DefaultSharedProfile bool     `json:"defaultSharedProfile"`
	CapabilityNames      []string `json:"capabilityNames"`
	APIOrigin            string   `json:"apiOrigin"`
	ShortConnectionID    string   `json:"shortConnectionId,omitempty"`
	DaysLeft             *int     `json:"daysLeft,omitempty"`
	ProfileWarning       string   `json:"profileWarning,omitempty"`
}

func (s ConnectionStatus) ProfileLine() string {
	kind := "custom"
	if s.DefaultSharedProfile {
		kind = "default shared"
	}
	return fmt.Sprintf("Profile directory: %s (%s)", strconv.Quote(s.ProfileDir), kind)
}

func DescribeProfile(ctx context.Context, dir string, p *Profile) ConnectionStatus {
	out := ConnectionStatus{ProfileDir: dir, DefaultSharedProfile: isDefaultProfileDir(dir)}
	now := time.Now()
	if p != nil {
		out.CapabilityNames = capabilityNames(p.Config.Capabilities)
		out.APIOrigin = p.Config.APIOrigin
		if p.State == "active" {
			out.ShortConnectionID = p.ConnectionID[:8]
			expires, _ := time.Parse(time.RFC3339Nano, p.ExpiresAt)
			days := 0
			if remaining := expires.Sub(now); remaining > 0 {
				days = int((remaining + 24*time.Hour - 1) / (24 * time.Hour))
			}
			out.DaysLeft = &days
		}
	}
	if out.DefaultSharedProfile {
		return out
	}
	out.ProfileWarning = "Agents using different profile dirs do not share logins."
	if dir, err := DefaultProfileDir(); err == nil && alternateProfileExists(ctx, dir, p, now) {
		out.ProfileWarning = fmt.Sprintf("Another profile exists at %s; agents using different profile dirs do not share logins.", strconv.Quote(dir))
	}
	return out
}

func alternateProfileExists(ctx context.Context, dir string, p *Profile, now time.Time) bool {
	// Only inspect the known default, using the same private profile validation.
	// Keep diagnostics bounded when a separate process holds that store's lock.
	if info, err := os.Lstat(filepath.Join(dir, "profile.json")); err != nil || !info.Mode().IsRegular() {
		return false
	}
	probe, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	store, err := OpenStore(probe, dir)
	if err != nil {
		return false
	}
	defer store.Close()
	other, err := store.Load()
	if err != nil || other.State != "active" {
		return false
	}
	expires, _ := time.Parse(time.RFC3339Nano, other.ExpiresAt)
	return expires.After(now) && (p == nil || p.ConnectionID != other.ConnectionID || p.Config.APIOrigin != other.Config.APIOrigin)
}
