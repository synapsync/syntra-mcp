// Package fixtures supplies a synthetic client-development issuer, not a security
// implementation or evidence that the production API satisfies this contract.
package fixtures

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

// These public values are synthetic. Never use them against a deployed service.
const RequestID = "11111111-1111-4111-8111-111111111111"
const UserCode = "BCDFGHJK"
const HumanAuthorization = "Bearer fixture-user-only"
const fixtureVerificationURI = "https://syntra.example.test/auth/device"

// TestHarnessOptions enables isolated synthetic client-test behavior. Its
// verification URI must use HTTPS on a reserved .test name or a loopback host.
type TestHarnessOptions struct {
	AcceptFirstValidRequestID bool
	VerificationURI           string
	IdentityMetadataTransform func(*v1.ToolConnection) *v1.ToolConnection
}

// DeviceFixture models one known request, including exact retries and consent.
// It deliberately has no database, provider, invitations, or real bearer tokens.
type DeviceFixture struct {
	syntrav1connect.UnimplementedToolLoginServiceHandler
	syntrav1connect.UnimplementedToolConnectionServiceHandler
	mu                        sync.Mutex
	begun                     bool
	begin                     *v1.BeginLoginRequest
	preview                   *v1.ToolLoginRequest
	connection                *v1.ToolConnection
	state                     v1.ToolLoginState
	now                       func() time.Time
	nextPoll                  time.Time
	pollInterval              int32
	requestID                 string
	acceptFirstValidRequestID bool
	verificationURI           string
	identityMetadataTransform func(*v1.ToolConnection) *v1.ToolConnection
}

func NewDeviceFixture(now func() time.Time) *DeviceFixture {
	if now == nil {
		now = time.Now
	}
	return &DeviceFixture{
		now:             now,
		state:           v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING,
		pollInterval:    5,
		requestID:       RequestID,
		verificationURI: fixtureVerificationURI,
	}
}

// NewDeviceFixtureForTestHarness configures a synthetic fixture for client
// harnesses that generate their own request UUID and verification URL. The
// first valid UUID is bound to this fixture; later requests must match it.
func NewDeviceFixtureForTestHarness(now func() time.Time, options TestHarnessOptions) (*DeviceFixture, error) {
	verificationURI := options.VerificationURI
	if verificationURI == "" {
		verificationURI = fixtureVerificationURI
	}
	if !isTrustedTestVerificationURI(verificationURI) {
		return nil, errors.New("fixture verification URI must use HTTPS on a reserved test or loopback host")
	}

	fixture := NewDeviceFixture(now)
	fixture.acceptFirstValidRequestID = options.AcceptFirstValidRequestID
	if options.AcceptFirstValidRequestID {
		fixture.requestID = ""
	}
	fixture.verificationURI = verificationURI
	fixture.identityMetadataTransform = options.IdentityMetadataTransform
	return fixture, nil
}

func isTrustedTestVerificationURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if strings.HasSuffix(hostname, ".test") && len(hostname) > len(".test") {
		return true
	}
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func (f *DeviceFixture) Handler() http.Handler {
	loginPath, loginHandler := syntrav1connect.NewToolLoginServiceHandler(f)
	connectionPath, connectionHandler := syntrav1connect.NewToolConnectionServiceHandler(f)
	mux := http.NewServeMux()
	mux.Handle(loginPath, loginHandler)
	mux.Handle(connectionPath, connectionHandler)
	return mux
}
func (f *DeviceFixture) BeginLogin(_ context.Context, r *connect.Request[v1.BeginLoginRequest]) (*connect.Response[v1.BeginLoginResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(r.Header().Values("Authorization")) != 0 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture Begin rejects authorization"))
	}
	in, fingerprint, err := normalizeIntent(r.Msg)
	if err != nil || (!f.acceptFirstValidRequestID && in.RequestId != f.requestID) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("fixture expects a valid intent with its documented request ID"))
	}
	if f.begun && !proto.Equal(f.begin, in) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("fixture request differs"))
	}
	if !f.begun {
		f.begun = true
		f.requestID = in.RequestId
		f.begin = in
		f.preview = &v1.ToolLoginRequest{RequestId: f.requestID, UserCode: UserCode, Label: in.Label, Capabilities: append([]v1.ToolCapability(nil), in.Capabilities...), ExpiresInSeconds: in.ExpiresInSeconds, ExpiresAt: timestamppb.New(f.now().Add(10 * time.Minute)), Fingerprint: fingerprint, State: f.state}
	}
	f.expire()
	preview := proto.Clone(f.preview).(*v1.ToolLoginRequest)
	preview.State = f.state
	return connect.NewResponse(&v1.BeginLoginResponse{Request: preview, VerificationUri: f.verificationURI, IntervalSeconds: f.pollInterval}), nil
}
func (f *DeviceFixture) PollLogin(_ context.Context, r *connect.Request[v1.PollLoginRequest]) (*connect.Response[v1.PollLoginResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const prefix = "SyntraLogin "
	auth := r.Header().Get("Authorization")
	if !f.begun || r.Msg.RequestId != f.requestID || len(auth) < len(prefix) || auth[:len(prefix)] != prefix {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture proof rejected"))
	}
	raw, err := base64.RawURLEncoding.DecodeString(auth[len(prefix):])
	digest := sha256.Sum256(raw)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != auth[len(prefix):] || subtle.ConstantTimeCompare(digest[:], f.begin.ProofDigest) != 1 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture proof rejected"))
	}
	f.expire()
	if f.connection != nil && !f.now().Before(f.connection.ExpiresAt.AsTime()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture connection inactive"))
	}
	if f.now().Before(f.nextPoll) {
		failure := connect.NewError(connect.CodeResourceExhausted, errors.New("fixture slow down"))
		f.pollInterval += 5
		if f.pollInterval > 60 {
			f.pollInterval = 60
		}
		f.nextPoll = f.now().Add(time.Duration(f.pollInterval) * time.Second)
		failure.Meta().Set("Retry-After", strconv.Itoa(int(f.pollInterval)))
		return nil, failure
	}
	f.nextPoll = f.now().Add(time.Duration(f.pollInterval) * time.Second)
	result := &v1.PollLoginResponse{State: f.state, IntervalSeconds: f.pollInterval}
	if f.connection != nil {
		result.Connection = proto.Clone(f.connection).(*v1.ToolConnection)
	}
	return connect.NewResponse(result), nil
}

func (f *DeviceFixture) GetIdentity(_ context.Context, r *connect.Request[v1.GetIdentityRequest]) (*connect.Response[v1.GetIdentityResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const prefix = "Bearer "
	auth := r.Header().Get("Authorization")
	if !f.begun || f.begin == nil || len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture tool credential rejected"))
	}
	token := auth[len(prefix):]
	if !validToolToken(token) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture tool credential rejected"))
	}
	digest := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(digest[:], f.begin.SecretDigest) != 1 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture tool credential rejected"))
	}
	if f.state != v1.ToolLoginState_TOOL_LOGIN_STATE_APPROVED || f.connection == nil || f.connection.State != v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE || f.connection.ExpiresAt == nil || !f.now().Before(f.connection.ExpiresAt.AsTime()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture connection inactive"))
	}
	connection := proto.Clone(f.connection).(*v1.ToolConnection)
	if f.identityMetadataTransform != nil {
		connection = f.identityMetadataTransform(connection)
		if connection == nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("fixture identity metadata transform returned nil"))
		}
	}
	return connect.NewResponse(&v1.GetIdentityResponse{Connection: proto.Clone(connection).(*v1.ToolConnection)}), nil
}

func validToolToken(token string) bool {
	const tokenPrefix = "syntra_tool_"
	if !strings.HasPrefix(token, tokenPrefix) {
		return false
	}
	encoded := token[len(tokenPrefix):]
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == encoded
}

func (f *DeviceFixture) human(code, auth string) error {
	if auth != HumanAuthorization {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("synthetic human required"))
	}
	if !f.begun || strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", "")) != UserCode {
		return connect.NewError(connect.CodeNotFound, errors.New("fixture request unavailable"))
	}
	f.expire()
	return nil
}
func (f *DeviceFixture) GetLoginRequest(_ context.Context, r *connect.Request[v1.GetLoginRequestRequest]) (*connect.Response[v1.GetLoginRequestResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.human(r.Msg.UserCode, r.Header().Get("Authorization")); err != nil {
		return nil, err
	}
	if f.state == v1.ToolLoginState_TOOL_LOGIN_STATE_EXPIRED {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("fixture request unavailable"))
	}
	preview := proto.Clone(f.preview).(*v1.ToolLoginRequest)
	preview.State = f.state
	return connect.NewResponse(&v1.GetLoginRequestResponse{Request: preview}), nil
}
func (f *DeviceFixture) ApproveLogin(_ context.Context, r *connect.Request[v1.ApproveLoginRequest]) (*connect.Response[v1.ApproveLoginResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.human(r.Msg.UserCode, r.Header().Get("Authorization")); err != nil {
		return nil, err
	}
	caps := append([]v1.ToolCapability(nil), r.Msg.Capabilities...)
	slices.Sort(caps)
	lifetime := r.Msg.ExpiresInSeconds
	if lifetime == 0 {
		lifetime = 2592000
	}
	if subtle.ConstantTimeCompare(r.Msg.ExpectedFingerprint, f.preview.Fingerprint) != 1 || !validSubset(caps, f.begin.Capabilities) || lifetime < 300 || lifetime > f.begin.ExpiresInSeconds {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("fixture consent mismatch"))
	}
	if f.state == v1.ToolLoginState_TOOL_LOGIN_STATE_APPROVED {
		if !capsEqual(caps, f.connection.Capabilities) || f.connection.ExpiresAt.AsTime().Sub(f.connection.CreatedAt.AsTime()) != time.Duration(lifetime)*time.Second {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("fixture approval differs"))
		}
		return connect.NewResponse(&v1.ApproveLoginResponse{Connection: proto.Clone(f.connection).(*v1.ToolConnection)}), nil
	}
	if f.state != v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("fixture request terminal"))
	}
	f.state = v1.ToolLoginState_TOOL_LOGIN_STATE_APPROVED
	f.connection = &v1.ToolConnection{Id: "22222222-2222-4222-8222-222222222222", OwnerUserId: "33333333-3333-4333-8333-333333333333", Label: f.begin.Label, Capabilities: caps, CreatedAt: timestamppb.New(f.now()), ExpiresAt: timestamppb.New(f.now().Add(time.Duration(lifetime) * time.Second)), State: v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE}
	return connect.NewResponse(&v1.ApproveLoginResponse{Connection: proto.Clone(f.connection).(*v1.ToolConnection)}), nil
}
func (f *DeviceFixture) DenyLogin(_ context.Context, r *connect.Request[v1.DenyLoginRequest]) (*connect.Response[v1.DenyLoginResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.human(r.Msg.UserCode, r.Header().Get("Authorization")); err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(r.Msg.ExpectedFingerprint, f.preview.Fingerprint) != 1 || (f.state != v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING && f.state != v1.ToolLoginState_TOOL_LOGIN_STATE_DENIED) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("fixture consent mismatch"))
	}
	f.state = v1.ToolLoginState_TOOL_LOGIN_STATE_DENIED
	return connect.NewResponse(&v1.DenyLoginResponse{State: f.state}), nil
}
func (f *DeviceFixture) expire() {
	if f.preview != nil && f.state == v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING && !f.now().Before(f.preview.ExpiresAt.AsTime()) {
		f.state = v1.ToolLoginState_TOOL_LOGIN_STATE_EXPIRED
	}
}
func capsEqual(a, b []v1.ToolCapability) bool {
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
func validSubset(c, requested []v1.ToolCapability) bool {
	if len(c) == 0 || c[0] != 1 {
		return false
	}
	for i, cap := range c {
		if !slices.Contains(requested, cap) || (i > 0 && cap == c[i-1]) || (cap == 3 && !slices.Contains(c, v1.ToolCapability_TOOL_CAPABILITY_PREPARE)) {
			return false
		}
	}
	return true
}

// normalizeIntent mirrors the frozen public fingerprint and normalization policy,
// not server-side authorization or persistence. Its output is tested against the
// unchanged backend reference vector, rather than a fixture-specific substitute.
func normalizeIntent(input *v1.BeginLoginRequest) (*v1.BeginLoginRequest, []byte, error) {
	if input == nil {
		return nil, nil, errors.New("missing intent")
	}
	in := proto.Clone(input).(*v1.BeginLoginRequest)
	id, err := hex.DecodeString(strings.ReplaceAll(in.RequestId, "-", ""))
	if err != nil || len(id) != 16 || len(in.RequestId) != 36 || in.RequestId[8] != '-' || in.RequestId[13] != '-' || in.RequestId[18] != '-' || in.RequestId[23] != '-' || strings.ToLower(in.RequestId) != in.RequestId || bytes.Equal(id, make([]byte, 16)) || len(in.SecretDigest) != 32 || len(in.ProofDigest) != 32 || bytes.Equal(in.SecretDigest, in.ProofDigest) || !utf8.ValidString(in.Label) || len(in.Label) > 320 || utf8.RuneCountInString(in.Label) > 80 || strings.TrimSpace(in.Label) == "" {
		return nil, nil, errors.New("invalid intent")
	}
	for _, r := range in.Label {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return nil, nil, errors.New("invalid label")
		}
	}
	slices.Sort(in.Capabilities)
	if len(in.Capabilities) == 0 || in.Capabilities[0] != 1 {
		return nil, nil, errors.New("invalid capabilities")
	}
	for i, c := range in.Capabilities {
		if c < 1 || c > 4 || (i > 0 && c == in.Capabilities[i-1]) || (c == 3 && !slices.Contains(in.Capabilities, v1.ToolCapability_TOOL_CAPABILITY_PREPARE)) {
			return nil, nil, errors.New("invalid capabilities")
		}
	}
	if in.ExpiresInSeconds == 0 {
		in.ExpiresInSeconds = 2592000
	}
	if in.ExpiresInSeconds < 300 || in.ExpiresInSeconds > 7776000 {
		return nil, nil, errors.New("invalid duration")
	}
	var out bytes.Buffer
	out.WriteString("syntra.tool-login\x00\x01")
	out.Write(id)
	out.Write(in.SecretDigest)
	out.Write(in.ProofDigest)
	_ = binary.Write(&out, binary.BigEndian, uint32(len(in.Label)))
	out.WriteString(in.Label)
	_ = binary.Write(&out, binary.BigEndian, in.ExpiresInSeconds)
	_ = binary.Write(&out, binary.BigEndian, uint32(len(in.Capabilities)))
	for _, c := range in.Capabilities {
		_ = binary.Write(&out, binary.BigEndian, int32(c))
	}
	fingerprint := sha256.Sum256(out.Bytes())
	return in, fingerprint[:], nil
}
