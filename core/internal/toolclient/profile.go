// Package toolclient shares one human-owned local connection across CLI and MCP.
// It never persists managed human sessions or interprets thread content as code.
package toolclient

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	v1 "syntra.local/mcp/gen/syntra/v1"
)

var ErrProfile = errors.New("invalid or unsafe local profile")

// Config is public connection configuration, never a service/admin key.
type Config struct {
	APIOrigin       string              `json:"apiOrigin"`
	ProviderOrigin  string              `json:"providerOrigin"`
	PublishableKey  string              `json:"publishableKey"`
	Label           string              `json:"label"`
	Capabilities    []v1.ToolCapability `json:"capabilities"`
	LifetimeSeconds int32               `json:"lifetimeSeconds"`
}

// BrowserConfig contains only public fields for browser-authorized profiles.
// ProviderOrigin and PublishableKey remain specific to the OTP method.
type BrowserConfig struct {
	APIOrigin       string              `json:"apiOrigin"`
	BrowserOrigin   string              `json:"browserOrigin"`
	Label           string              `json:"label"`
	Capabilities    []v1.ToolCapability `json:"capabilities"`
	LifetimeSeconds int32               `json:"lifetimeSeconds"`
}

// Profile contains ONLY a tool bearer and bounded issuance/configuration metadata.
// Pending persists before the first issuance request and survives ambiguous errors.
type Profile struct {
	Version       int    `json:"version"`
	Config        Config `json:"config"`
	AccountID     string `json:"accountId"`
	RequestID     string `json:"requestId"`
	Secret        string `json:"secret"`
	State         string `json:"state"`
	ConnectionID  string `json:"connectionId,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
	Method        string `json:"-"`
	BrowserOrigin string `json:"-"`
}

type profileV1JSON struct {
	Version      int    `json:"version"`
	Config       Config `json:"config"`
	AccountID    string `json:"accountId"`
	RequestID    string `json:"requestId"`
	Secret       string `json:"secret"`
	State        string `json:"state"`
	ConnectionID string `json:"connectionId,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
}

type profileV2JSON struct {
	Version      int           `json:"version"`
	Method       string        `json:"method"`
	Config       BrowserConfig `json:"config"`
	AccountID    string        `json:"accountId"`
	RequestID    string        `json:"requestId"`
	Secret       string        `json:"secret"`
	State        string        `json:"state"`
	ConnectionID string        `json:"connectionId"`
	CreatedAt    string        `json:"createdAt"`
	ExpiresAt    string        `json:"expiresAt"`
}

// MarshalJSON preserves the exact v1 profile shape while giving browser
// profiles an explicit method discriminator and browser-only config schema.
func (p Profile) MarshalJSON() ([]byte, error) {
	switch p.Version {
	case 1:
		if p.Method != "" || p.BrowserOrigin != "" {
			return nil, ErrProfile
		}
		return json.Marshal(profileV1JSON{
			Version: p.Version, Config: p.Config, AccountID: p.AccountID,
			RequestID: p.RequestID, Secret: p.Secret, State: p.State,
			ConnectionID: p.ConnectionID, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
		})
	case 2:
		if p.Method != "browser" || p.Config.ProviderOrigin != "" || p.Config.PublishableKey != "" {
			return nil, ErrProfile
		}
		return json.Marshal(profileV2JSON{
			Version: p.Version, Method: p.Method,
			Config: BrowserConfig{
				APIOrigin: p.Config.APIOrigin, BrowserOrigin: p.BrowserOrigin,
				Label: p.Config.Label, Capabilities: p.Config.Capabilities,
				LifetimeSeconds: p.Config.LifetimeSeconds,
			},
			AccountID: p.AccountID, RequestID: p.RequestID, Secret: p.Secret,
			State: p.State, ConnectionID: p.ConnectionID,
			CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
		})
	default:
		return nil, ErrProfile
	}
}

func ValidateOrigin(s string) error {
	u, e := url.Parse(s)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.String() != s || strings.ContainsAny(s, "\r\n\t ") {
		return errors.New("expected an exact HTTPS origin without path, credentials, query or fragment")
	}
	return nil
}
func validUUID(s string) bool {
	if len(s) != 36 || s == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// NewRequestID generates a fresh RFC 4122 version 4 request identifier.
func NewRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("system randomness unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func randomUUID() string {
	id, err := NewRequestID()
	if err != nil {
		panic("system randomness unavailable")
	}
	return id
}

func (c Config) Validate() error {
	if ValidateOrigin(c.APIOrigin) != nil || ValidateOrigin(c.ProviderOrigin) != nil {
		return ErrProfile
	}
	// Only public publishable keys are accepted; JWT keys can
	// contain privileged roles and are deliberately unsupported in this pilot.
	if !strings.HasPrefix(c.PublishableKey, "sb_publishable_") || len(c.PublishableKey) > 256 || len(c.PublishableKey) < 20 {
		return ErrProfile
	}
	for _, r := range c.PublishableKey {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return ErrProfile
		}
	}
	return validateConnectionConfig(c.Label, c.Capabilities, c.LifetimeSeconds)
}

func (c BrowserConfig) Validate() error {
	if ValidateOrigin(c.APIOrigin) != nil || ValidateOrigin(c.BrowserOrigin) != nil {
		return ErrProfile
	}
	return validateConnectionConfig(c.Label, c.Capabilities, c.LifetimeSeconds)
}

func validateConnectionConfig(label string, capabilities []v1.ToolCapability, lifetimeSeconds int32) error {
	if !utf8.ValidString(label) || len(label) > 320 || utf8.RuneCountInString(label) > 80 || strings.TrimSpace(label) == "" {
		return ErrProfile
	}
	for _, r := range label {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ErrProfile
		}
	}
	if lifetimeSeconds < 300 || lifetimeSeconds > 7776000 || len(capabilities) == 0 || capabilities[0] != v1.ToolCapability_TOOL_CAPABILITY_READ {
		return ErrProfile
	}
	prev := v1.ToolCapability_TOOL_CAPABILITY_UNSPECIFIED
	prepare := false
	for _, cap := range capabilities {
		if cap <= prev || cap > v1.ToolCapability_TOOL_CAPABILITY_MARK_READ {
			return ErrProfile
		}
		if cap == v1.ToolCapability_TOOL_CAPABILITY_PREPARE {
			prepare = true
		}
		if cap == v1.ToolCapability_TOOL_CAPABILITY_PUBLISH && !prepare {
			return ErrProfile
		}
		prev = cap
	}
	return nil
}
func (p *Profile) Validate() error {
	if p == nil || !validUUID(p.AccountID) || !validUUID(p.RequestID) || !validToolSecret(p.Secret) {
		return ErrProfile
	}
	if p.Version == 1 {
		if p.Method != "" || p.BrowserOrigin != "" || p.Config.Validate() != nil {
			return ErrProfile
		}
	} else if p.Version == 2 {
		if p.State != "active" || p.Method != browserLoginMethod || p.Config.ProviderOrigin != "" || p.Config.PublishableKey != "" || (BrowserConfig{
			APIOrigin: p.Config.APIOrigin, BrowserOrigin: p.BrowserOrigin,
			Label: p.Config.Label, Capabilities: p.Config.Capabilities,
			LifetimeSeconds: p.Config.LifetimeSeconds,
		}).Validate() != nil {
			return ErrProfile
		}
	} else {
		return ErrProfile
	}
	switch p.State {
	case "pending":
		if p.ConnectionID != "" || p.CreatedAt != "" || p.ExpiresAt != "" {
			return ErrProfile
		}
	case "active":
		if !validUUID(p.ConnectionID) {
			return ErrProfile
		}
		a, e := time.Parse(time.RFC3339Nano, p.CreatedAt)
		b, f := time.Parse(time.RFC3339Nano, p.ExpiresAt)
		if e != nil || f != nil || b.Sub(a) != time.Duration(p.Config.LifetimeSeconds)*time.Second {
			return ErrProfile
		}
	default:
		return ErrProfile
	}
	return nil
}

func validToolSecret(secret string) bool {
	if len(secret) != 55 || !strings.HasPrefix(secret, "syntra_tool_") {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(secret[12:])
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == secret[12:]
}
func NewPending(c Config, account string) (*Profile, error) {
	if c.Validate() != nil || !validUUID(account) {
		return nil, ErrProfile
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return nil, errors.New("system randomness unavailable")
	}
	c.Capabilities = append([]v1.ToolCapability(nil), c.Capabilities...)
	return &Profile{Version: 1, Config: c, AccountID: account, RequestID: randomUUID(), Secret: "syntra_tool_" + base64.RawURLEncoding.EncodeToString(b), State: "pending"}, nil
}
func SameConfig(a, b Config) bool { return reflect.DeepEqual(a, b) }
