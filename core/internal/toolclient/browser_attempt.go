package toolclient

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	v1 "syntra.local/mcp/gen/syntra/v1"
)

const browserAttemptVersion = 1
const browserProfileVersion = 2
const browserLoginMethod = "browser"

var ErrBrowserAttemptExists = errors.New("a different pending browser attempt already exists")

// PendingBrowserAttempt is the private durable intent retained before BeginLogin.
// Its independent secrets are removed from the active profile after activation.
type PendingBrowserAttempt struct {
	Version    int           `json:"version"`
	Method     string        `json:"method"`
	State      string        `json:"state"`
	Config     BrowserConfig `json:"config"`
	RequestID  string        `json:"requestId"`
	ToolSecret string        `json:"toolSecret"`
	PollProof  string        `json:"pollProof"`
}

// NewPendingBrowserAttempt creates independent tool and proof credentials before
// any network request. The exact browser-only config is copied into the intent.
func NewPendingBrowserAttempt(config BrowserConfig) (*PendingBrowserAttempt, error) {
	if config.Validate() != nil {
		return nil, ErrProfile
	}
	toolSecret := make([]byte, 32)
	if _, err := rand.Read(toolSecret); err != nil {
		return nil, errors.New("system randomness unavailable")
	}
	var pollProof []byte
	for i := 0; i < 3; i++ {
		pollProof = make([]byte, 32)
		if _, err := rand.Read(pollProof); err != nil {
			return nil, errors.New("system randomness unavailable")
		}
		if !bytes.Equal(toolSecret, pollProof) {
			break
		}
	}
	if bytes.Equal(toolSecret, pollProof) {
		return nil, errors.New("independent credential generation failed")
	}
	config.Capabilities = append([]v1.ToolCapability(nil), config.Capabilities...)
	return &PendingBrowserAttempt{
		Version:    browserAttemptVersion,
		Method:     browserLoginMethod,
		State:      "pending",
		Config:     config,
		RequestID:  randomUUID(),
		ToolSecret: "syntra_tool_" + base64.RawURLEncoding.EncodeToString(toolSecret),
		PollProof:  base64.RawURLEncoding.EncodeToString(pollProof),
	}, nil
}

func (a *PendingBrowserAttempt) Validate() error {
	if a == nil || a.Version != browserAttemptVersion || a.Method != browserLoginMethod || a.State != "pending" || a.Config.Validate() != nil || !validUUID(a.RequestID) || !validToolSecret(a.ToolSecret) {
		return ErrProfile
	}
	proof, err := base64.RawURLEncoding.DecodeString(a.PollProof)
	if err != nil || len(proof) != 32 || base64.RawURLEncoding.EncodeToString(proof) != a.PollProof {
		return ErrProfile
	}
	toolSecret, _ := base64.RawURLEncoding.DecodeString(a.ToolSecret[12:])
	if bytes.Equal(toolSecret, proof) {
		return ErrProfile
	}
	return nil
}

// BeginRequest derives digest-only wire intent from the retained credentials.
func (a *PendingBrowserAttempt) BeginRequest() (*v1.BeginLoginRequest, error) {
	if a.Validate() != nil {
		return nil, ErrProfile
	}
	proof, _ := base64.RawURLEncoding.DecodeString(a.PollProof)
	secretDigest := sha256.Sum256([]byte(a.ToolSecret))
	proofDigest := sha256.Sum256(proof)
	return &v1.BeginLoginRequest{
		RequestId:        a.RequestID,
		SecretDigest:     append([]byte(nil), secretDigest[:]...),
		ProofDigest:      append([]byte(nil), proofDigest[:]...),
		Label:            a.Config.Label,
		Capabilities:     append([]v1.ToolCapability(nil), a.Config.Capabilities...),
		ExpiresInSeconds: a.Config.LifetimeSeconds,
	}, nil
}

// Fingerprint computes the frozen consent binding from the same canonical
// Begin payload that is sent on the wire.
func (a *PendingBrowserAttempt) Fingerprint() ([]byte, error) {
	request, err := a.BeginRequest()
	if err != nil {
		return nil, err
	}
	return loginFingerprint(request)
}

func loginFingerprint(request *v1.BeginLoginRequest) ([]byte, error) {
	if request == nil || !validUUID(request.RequestId) || len(request.SecretDigest) != 32 || len(request.ProofDigest) != 32 || bytes.Equal(request.SecretDigest, request.ProofDigest) || !utf8.ValidString(request.Label) || validateConnectionConfig(request.Label, request.Capabilities, request.ExpiresInSeconds) != nil {
		return nil, ErrProfile
	}
	id, err := hex.DecodeString(strings.ReplaceAll(request.RequestId, "-", ""))
	if err != nil || len(id) != 16 {
		return nil, ErrProfile
	}
	var canonical bytes.Buffer
	canonical.WriteString("syntra.tool-login\x00\x01")
	canonical.Write(id)
	canonical.Write(request.SecretDigest)
	canonical.Write(request.ProofDigest)
	if err := binary.Write(&canonical, binary.BigEndian, uint32(len(request.Label))); err != nil {
		return nil, ErrProfile
	}
	canonical.WriteString(request.Label)
	if err := binary.Write(&canonical, binary.BigEndian, request.ExpiresInSeconds); err != nil {
		return nil, ErrProfile
	}
	if err := binary.Write(&canonical, binary.BigEndian, uint32(len(request.Capabilities))); err != nil {
		return nil, ErrProfile
	}
	for _, capability := range request.Capabilities {
		if err := binary.Write(&canonical, binary.BigEndian, int32(capability)); err != nil {
			return nil, ErrProfile
		}
	}
	digest := sha256.Sum256(canonical.Bytes())
	return digest[:], nil
}

func (a *PendingBrowserAttempt) PollAuthorization() (string, error) {
	if a.Validate() != nil {
		return "", ErrProfile
	}
	return "SyntraLogin " + a.PollProof, nil
}

func (a *PendingBrowserAttempt) ToolAuthorization() (string, error) {
	if a.Validate() != nil {
		return "", ErrProfile
	}
	return "Bearer " + a.ToolSecret, nil
}

// BrowserConfigMatchesProfile reports whether a browser login request exactly
// matches the authorization represented by an existing profile. Previous OTP
// profiles can be reused too; their provider fields are private implementation
// details and do not change the connection's requested scope.
func BrowserConfigMatchesProfile(profile *Profile, config BrowserConfig) bool {
	if profile == nil || profile.Validate() != nil || profile.State != "active" || ValidateOrigin(config.APIOrigin) != nil || validateConnectionConfig(config.Label, config.Capabilities, config.LifetimeSeconds) != nil || profile.Config.APIOrigin != config.APIOrigin || profile.Config.Label != config.Label || profile.Config.LifetimeSeconds != config.LifetimeSeconds || !equalCaps(profile.Config.Capabilities, config.Capabilities) {
		return false
	}
	if profile.Version == 1 {
		return true
	}
	return profile.Version == browserProfileVersion && profile.Method == browserLoginMethod && ValidateOrigin(config.BrowserOrigin) == nil && profile.BrowserOrigin == config.BrowserOrigin
}

// ActiveProfile adapts verified issuance metadata to the explicit browser v2
// profile without inventing previous provider credentials.
func (a *PendingBrowserAttempt) ActiveProfile(connection *v1.ToolConnection) (*Profile, error) {
	if a.Validate() != nil {
		return nil, ErrProfile
	}
	grantedLifetime, err := validateBrowserGrant(a.Config, connection)
	if err != nil {
		return nil, err
	}
	profile := &Profile{
		Version:       browserProfileVersion,
		Method:        browserLoginMethod,
		BrowserOrigin: a.Config.BrowserOrigin,
		Config: Config{
			APIOrigin:       a.Config.APIOrigin,
			Label:           a.Config.Label,
			Capabilities:    append([]v1.ToolCapability(nil), connection.Capabilities...),
			LifetimeSeconds: grantedLifetime,
		},
		AccountID: connection.OwnerUserId,
		RequestID: a.RequestID,
		Secret:    a.ToolSecret,
		State:     "active",
	}
	profile.ConnectionID = connection.Id
	profile.CreatedAt = connection.CreatedAt.AsTime().Format(time.RFC3339Nano)
	profile.ExpiresAt = connection.ExpiresAt.AsTime().Format(time.RFC3339Nano)
	if profile.Validate() != nil {
		return nil, ErrProfile
	}
	return profile, nil
}

func validateBrowserGrant(requested BrowserConfig, connection *v1.ToolConnection) (int32, error) {
	if connection == nil || !validUUID(connection.Id) || !validUUID(connection.OwnerUserId) || connection.Label != requested.Label || connection.State != v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE || connection.RevokedAt != nil || connection.CreatedAt == nil || connection.ExpiresAt == nil || connection.CreatedAt.CheckValid() != nil || connection.ExpiresAt.CheckValid() != nil {
		return 0, errors.New("issuance metadata mismatch; pending browser attempt retained")
	}
	created, expires := connection.CreatedAt.AsTime(), connection.ExpiresAt.AsTime()
	lifetime := expires.Sub(created)
	if lifetime%time.Second != 0 || lifetime < 300*time.Second || lifetime > time.Duration(requested.LifetimeSeconds)*time.Second || !expires.After(time.Now()) || created.After(time.Now().Add(time.Minute)) {
		return 0, errors.New("invalid or expired browser grant lifetime; pending browser attempt retained")
	}
	grantedLifetime := int32(lifetime / time.Second)
	grantedConfig := requested
	grantedConfig.Capabilities = append([]v1.ToolCapability(nil), connection.Capabilities...)
	grantedConfig.LifetimeSeconds = grantedLifetime
	if grantedConfig.Validate() != nil {
		return 0, errors.New("invalid browser grant scope; pending browser attempt retained")
	}
	for _, capability := range connection.Capabilities {
		if !slices.Contains(requested.Capabilities, capability) {
			return 0, errors.New("browser grant expanded requested scope; pending browser attempt retained")
		}
	}
	return grantedLifetime, nil
}
