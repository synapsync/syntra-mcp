package toolclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

const maxResponseBytes = 1048576

// Client scopes every request to an immutable HTTPS API origin. A client never
// adopts an origin override from content, response metadata, or another profile.
type Client struct {
	origin string
	http   *http.Client
}

// NewClient uses normal verified TLS. roots may supply a fixture/private CA, but
// does not disable hostname/certificate verification or permit HTTP.
func NewClient(origin string, roots *x509.CertPool) (*Client, error) {
	if ValidateOrigin(origin) != nil {
		return nil, errors.New("invalid API origin")
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	t.ResponseHeaderTimeout = 15 * time.Second
	t.MaxResponseHeaderBytes = 16384
	return &Client{origin: origin, http: &http.Client{Transport: t, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}, nil
}

// Do implements the generated client's HTTP interface with exact origin and
// bounded response enforcement. Errors never contain upstream text or URLs.
func (c *Client) Do(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != c.origin || r.URL.User != nil || r.URL.RawQuery != "" || r.URL.Fragment != "" {
		return nil, errors.New("request origin mismatch")
	}
	res, e := c.http.Do(r)
	if e != nil {
		return nil, errors.New("API transport failed")
	}
	if res.ContentLength > maxResponseBytes {
		res.Body.Close()
		return nil, errors.New("API response too large")
	}
	res.Body = &limitedBody{body: res.Body, remaining: maxResponseBytes}
	return res, nil
}

type limitedBody struct {
	body      io.ReadCloser
	remaining int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		var one [1]byte
		n, e := b.body.Read(one[:])
		if n > 0 {
			return 0, errors.New("response too large")
		}
		return 0, e
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, e := b.body.Read(p)
	b.remaining -= int64(n)
	return n, e
}
func (b *limitedBody) Close() error { return b.body.Close() }

// RPCOptions keeps the generated clients bounded. Callers must also use Do.
func RPCOptions() []connect.ClientOption {
	return []connect.ClientOption{connect.WithReadMaxBytes(maxResponseBytes), connect.WithSendMaxBytes(maxResponseBytes)}
}
func (c *Client) Tools() syntrav1connect.ToolConnectionServiceClient {
	return syntrav1connect.NewToolConnectionServiceClient(c, c.origin, RPCOptions()...)
}
func SafeRPCError(e error) error {
	if e == nil {
		return nil
	}
	return fmt.Errorf("API request failed (%s)", connect.CodeOf(e))
}

// Preserve only a documented denial or a sanitized unauthenticated code for
// the caller to attach profile-specific guidance.
func safeConnectionRPCError(err error) error {
	if cap := actionable.CapabilityFromConnect(err); cap != v1.ToolCapability_TOOL_CAPABILITY_UNSPECIFIED {
		return actionable.DeniedCapability(cap)
	}
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		return connect.NewError(connect.CodeUnauthenticated, SafeRPCError(err))
	}
	return SafeRPCError(err)
}

func bearer[T any](r *connect.Request[T], token string) *connect.Request[T] {
	r.Header().Set("Authorization", "Bearer "+token)
	return r
}

// Identity validates the live connection against its retained account/metadata.
func (c *Client) Identity(ctx context.Context, p *Profile) (*v1.ToolConnection, error) {
	if p.Validate() != nil || p.Config.APIOrigin != c.origin || p.State != "active" {
		return nil, ErrProfile
	}
	r, e := c.Tools().GetIdentity(ctx, bearer(connect.NewRequest(&v1.GetIdentityRequest{}), p.Secret))
	if e != nil {
		return nil, safeConnectionRPCError(e)
	}
	if e = validateConnection(p, r.Msg.Connection, true); e != nil {
		return nil, e
	}
	return r.Msg.Connection, nil
}
func (c *Client) Disconnect(ctx context.Context, p *Profile) error {
	if p.Validate() != nil || p.Config.APIOrigin != c.origin || p.State != "active" {
		return ErrProfile
	}
	r, e := c.Tools().Disconnect(ctx, bearer(connect.NewRequest(&v1.DisconnectRequest{}), p.Secret))
	if e != nil {
		return safeConnectionRPCError(e)
	}
	q := r.Msg.Connection
	if q == nil || q.State != v1.ToolConnectionState_TOOL_CONNECTION_STATE_REVOKED || q.RevokedAt == nil || q.RevokedAt.CheckValid() != nil {
		return errors.New("invalid revocation metadata")
	}
	// Original issuance fields must still match; revocation is checked separately.
	clone := proto.Clone(q).(*v1.ToolConnection)
	clone.State = v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE
	clone.RevokedAt = nil
	return validateConnection(p, clone, true)
}
func validateConnection(p *Profile, q *v1.ToolConnection, retained bool) error {
	if q == nil || !validUUID(q.Id) || q.OwnerUserId != p.AccountID || q.Label != p.Config.Label || !equalCaps(q.Capabilities, p.Config.Capabilities) || q.State != v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE || q.RevokedAt != nil || q.CreatedAt == nil || q.ExpiresAt == nil || q.CreatedAt.CheckValid() != nil || q.ExpiresAt.CheckValid() != nil {
		return errors.New("issuance metadata mismatch; pending profile retained")
	}
	a, b := q.CreatedAt.AsTime(), q.ExpiresAt.AsTime()
	if b.Sub(a) != time.Duration(p.Config.LifetimeSeconds)*time.Second || !b.After(time.Now()) || a.After(time.Now().Add(time.Minute)) {
		return errors.New("invalid or expired issuance lifetime; pending profile retained")
	}
	if retained && (p.ConnectionID != q.Id || p.CreatedAt != a.Format(time.RFC3339Nano) || p.ExpiresAt != b.Format(time.RFC3339Nano)) {
		return errors.New("retained connection metadata mismatch")
	}
	return nil
}
func equalCaps(a, b []v1.ToolCapability) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// OTPProvider implements the managed email OTP endpoints, without refresh flow.
// Human access/refresh fields never enter Profile or any output.
type OTPProvider struct {
	client *Client
	key    string
}

func NewOTPProvider(origin, key string, roots *x509.CertPool) (*OTPProvider, error) {
	c, e := NewClient(origin, roots)
	if e != nil {
		return nil, e
	}
	test := Config{APIOrigin: origin, ProviderOrigin: origin, PublishableKey: key, Label: "validation", Capabilities: []v1.ToolCapability{1}, LifetimeSeconds: 300}
	if test.Validate() != nil {
		return nil, errors.New("expected public Supabase publishable key")
	}
	return &OTPProvider{client: c, key: key}, nil
}
func (p *OTPProvider) post(ctx context.Context, path string, payload any) ([]byte, error) {
	raw, e := json.Marshal(payload)
	if e != nil {
		return nil, errors.New("invalid managed auth request")
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, p.client.origin+path, bytes.NewReader(raw))
	if e != nil {
		return nil, errors.New("invalid managed auth request")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("apikey", p.key)
	res, e := p.client.Do(r)
	if e != nil {
		return nil, errors.New("managed auth transport failed")
	}
	defer res.Body.Close()
	body, e := io.ReadAll(res.Body)
	if e != nil {
		return nil, errors.New("invalid managed auth response")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("managed email OTP rejected or expired")
	}
	return body, nil
}
func (p *OTPProvider) Request(ctx context.Context, email string) error {
	if len(email) > 254 || !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n\x00") {
		return errors.New("invalid email")
	}
	_, e := p.post(ctx, "/auth/v1/otp", struct {
		Email      string `json:"email"`
		CreateUser bool   `json:"create_user"`
	}{email, false})
	return e
}

// Verify returns only an ephemeral access token. The bounded provider response,
// including any refresh token/provider subject, dies with the login process.
func (p *OTPProvider) Verify(ctx context.Context, email string, code []byte) (string, error) {
	if len(code) < 6 || len(code) > 10 {
		return "", errors.New("invalid OTP")
	}
	for _, b := range code {
		if b < '0' || b > '9' {
			return "", errors.New("invalid OTP")
		}
	}
	raw, e := p.post(ctx, "/auth/v1/verify", struct {
		Email string `json:"email"`
		Token string `json:"token"`
		Type  string `json:"type"`
	}{email, string(code), "email"})
	if e != nil {
		return "", e
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if json.Unmarshal(raw, &out) != nil || out.AccessToken == "" || len(out.AccessToken) > 16384 || !strings.EqualFold(out.TokenType, "bearer") || strings.ContainsAny(out.AccessToken, "\r\n\x00") {
		return "", errors.New("invalid managed auth session")
	}
	return out.AccessToken, nil
}

// CompleteLogin consumes a transient human JWT. The caller must have acquired
// Store before requesting OTP. Pending is saved+fsynced before digest issuance.
func (c *Client) CompleteLogin(ctx context.Context, store *Store, config Config, humanJWT string) (*Profile, error) {
	if config.Validate() != nil || config.APIOrigin != c.origin {
		return nil, ErrProfile
	}
	existing, e := store.Load()
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if existing != nil && existing.Method == browserLoginMethod {
		return nil, errors.New("browser profile cannot be replaced through OTP login")
	}
	if existing != nil && !SameConfig(existing.Config, config) {
		return nil, errors.New("existing profile configuration differs; retained without replacement")
	}
	users := syntrav1connect.NewUserServiceClient(c, c.origin, RPCOptions()...)
	me, e := users.GetMe(ctx, bearer(connect.NewRequest(&v1.GetMeRequest{}), humanJWT))
	if e != nil {
		return nil, SafeRPCError(e)
	}
	account := me.Msg.GetProfile().GetId()
	if !validUUID(account) {
		return nil, errors.New("application account not admitted")
	}
	if existing != nil && existing.AccountID != account {
		return nil, errors.New("existing profile belongs to another application account")
	}
	if existing != nil && existing.State == "active" {
		if _, e = c.Identity(ctx, existing); e != nil {
			return nil, e
		}
		return existing, nil
	}
	p := existing
	if p == nil {
		p, e = NewPending(config, account)
		if e != nil {
			return nil, e
		}
		if e = store.Save(p); e != nil {
			return nil, e
		}
	}
	sum := sha256.Sum256([]byte(p.Secret))
	req := &v1.HumanCreateRequest{Label: p.Config.Label, Capabilities: append([]v1.ToolCapability(nil), p.Config.Capabilities...), ExpiresInSeconds: p.Config.LifetimeSeconds, RequestId: p.RequestID, SecretDigest: sum[:]}
	result, e := c.Tools().HumanCreate(ctx, bearer(connect.NewRequest(req), humanJWT))
	if e != nil {
		return nil, SafeRPCError(e)
	}
	if e = validateConnection(p, result.Msg.Connection, false); e != nil {
		return nil, e
	}
	q := result.Msg.Connection
	p.State = "active"
	p.ConnectionID = q.Id
	p.CreatedAt = q.CreatedAt.AsTime().Format(time.RFC3339Nano)
	p.ExpiresAt = q.ExpiresAt.AsTime().Format(time.RFC3339Nano)
	if e = store.Save(p); e != nil {
		return nil, e
	}
	return p, nil
}
