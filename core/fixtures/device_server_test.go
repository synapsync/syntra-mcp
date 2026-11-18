package fixtures

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

func setup(t *testing.T) (syntrav1connect.ToolLoginServiceClient, func(time.Duration), []byte, *v1.ToolLoginRequest) {
	t.Helper()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	f := NewDeviceFixture(func() time.Time { return now })
	server := httptest.NewTLSServer(f.Handler())
	t.Cleanup(server.Close)
	client := syntrav1connect.NewToolLoginServiceClient(server.Client(), server.URL)
	proof := make([]byte, 32)
	for i := range proof {
		proof[i] = byte(i)
	}
	proofDigest := sha256.Sum256(proof)
	toolBytes := make([]byte, 32)
	for i := range toolBytes {
		toolBytes[i] = byte(i + 64)
	}
	if bytes.Equal(toolBytes, proof) {
		t.Fatal("fixture secrets must be independent patterns")
	}
	token := "syntra_tool_" + base64.RawURLEncoding.EncodeToString(toolBytes)
	secretDigest := sha256.Sum256([]byte(token))
	begin := &v1.BeginLoginRequest{RequestId: RequestID, SecretDigest: secretDigest[:], ProofDigest: proofDigest[:], Label: "Work laptop", Capabilities: []v1.ToolCapability{1, 2, 3}, ExpiresInSeconds: 2592000}
	first, err := client.BeginLogin(context.Background(), connect.NewRequest(begin))
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.BeginLogin(context.Background(), connect.NewRequest(proto.Clone(begin).(*v1.BeginLoginRequest)))
	if err != nil || !proto.Equal(first.Msg, second.Msg) {
		t.Fatal("exact Begin recovery differs", err)
	}
	if first.Msg.VerificationUri != "https://syntra.example.test/auth/device" || first.Msg.IntervalSeconds != 5 {
		t.Fatal("unexpected browser metadata")
	}
	_, expectedFingerprint, err := normalizeIntent(begin)
	if err != nil || !bytes.Equal(first.Msg.Request.Fingerprint, expectedFingerprint) {
		t.Fatal("Begin fingerprint does not bind canonical intent", err)
	}
	normalized := proto.Clone(begin).(*v1.BeginLoginRequest)
	normalized.Capabilities = []v1.ToolCapability{3, 1, 2}
	normalized.ExpiresInSeconds = 0
	normalizedRetry, err := client.BeginLogin(context.Background(), connect.NewRequest(normalized))
	if err != nil || !proto.Equal(first.Msg, normalizedRetry.Msg) {
		t.Fatal("normalized exact retry differs", err)
	}
	begin.Label = "Changed laptop"
	if _, err = client.BeginLogin(context.Background(), connect.NewRequest(begin)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal("changed retry accepted", err)
	}
	return client, func(delta time.Duration) { now = now.Add(delta) }, proof, first.Msg.Request
}

type activeTestHarness struct {
	login       syntrav1connect.ToolLoginServiceClient
	connections syntrav1connect.ToolConnectionServiceClient
	requestID   string
	proof       []byte
	toolSecret  []byte
	toolToken   string
	connection  *v1.ToolConnection
}

func setupActiveTestHarness(t *testing.T, transform func(*v1.ToolConnection) *v1.ToolConnection) *activeTestHarness {
	t.Helper()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	const requestID = "a1234567-89ab-4cde-8fab-0123456789ab"
	const verificationURI = "https://syntra.example.test/test/device"
	fixture, err := NewDeviceFixtureForTestHarness(func() time.Time { return now }, TestHarnessOptions{
		AcceptFirstValidRequestID: true,
		VerificationURI:           verificationURI,
		IdentityMetadataTransform: transform,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(fixture.Handler())
	t.Cleanup(server.Close)
	login := syntrav1connect.NewToolLoginServiceClient(server.Client(), server.URL)
	connections := syntrav1connect.NewToolConnectionServiceClient(server.Client(), server.URL)

	proof := make([]byte, 32)
	toolSecret := make([]byte, 32)
	for i := range proof {
		proof[i] = byte(i)
		toolSecret[i] = byte(i + 64)
	}
	if bytes.Equal(proof, toolSecret) {
		t.Fatal("fixture proof and tool credential must be independent")
	}
	toolToken := "syntra_tool_" + base64.RawURLEncoding.EncodeToString(toolSecret)
	proofDigest := sha256.Sum256(proof)
	secretDigest := sha256.Sum256([]byte(toolToken))
	begin := &v1.BeginLoginRequest{
		RequestId:        requestID,
		SecretDigest:     secretDigest[:],
		ProofDigest:      proofDigest[:],
		Label:            "Activation test",
		Capabilities:     []v1.ToolCapability{1, 2, 3},
		ExpiresInSeconds: 2592000,
	}
	invalid := proto.Clone(begin).(*v1.BeginLoginRequest)
	invalid.RequestId = "not-a-uuid"
	if _, err := login.BeginLogin(context.Background(), connect.NewRequest(invalid)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal("test harness accepted an invalid request UUID", err)
	}
	started, err := login.BeginLogin(context.Background(), connect.NewRequest(begin))
	if err != nil {
		t.Fatal(err)
	}
	if started.Msg.Request.RequestId != requestID || started.Msg.VerificationUri != verificationURI {
		t.Fatal("test harness did not bind the first valid request UUID and verification URI")
	}
	retry, err := login.BeginLogin(context.Background(), connect.NewRequest(proto.Clone(begin).(*v1.BeginLoginRequest)))
	if err != nil || !proto.Equal(started.Msg, retry.Msg) {
		t.Fatal("test harness exact Begin retry differs", err)
	}
	different := proto.Clone(begin).(*v1.BeginLoginRequest)
	different.RequestId = "b1234567-89ab-4cde-8fab-0123456789ab"
	if _, err := login.BeginLogin(context.Background(), connect.NewRequest(different)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal("test harness did not bind its first request UUID", err)
	}

	pollRequest := func(secret []byte) (*connect.Response[v1.PollLoginResponse], error) {
		req := connect.NewRequest(&v1.PollLoginRequest{RequestId: requestID})
		req.Header().Set("Authorization", "SyntraLogin "+base64.RawURLEncoding.EncodeToString(secret))
		return login.PollLogin(context.Background(), req)
	}
	pending, err := pollRequest(proof)
	if err != nil || pending.Msg.State != v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING {
		t.Fatal("proof credential did not reach pending state", err)
	}
	if _, err := pollRequest(toolSecret); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal("tool credential was accepted as the proof credential", err)
	}

	consent := connect.NewRequest(&v1.GetLoginRequestRequest{UserCode: UserCode})
	consent.Header().Set("Authorization", HumanAuthorization)
	preview, err := login.GetLoginRequest(context.Background(), consent)
	if err != nil || !proto.Equal(preview.Msg.Request, started.Msg.Request) {
		t.Fatal("consent preview differs from Begin", err)
	}
	approve := connect.NewRequest(&v1.ApproveLoginRequest{
		UserCode:            UserCode,
		ExpectedFingerprint: preview.Msg.Request.Fingerprint,
		Capabilities:        []v1.ToolCapability{1},
		ExpiresInSeconds:    300,
	})
	approve.Header().Set("Authorization", HumanAuthorization)
	issued, err := login.ApproveLogin(context.Background(), approve)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Msg.Connection.State != v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE || len(issued.Msg.Connection.Capabilities) != 1 || issued.Msg.Connection.Capabilities[0] != 1 {
		t.Fatal("fixture did not issue the reduced active grant", issued.Msg.Connection)
	}
	now = now.Add(5 * time.Second)
	approved, err := pollRequest(proof)
	if err != nil || approved.Msg.State != v1.ToolLoginState_TOOL_LOGIN_STATE_APPROVED || !proto.Equal(approved.Msg.Connection, issued.Msg.Connection) {
		t.Fatal("approval was not recovered with the proof credential", err)
	}

	return &activeTestHarness{
		login:       login,
		connections: connections,
		requestID:   requestID,
		proof:       proof,
		toolSecret:  toolSecret,
		toolToken:   toolToken,
		connection:  proto.Clone(approved.Msg.Connection).(*v1.ToolConnection),
	}
}

func poll(client syntrav1connect.ToolLoginServiceClient, proof []byte) (*connect.Response[v1.PollLoginResponse], error) {
	req := connect.NewRequest(&v1.PollLoginRequest{RequestId: RequestID})
	req.Header().Set("Authorization", "SyntraLogin "+base64.RawURLEncoding.EncodeToString(proof))
	return client.PollLogin(context.Background(), req)
}
func TestDeviceClientPendingApprovalRecoveryAndReducedCapabilities(t *testing.T) {
	client, advance, proof, preview := setup(t)
	got, err := poll(client, proof)
	if err != nil || got.Msg.State != v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING {
		t.Fatal(got, err)
	}
	if _, err = poll(client, proof); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatal("poll interval ignored", err)
	}
	var throttle *connect.Error
	if !errors.As(err, &throttle) || throttle.Meta().Get("Retry-After") != "10" {
		t.Fatal("slow_down did not increase the interval", err)
	}
	advance(10 * time.Second)
	get := connect.NewRequest(&v1.GetLoginRequestRequest{UserCode: UserCode})
	get.Header().Set("Authorization", HumanAuthorization)
	view, err := client.GetLoginRequest(context.Background(), get)
	if err != nil || !proto.Equal(view.Msg.Request, preview) {
		t.Fatal("consent preview differs", err)
	}
	approve := connect.NewRequest(&v1.ApproveLoginRequest{UserCode: UserCode, ExpectedFingerprint: preview.Fingerprint, Capabilities: []v1.ToolCapability{1}, ExpiresInSeconds: 300})
	approve.Header().Set("Authorization", HumanAuthorization)
	issued, err := client.ApproveLogin(context.Background(), approve)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := client.ApproveLogin(context.Background(), approve)
	if err != nil || !proto.Equal(issued.Msg, recovered.Msg) {
		t.Fatal("approval exact retry differs", err)
	}
	got, err = poll(client, proof)
	if err != nil || got.Msg.State != v1.ToolLoginState_TOOL_LOGIN_STATE_APPROVED || !proto.Equal(got.Msg.Connection, issued.Msg.Connection) {
		t.Fatal("approved result not recovered", err)
	}
	if len(got.Msg.Connection.Capabilities) != 1 || got.Msg.Connection.Capabilities[0] != 1 {
		t.Fatal("fixture elevated permissions")
	}
	bad := append([]byte(nil), proof...)
	bad[0] ^= 1
	if _, err = poll(client, bad); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal("wrong private proof accepted", err)
	}
}

func TestDeviceFixtureActivationGetsIdentityWithIssuedToolCredential(t *testing.T) {
	harness := setupActiveTestHarness(t, nil)
	req := connect.NewRequest(&v1.GetIdentityRequest{})
	req.Header().Set("Authorization", "Bearer "+harness.toolToken)
	got, err := harness.connections.GetIdentity(context.Background(), req)
	if err != nil || !proto.Equal(got.Msg.Connection, harness.connection) {
		t.Fatal("GetIdentity did not return the same active grant", err)
	}
	if got.Msg.Connection.State != v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE || len(got.Msg.Connection.Capabilities) != 1 || got.Msg.Connection.Capabilities[0] != 1 {
		t.Fatal("GetIdentity changed the consented reduced scope", got.Msg.Connection)
	}

	wrongSecret := append([]byte(nil), harness.toolSecret...)
	wrongSecret[0] ^= 1
	proofAsToolToken := "syntra_tool_" + base64.RawURLEncoding.EncodeToString(harness.proof)
	for _, token := range []string{
		"syntra_tool_" + base64.RawURLEncoding.EncodeToString(wrongSecret),
		proofAsToolToken,
	} {
		request := connect.NewRequest(&v1.GetIdentityRequest{})
		request.Header().Set("Authorization", "Bearer "+token)
		if _, err := harness.connections.GetIdentity(context.Background(), request); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatal("wrong or proof credential accepted by GetIdentity", err)
		}
	}
}

func TestDeviceFixtureTestHarnessCanSubstituteIdentityMetadata(t *testing.T) {
	harness := setupActiveTestHarness(t, func(connection *v1.ToolConnection) *v1.ToolConnection {
		connection.Capabilities = []v1.ToolCapability{1, 2}
		return connection
	})
	req := connect.NewRequest(&v1.GetIdentityRequest{})
	req.Header().Set("Authorization", "Bearer "+harness.toolToken)
	got, err := harness.connections.GetIdentity(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	expected := proto.Clone(harness.connection).(*v1.ToolConnection)
	expected.Capabilities = []v1.ToolCapability{1, 2}
	if !proto.Equal(got.Msg.Connection, expected) || proto.Equal(got.Msg.Connection, harness.connection) {
		t.Fatal("test harness did not return its substituted capability metadata", got.Msg.Connection)
	}
}

func TestDeviceFixtureTestHarnessRejectsUntrustedVerificationURI(t *testing.T) {
	if _, err := NewDeviceFixtureForTestHarness(nil, TestHarnessOptions{VerificationURI: "https://example.com/device"}); err == nil {
		t.Fatal("test harness accepted a non-test verification host")
	}
}

func TestDeviceClientDeniedAndExpired(t *testing.T) {
	t.Run("denied", func(t *testing.T) {
		client, _, proof, preview := setup(t)
		deny := connect.NewRequest(&v1.DenyLoginRequest{UserCode: UserCode, ExpectedFingerprint: preview.Fingerprint})
		deny.Header().Set("Authorization", HumanAuthorization)
		if _, err := client.DenyLogin(context.Background(), deny); err != nil {
			t.Fatal(err)
		}
		got, err := poll(client, proof)
		if err != nil || got.Msg.State != v1.ToolLoginState_TOOL_LOGIN_STATE_DENIED || got.Msg.Connection != nil {
			t.Fatal(got, err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		client, advance, proof, _ := setup(t)
		advance(10 * time.Minute)
		got, err := poll(client, proof)
		if err != nil || got.Msg.State != v1.ToolLoginState_TOOL_LOGIN_STATE_EXPIRED || got.Msg.Connection != nil {
			t.Fatal(got, err)
		}
	})
}
func TestDeviceClientRejectsStaleConsentAndNonhuman(t *testing.T) {
	client, _, _, preview := setup(t)
	req := connect.NewRequest(&v1.GetLoginRequestRequest{UserCode: UserCode})
	if _, err := client.GetLoginRequest(context.Background(), req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
	approve := connect.NewRequest(&v1.ApproveLoginRequest{UserCode: UserCode, ExpectedFingerprint: make([]byte, 32), Capabilities: []v1.ToolCapability{1, 2, 3}, ExpiresInSeconds: 2592000})
	approve.Header().Set("Authorization", HumanAuthorization)
	if _, err := client.ApproveLogin(context.Background(), approve); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal("stale fingerprint accepted", err)
	}
	approve.Msg.ExpectedFingerprint = preview.Fingerprint
	approve.Msg.Capabilities = []v1.ToolCapability{1, 3}
	if _, err := client.ApproveLogin(context.Background(), approve); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal("publish without prepare accepted", err)
	}
}

func TestDeviceFingerprintMatchesUnchangedBackendVector(t *testing.T) {
	raw, err := os.ReadFile("../../reference/device-fingerprint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		RequestID    string              `json:"requestId"`
		SecretDigest string              `json:"secretDigestHex"`
		ProofDigest  string              `json:"proofDigestHex"`
		Label        string              `json:"label"`
		Capabilities []v1.ToolCapability `json:"capabilities"`
		Lifetime     int32               `json:"expiresInSeconds"`
		Fingerprint  string              `json:"fingerprintHex"`
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	secret, err := hex.DecodeString(v.SecretDigest)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := hex.DecodeString(v.ProofDigest)
	if err != nil {
		t.Fatal(err)
	}
	in := &v1.BeginLoginRequest{RequestId: v.RequestID, SecretDigest: secret, ProofDigest: proof, Label: v.Label, Capabilities: v.Capabilities, ExpiresInSeconds: v.Lifetime}
	_, hash, err := normalizeIntent(in)
	if err != nil || hex.EncodeToString(hash) != v.Fingerprint {
		t.Fatal("frozen backend fingerprint mismatch", err)
	}
	in.Capabilities = []v1.ToolCapability{3, 2, 1}
	in.ExpiresInSeconds = 0
	normalized, again, err := normalizeIntent(in)
	if err != nil || !bytes.Equal(hash, again) || normalized.ExpiresInSeconds != 2592000 || !capsEqual(normalized.Capabilities, []v1.ToolCapability{1, 2, 3}) {
		t.Fatal("normalization changed fingerprint", err)
	}
	in.Label = "Work laptop "
	_, changed, err := normalizeIntent(in)
	if err != nil || bytes.Equal(hash, changed) {
		t.Fatal("label bytes not bound", err)
	}
}

func TestDeviceFixtureNormalizationRejectsInvalidIntent(t *testing.T) {
	raw, err := os.ReadFile("../../reference/device-fingerprint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector map[string]any
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	secret, _ := hex.DecodeString(vector["secretDigestHex"].(string))
	proof, _ := hex.DecodeString(vector["proofDigestHex"].(string))
	base := &v1.BeginLoginRequest{RequestId: RequestID, SecretDigest: secret, ProofDigest: proof, Label: "Work laptop", Capabilities: []v1.ToolCapability{1, 2, 3}, ExpiresInSeconds: 2592000}
	for _, change := range []func(*v1.BeginLoginRequest){
		func(in *v1.BeginLoginRequest) { in.Capabilities = []v1.ToolCapability{1, 1} },
		func(in *v1.BeginLoginRequest) { in.Capabilities = []v1.ToolCapability{1, 3} },
		func(in *v1.BeginLoginRequest) { in.Capabilities = []v1.ToolCapability{1, 5} },
		func(in *v1.BeginLoginRequest) { in.Label = "   " },
		func(in *v1.BeginLoginRequest) { in.Label = "line\n" },
		func(in *v1.BeginLoginRequest) { in.ProofDigest = append([]byte(nil), in.SecretDigest...) },
		func(in *v1.BeginLoginRequest) { in.ExpiresInSeconds = 299 },
	} {
		in := proto.Clone(base).(*v1.BeginLoginRequest)
		change(in)
		if _, _, err = normalizeIntent(in); err == nil {
			t.Fatal("invalid intent accepted")
		}
	}
	if validSubset([]v1.ToolCapability{1, 2}, []v1.ToolCapability{1}) {
		t.Fatal("scope escalation accepted")
	}
}
