package toolmcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"syntra.local/mcp/fixtures"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
	ci "syntra.local/mcp/internal/contentintegrity"
	"syntra.local/mcp/internal/contentintegrity/actionable"
	"syntra.local/mcp/internal/testsupport"
	"syntra.local/mcp/internal/toolclient"
)

const ownerID = "11111111-1111-4111-8111-111111111111"
const recipientID = "22222222-2222-4222-8222-222222222222"
const connectionID = "33333333-3333-4333-8333-333333333333"
const workspaceID = "44444444-4444-4444-8444-444444444444"
const secondWorkspaceID = "66666666-6666-4666-8666-666666666666"
const projectID = "55555555-5555-4555-8555-555555555555"
const ticketID = "77777777-7777-4777-8777-777777777777"
const draftID = "88888888-8888-4888-8888-888888888888"
const messageID = "99999999-9999-4999-8999-999999999999"

func id() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func str(s string) *string { return &s }
func revision(n byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32))
}
func contentDigest(c *v1.AuthoredContent) []byte {
	x, _ := ci.FromProto(c)
	d, _ := ci.Digest(x)
	return d[:]
}
func newRequest() *v1.PrepareContentRequest {
	return &v1.PrepareContentRequest{RequestId: id(), Content: &v1.AuthoredContent{FormatVersion: 1, ExpectedAuthorUserId: ownerID, WorkspaceId: workspaceID, Title: "Exact e\u0301", OriginalDestinationIntent: "  Pedro e\u0301  ", Destination: &v1.ContentDestination{Target: &v1.ContentDestination_UnlinkedLabel{UnlinkedLabel: "Pedro"}}, Summary: " e\u0301\r\n exact ", ContextMarkdown: "```go\r\nvar endpoint = `/records`;\r\n```\r\n$HOME is ticket data"}, ClientReported: &v1.ClientReportedProvenance{Model: str("first-model"), Tool: str("fixture")}}
}

type apiFixture struct {
	syntrav1connect.UnimplementedToolConnectionServiceHandler
	syntrav1connect.UnimplementedAuthoredContentServiceHandler
	syntrav1connect.UnimplementedProjectServiceHandler
	syntrav1connect.UnimplementedWorkspaceServiceHandler
	syntrav1connect.UnimplementedTicketServiceHandler
	ticketClosed  bool
	mutationError error
	mu            sync.Mutex
	p             *toolclient.Profile
	attempts      map[string]proto.Message
	snapshots     map[string]*v1.AuthoredContentSnapshot
	mutations     int
	identities    int
	duplicate     bool
	big           int
	hold          bool
	held          chan struct{}
	forbidden     bool
	workspaces    []*v1.AccessibleWorkspace
	publishes     int
}

func (f *apiFixture) GetIdentity(_ context.Context, r *connect.Request[v1.GetIdentityRequest]) (*connect.Response[v1.GetIdentityResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identities++
	if f.forbidden || r.Header().Get("Authorization") != "Bearer "+f.p.Secret {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture-private-provider-error"))
	}
	a, _ := time.Parse(time.RFC3339Nano, f.p.CreatedAt)
	b, _ := time.Parse(time.RFC3339Nano, f.p.ExpiresAt)
	return connect.NewResponse(&v1.GetIdentityResponse{Connection: &v1.ToolConnection{Id: f.p.ConnectionID, OwnerUserId: f.p.AccountID, Label: f.p.Config.Label, Capabilities: f.p.Config.Capabilities, CreatedAt: timestamppb.New(a), ExpiresAt: timestamppb.New(b), State: v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE}}), nil
}
func (f *apiFixture) secret() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.p == nil {
		return ""
	}
	return f.p.Secret
}
func (f *apiFixture) ListAccessibleWorkspaces(context.Context, *connect.Request[v1.ListAccessibleWorkspacesRequest]) (*connect.Response[v1.ListAccessibleWorkspacesResponse], error) {
	f.mu.Lock()
	owner := ownerID
	if f.p != nil {
		owner = f.p.AccountID
	}
	workspaces := f.workspaces
	if len(workspaces) == 0 {
		workspaces = []*v1.AccessibleWorkspace{{WorkspaceId: workspaceID, Name: "Labs"}}
	}
	result := make([]*v1.AccessibleWorkspace, 0, len(workspaces))
	for _, workspace := range workspaces {
		copy := proto.Clone(workspace).(*v1.AccessibleWorkspace)
		copy.MemberUserId = owner
		result = append(result, copy)
	}
	f.mu.Unlock()
	return connect.NewResponse(&v1.ListAccessibleWorkspacesResponse{Workspaces: result}), nil
}
func (f *apiFixture) ListProjects(ctx context.Context, r *connect.Request[v1.ListProjectsRequest]) (*connect.Response[v1.ListProjectsResponse], error) {
	f.mu.Lock()
	duplicate, big, hold := f.duplicate, f.big, f.hold
	if hold {
		f.hold = false
		close(f.held)
	}
	f.mu.Unlock()
	if hold {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	name := "App"
	key := projectID
	if r.Msg.PageToken != "" {
		key = recipientID
		if !duplicate {
			name = "Other"
		}
	}
	next := ""
	if r.Msg.PageToken == "" && (duplicate || big > 400000) {
		next = "later"
	}
	if big > 400000 {
		big = 400000
	}
	return connect.NewResponse(&v1.ListProjectsResponse{Projects: []*v1.Project{{Id: key, WorkspaceId: workspaceID, Name: name, Description: strings.Repeat("x", big)}}, NextPageToken: next}), nil
}
func (f *apiFixture) ListMembers(context.Context, *connect.Request[v1.ListMembersRequest]) (*connect.Response[v1.ListMembersResponse], error) {
	return connect.NewResponse(&v1.ListMembersResponse{Members: []*v1.WorkspaceMember{{UserId: recipientID, DisplayName: "Pedro"}}}), nil
}
func (f *apiFixture) GetTicket(_ context.Context, r *connect.Request[v1.GetTicketRequest]) (*connect.Response[v1.GetTicketResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := v1.TicketState_TICKET_STATE_OPEN
	if f.ticketClosed {
		state = v1.TicketState_TICKET_STATE_CLOSED
	}
	ticket := &v1.Ticket{Id: r.Msg.TicketId, WorkspaceId: r.Msg.WorkspaceId, Title: "Visible title", State: state, Revision: revision(1)}
	return connect.NewResponse(&v1.GetTicketResponse{Ticket: ticket}), nil
}

func (f *apiFixture) apply(id string, req proto.Message, c *v1.AuthoredContent, p *v1.ClientReportedProvenance, publish bool) (*v1.AuthoredContentSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mutations++
	if f.mutationError != nil {
		return nil, f.mutationError
	}
	if old := f.attempts[id]; old != nil && !proto.Equal(old, req) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("private-request-mismatch"))
	}
	if old := f.snapshots[id]; old != nil {
		return proto.Clone(old).(*v1.AuthoredContentSnapshot), nil
	}
	owner, connection := ownerID, connectionID
	if f.p != nil {
		owner, connection = f.p.AccountID, f.p.ConnectionID
	}
	s := &v1.AuthoredContentSnapshot{Content: proto.Clone(c).(*v1.AuthoredContent), Receipt: &v1.ContentProvenanceReceipt{ContentDigest: contentDigest(c), OwnerUserId: owner, ConnectionId: str(connection), RequestId: id, TicketId: ticketID, RecordedAt: timestamppb.Now(), ClientReported: p, IntegrityOrigin: v1.ContentIntegrityOrigin_CONTENT_INTEGRITY_ORIGIN_CLIENT_EXPECTED}, TicketRevision: revision(byte(len(f.snapshots) + 1)), Record: &v1.AuthoredContentSnapshot_PrivateDraft{PrivateDraft: &v1.PrivateContentRevision{DraftId: draftID, Revision: revision(byte(len(f.snapshots) + 1))}}}
	if publish {
		s.Receipt.MessageId = messageID
		s.Record = &v1.AuthoredContentSnapshot_PublishedMessageId{PublishedMessageId: messageID}
	}
	f.attempts[id] = proto.Clone(req)
	f.snapshots[id] = s
	return proto.Clone(s).(*v1.AuthoredContentSnapshot), nil
}
func (f *apiFixture) PrepareContent(_ context.Context, r *connect.Request[v1.PrepareContentRequest]) (*connect.Response[v1.PrepareContentResponse], error) {
	s, e := f.apply(r.Msg.RequestId, r.Msg, r.Msg.Content, r.Msg.ClientReported, false)
	return connect.NewResponse(&v1.PrepareContentResponse{Snapshot: s}), e
}
func (f *apiFixture) LinkContent(_ context.Context, r *connect.Request[v1.LinkContentRequest]) (*connect.Response[v1.LinkContentResponse], error) {
	s, e := f.apply(r.Msg.RequestId, r.Msg, r.Msg.LinkedContent, r.Msg.ClientReported, false)
	return connect.NewResponse(&v1.LinkContentResponse{Snapshot: s}), e
}
func (f *apiFixture) PublishContent(_ context.Context, r *connect.Request[v1.PublishContentRequest]) (*connect.Response[v1.PublishContentResponse], error) {
	f.mu.Lock()
	f.publishes++
	var c *v1.AuthoredContent
	for _, s := range f.snapshots {
		if bytes.Equal(s.Receipt.ContentDigest, r.Msg.Current.ExpectedContentDigest) {
			c = s.Content
			break
		}
	}
	f.mu.Unlock()
	if c == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("private-no-draft"))
	}
	s, e := f.apply(r.Msg.RequestId, r.Msg, c, r.Msg.ClientReported, true)
	return connect.NewResponse(&v1.PublishContentResponse{Snapshot: s}), e
}
func (f *apiFixture) GetContentSnapshot(_ context.Context, r *connect.Request[v1.GetContentSnapshotRequest]) (*connect.Response[v1.GetContentSnapshotResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.snapshots {
		if s.GetPublishedMessageId() == r.Msg.GetPublishedMessageId() && s.GetPublishedMessageId() != "" {
			return connect.NewResponse(&v1.GetContentSnapshotResponse{Representation: &v1.GetContentSnapshotResponse_Authored{Authored: s}}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("private-not-found"))
}

type setup struct {
	api                     *apiFixture
	dir, ca, binary, apiURL string
}

type observedBrowserFixture struct {
	*fixtures.DeviceFixture
	mu            sync.Mutex
	beginRequests []*v1.BeginLoginRequest
	beginEvents   chan struct{}
	dropBegin     atomic.Bool
	approvals     atomic.Int32
}

func (f *observedBrowserFixture) BeginLogin(ctx context.Context, request *connect.Request[v1.BeginLoginRequest]) (*connect.Response[v1.BeginLoginResponse], error) {
	response, err := f.DeviceFixture.BeginLogin(ctx, request)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.beginRequests = append(f.beginRequests, proto.Clone(request.Msg).(*v1.BeginLoginRequest))
	f.mu.Unlock()
	select {
	case f.beginEvents <- struct{}{}:
	default:
	}
	if f.dropBegin.CompareAndSwap(true, false) {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("synthetic Begin response lost after commit"))
	}
	return response, nil
}

func (f *observedBrowserFixture) requests() []*v1.BeginLoginRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	requests := make([]*v1.BeginLoginRequest, len(f.beginRequests))
	for i, request := range f.beginRequests {
		requests[i] = proto.Clone(request).(*v1.BeginLoginRequest)
	}
	return requests
}

type browserIdentityAdapter struct {
	*apiFixture
	device        *observedBrowserFixture
	apiOrigin     string
	browserOrigin string
}

func (a *browserIdentityAdapter) GetIdentity(ctx context.Context, request *connect.Request[v1.GetIdentityRequest]) (*connect.Response[v1.GetIdentityResponse], error) {
	response, err := a.device.GetIdentity(ctx, request)
	if err != nil {
		return nil, err
	}
	connection := response.Msg.Connection
	secret := strings.TrimPrefix(request.Header().Get("Authorization"), "Bearer ")
	if secret == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("synthetic browser token missing"))
	}
	requests := a.device.requests()
	if len(requests) == 0 {
		return nil, connect.NewError(connect.CodeInternal, errors.New("synthetic Begin intent missing"))
	}
	lifetime := int32(connection.ExpiresAt.AsTime().Sub(connection.CreatedAt.AsTime()) / time.Second)
	profile := &toolclient.Profile{
		Version: 2, Method: "browser", BrowserOrigin: a.browserOrigin,
		Config: toolclient.Config{
			APIOrigin: a.apiOrigin, Label: connection.Label,
			Capabilities: append([]v1.ToolCapability(nil), connection.Capabilities...), LifetimeSeconds: lifetime,
		},
		AccountID: connection.OwnerUserId, RequestID: requests[0].RequestId, Secret: secret,
		State: "active", ConnectionID: connection.Id,
		CreatedAt: connection.CreatedAt.AsTime().Format(time.RFC3339Nano),
		ExpiresAt: connection.ExpiresAt.AsTime().Format(time.RFC3339Nano),
	}
	a.apiFixture.mu.Lock()
	if a.apiFixture.p == nil {
		a.apiFixture.p = profile
	} else if a.apiFixture.p.Version != profile.Version || a.apiFixture.p.Method != profile.Method || a.apiFixture.p.BrowserOrigin != profile.BrowserOrigin || a.apiFixture.p.Secret != profile.Secret || a.apiFixture.p.RequestID != profile.RequestID || a.apiFixture.p.AccountID != profile.AccountID || a.apiFixture.p.ConnectionID != profile.ConnectionID || a.apiFixture.p.Config.APIOrigin != profile.Config.APIOrigin || a.apiFixture.p.Config.Label != profile.Config.Label || a.apiFixture.p.Config.LifetimeSeconds != profile.Config.LifetimeSeconds || !equalCapabilities(a.apiFixture.p.Config.Capabilities, profile.Config.Capabilities) {
		a.apiFixture.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("synthetic identity differs from the installed profile"))
	}
	a.apiFixture.mu.Unlock()
	return response, nil
}

func equalCapabilities(a, b []v1.ToolCapability) bool {
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

func browserProcessFixture(t *testing.T) (*setup, *observedBrowserFixture) {
	t.Helper()
	root := t.TempDir()
	api := &apiFixture{
		attempts: map[string]proto.Message{}, snapshots: map[string]*v1.AuthoredContentSnapshot{}, held: make(chan struct{}),
		workspaces: []*v1.AccessibleWorkspace{
			{WorkspaceId: workspaceID, Name: "Labs"},
			{WorkspaceId: secondWorkspaceID, Name: "Notes"},
		},
	}
	fixedNow := time.Now().UTC().Truncate(time.Second)
	device, err := fixtures.NewDeviceFixtureForTestHarness(func() time.Time { return fixedNow }, fixtures.TestHarnessOptions{
		AcceptFirstValidRequestID: true, VerificationURI: "https://syntra.example.test/auth/device",
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := &observedBrowserFixture{DeviceFixture: device, beginEvents: make(chan struct{}, 4)}
	observed.dropBegin.Store(true)
	identity := &browserIdentityAdapter{apiFixture: api, device: observed, browserOrigin: "https://syntra.example.test"}
	mux := http.NewServeMux()
	loginPath, loginHandler := syntrav1connect.NewToolLoginServiceHandler(observed)
	connectionPath, connectionHandler := syntrav1connect.NewToolConnectionServiceHandler(identity)
	mux.Handle(loginPath, loginHandler)
	mux.Handle(connectionPath, connectionHandler)
	mux.Handle(syntrav1connect.NewAuthoredContentServiceHandler(api))
	mux.Handle(syntrav1connect.NewProjectServiceHandler(api))
	mux.Handle(syntrav1connect.NewWorkspaceServiceHandler(api))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	identity.apiOrigin = server.URL
	store, err := toolclient.OpenStore(context.Background(), filepath.Join(root, "profile"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	binary := testsupport.BuildCLI(t, "../../cmd/syntra", server.Certificate())
	return &setup{api: api, dir: filepath.Join(root, "profile"), binary: binary, apiURL: server.URL}, observed
}

type commandResult struct {
	output []byte
	err    error
}

func runBrowserCLI(f *setup) (*exec.Cmd, <-chan commandResult) {
	cmd := exec.Command(f.binary, "login", "--profile-dir", f.dir, "--api", f.apiURL, "--browser-origin", "https://syntra.example.test", "--no-browser", "--lifetime-seconds", "600", "--timeout", "30s")
	cmd.Env = testsupport.FixtureCLIEnv()
	result := make(chan commandResult, 1)
	var stdout, stderr safeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		result <- commandResult{err: err}
		return cmd, result
	}
	go func() {
		err := cmd.Wait()
		result <- commandResult{output: []byte(stdout.String() + stderr.String()), err: err}
	}()
	return cmd, result
}

func safeBrowserOutput(output []byte, attempt *toolclient.PendingBrowserAttempt) string {
	redacted := string(output)
	if attempt != nil {
		redacted = strings.ReplaceAll(redacted, attempt.ToolSecret, "[tool credential redacted]")
		redacted = strings.ReplaceAll(redacted, attempt.PollProof, "[poll proof redacted]")
	}
	return redacted
}

func awaitCommand(t *testing.T, cmd *exec.Cmd, result <-chan commandResult, timeout time.Duration) commandResult {
	t.Helper()
	select {
	case done := <-result:
		return done
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case done := <-result:
			t.Fatalf("owned CLI exceeded %s: %v", timeout, done.err)
		case <-time.After(5 * time.Second):
			t.Fatalf("owned CLI did not exit after kill")
		}
	}
	return commandResult{err: errors.New("unreachable command timeout")}
}

func fixture(t *testing.T) *setup {
	t.Helper()
	root := t.TempDir()
	f := &apiFixture{attempts: map[string]proto.Message{}, snapshots: map[string]*v1.AuthoredContentSnapshot{}, held: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(syntrav1connect.NewToolConnectionServiceHandler(f))
	mux.Handle(syntrav1connect.NewAuthoredContentServiceHandler(f))
	mux.Handle(syntrav1connect.NewProjectServiceHandler(f))
	mux.Handle(syntrav1connect.NewWorkspaceServiceHandler(f))
	mux.Handle(syntrav1connect.NewTicketServiceHandler(f))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	cfg := toolclient.Config{APIOrigin: server.URL, ProviderOrigin: "https://provider.invalid", PublishableKey: "sb_publishable_fixture_public", Label: "Shared fixture", Capabilities: []v1.ToolCapability{1, 2, 3}, LifetimeSeconds: 300}
	p, e := toolclient.NewPending(cfg, ownerID)
	if e != nil {
		t.Fatal(e)
	}
	p.State = "active"
	p.ConnectionID = connectionID
	now := time.Now().UTC()
	p.CreatedAt = now.Format(time.RFC3339Nano)
	p.ExpiresAt = now.Add(300 * time.Second).Format(time.RFC3339Nano)
	f.p = p
	s, e := toolclient.OpenStore(context.Background(), filepath.Join(root, "profile"))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Save(p); e != nil {
		t.Fatal(e)
	}
	s.Close()
	binary := testsupport.BuildCLI(t, "../../cmd/syntra", server.Certificate())
	return &setup{api: f, dir: filepath.Join(root, "profile"), binary: binary, apiURL: server.URL}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.b.Len()+len(p) < 8192 {
		b.b.Write(p)
	}
	return len(p), nil
}
func (b *safeBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type process struct {
	cmd           *exec.Cmd
	in            io.WriteCloser
	lines         chan []byte
	done          chan struct{}
	waitErr       error
	outputInvalid atomic.Bool
	stderr        safeBuffer
	next          int
	f             *setup
}

func spawn(t *testing.T, f *setup, extra ...string) *process {
	t.Helper()
	args := append([]string{"mcp", "--profile-dir", f.dir}, extra...)
	p := &process{cmd: exec.Command(f.binary, args...), lines: make(chan []byte, 16), done: make(chan struct{}), f: f}
	p.cmd.Env = testsupport.FixtureCLIEnv()
	var e error
	p.in, e = p.cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := p.cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	p.cmd.Stderr = &p.stderr
	if e = p.cmd.Start(); e != nil {
		t.Fatal(e)
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 4096), MaxFrameBytes+1)
		for scanner.Scan() {
			raw := append([]byte(nil), scanner.Bytes()...)
			if !json.Valid(raw) || bytes.Contains(raw, []byte(f.api.secret())) {
				p.outputInvalid.Store(true)
				p.lines <- []byte(`{"invalidProtocolOutput":true}`)
				break
			}
			p.lines <- raw
		}
		close(p.lines)
	}()
	go func() { <-readDone; p.waitErr = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		p.in.Close()
		p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(4 * time.Second):
			t.Error("owned MCP process failed cleanup")
		}
		if p.outputInvalid.Load() {
			t.Error("stdout contained malformed protocol or credential")
		}
		if strings.Contains(p.stderr.String(), f.api.secret()) || strings.Contains(p.stderr.String(), "fixture-private") {
			t.Error("stderr leaked fixture credential/providerbody")
		}
	})
	return p
}
func (p *process) send(t *testing.T, raw []byte) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, e := p.in.Write(append(raw, '\n')); done <- e }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("MCP input failed: %v", e)
		}
	case <-time.After(5 * time.Second):
		p.cmd.Process.Kill()
		t.Fatal("MCP write hung")
	}
}
func (p *process) receive(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	select {
	case raw, ok := <-p.lines:
		if !ok {
			t.Fatal("MCP exited without reply", p.stderr.String())
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil || m["jsonrpc"] == nil || bytes.Contains(raw, []byte(p.f.api.secret())) {
			t.Fatal("nonprotocol/credential output")
		}
		return m
	case <-time.After(8 * time.Second):
		p.cmd.Process.Kill()
		t.Fatal("MCP reply timeout")
	}
	return nil
}
func (p *process) init(t *testing.T) {
	p.send(t, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture-client","version":"1"}}}`))
	m := p.receive(t)
	if m["result"] == nil {
		t.Fatal("initialize failed", string(m["error"]))
	}
	p.send(t, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	p.next = 1
}
func (p *process) call(t *testing.T, name string, args any) map[string]json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(args)
	p.next++
	p.send(t, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, p.next, name, raw)))
	return p.receive(t)
}
func toolResult(t *testing.T, m map[string]json.RawMessage, wantError bool) json.RawMessage {
	t.Helper()
	if m["error"] != nil {
		t.Fatal("protocol error", string(m["error"]))
	}
	var r struct {
		IsError    bool            `json:"isError"`
		Structured json.RawMessage `json:"structuredContent"`
		Content    []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(m["result"], &r) != nil || r.IsError != wantError {
		t.Fatalf("tool result error=%t want=%t: %s", r.IsError, wantError, m["result"])
	}
	if r.Structured != nil {
		return r.Structured
	}
	if len(r.Content) == 0 {
		t.Fatal("empty result")
	}
	return []byte(r.Content[0].Text)
}
func rpcArgs(t *testing.T, r proto.Message, base string) map[string]any {
	t.Helper()
	raw, e := protojson.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	out := map[string]any{"request": json.RawMessage(raw)}
	if base != "" {
		out["base"] = base
	}
	return out
}
func expect(s *v1.AuthoredContentSnapshot) *v1.ContentDraftExpectation {
	return &v1.ContentDraftExpectation{TicketId: s.Receipt.TicketId, DraftId: s.GetPrivateDraft().DraftId, ExpectedTicketRevision: s.TicketRevision, ExpectedDraftRevision: s.GetPrivateDraft().Revision, ExpectedContentDigest: s.Receipt.ContentDigest}
}

func TestMCPConnectionDiagnosticCLIParity(t *testing.T) {
	f := fixture(t)
	p := spawn(t, f)
	p.init(t)

	assertParity := func(tool string, toolArgs map[string]any, cliArgs []string, in []byte, want string) {
		t.Helper()
		got := string(toolResult(t, p.call(t, tool, toolArgs), true))
		cmd := exec.Command(f.binary, append(cliArgs, "--profile-dir", f.dir)...)
		cmd.Env = testsupport.FixtureCLIEnv()
		cmd.Stdin = bytes.NewReader(in)
		out, err := cmd.CombinedOutput()
		if got != want || err == nil || string(out) != want+"\n" {
			t.Fatalf("expected %q, CLI %v %q, MCP %q", want, err, out, got)
		}
		t.Log(got)
	}
	prepare := func(want string) {
		t.Helper()
		r := newRequest()
		data, err := protojson.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		assertParity("prepare", rpcArgs(t, r, ""), []string{"prepare", "--file", "-"}, data, want)
	}
	identity := toolResult(t, p.call(t, "identity", map[string]any{}), false)
	var status struct {
		ProfileDir           string
		DefaultSharedProfile bool
		CapabilityNames      []string
		DaysLeft             int
		ShortConnectionID    string
		ProfileWarning       string
		APIOrigin            string
	}
	if err := json.Unmarshal(identity, &status); err != nil || status.ProfileDir != f.dir || status.DefaultSharedProfile ||
		strings.Join(status.CapabilityNames, ",") != "read,prepare,publish" || status.DaysLeft != 1 ||
		status.ShortConnectionID != connectionID[:8] || status.APIOrigin != f.apiURL || status.ProfileWarning == "" {
		t.Fatalf("MCP profile diagnostics missing: %v %s", err, identity)
	}
	suffix := " --profile-dir '" + f.dir + "' (approve in the browser)."
	f.api.mu.Lock()
	f.api.mutationError = actionable.DeniedCapability(v1.ToolCapability_TOOL_CAPABILITY_PREPARE)
	f.api.mu.Unlock()
	prepare("Missing capability PREPARE: the server denied this permission for the connection. Run: syntra login --reauthorize --prepare --publish" + suffix)

	f.api.mu.Lock()
	f.api.p.Config.Capabilities = []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}
	f.api.mu.Unlock()
	store, err := toolclient.OpenStore(context.Background(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	local.Config.Capabilities = []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}
	if err = store.Save(local); err != nil {
		t.Fatal(err)
	}
	store.Close()
	prepare("Missing capability PREPARE: this connection allows READ only. Run: syntra login --reauthorize --prepare" + suffix)
	store, err = toolclient.OpenStore(context.Background(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Remove(); err != nil {
		t.Fatal(err)
	}
	store.Close()
	assertParity("workspaces", map[string]any{}, []string{"workspaces"}, nil,
		"No Syntra connection. Run: syntra login --profile-dir '"+f.dir+"' (choose permissions in the browser; use --prepare for drafts, --prepare --publish to send, or --mark-read for read receipts).")
}

func TestMCPActionableErrorCLIParity(t *testing.T) {
	f := fixture(t)
	p := spawn(t, f)
	p.init(t)

	checkCLI := func(r *v1.PrepareContentRequest, want string) {
		t.Helper()
		body, err := protojson.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(f.binary, "prepare", "--profile-dir", f.dir, "--file", "-")
		cmd.Env = testsupport.FixtureCLIEnv()
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.CombinedOutput()
		if err == nil || string(out) != want+"\n" {
			t.Fatalf("CLI output differs: %v %q, wanted %q", err, out, want)
		}
	}
	t.Run("local_preflight", func(t *testing.T) {
		f.api.mu.Lock()
		f.api.ticketClosed = true
		f.api.mu.Unlock()
		r := newRequest()
		r.Content.ExistingTicketId = str(ticketID)
		r.ExpectedTicketRevision = str(revision(1))
		got := string(toolResult(t, p.call(t, "prepare", rpcArgs(t, r, "")), true))
		want := actionable.New(actionable.TicketClosed).Error()
		if got != want {
			t.Fatalf("MCP output differs: %q, wanted %q", got, want)
		}
		checkCLI(r, want)
		f.api.mu.Lock()
		defer f.api.mu.Unlock()
		if f.api.mutations != 0 {
			t.Fatalf("preflight sent %d mutations", f.api.mutations)
		}
	})
	t.Run("server_rejection", func(t *testing.T) {
		remote := actionable.New(actionable.StaleTicketRevision)
		f.api.mu.Lock()
		f.api.ticketClosed = false
		f.api.mutationError = remote.Connect()
		f.api.mu.Unlock()
		r := newRequest()
		r.Content.ExistingTicketId = str(ticketID)
		r.Content.Title = "Visible title"
		r.ExpectedTicketRevision = str(revision(1))
		got := string(toolResult(t, p.call(t, "prepare", rpcArgs(t, r, "")), true))
		want := remote.Error() + " (retained request " + r.RequestId + ")"
		if got != want {
			t.Fatalf("MCP output differs: %q, wanted %q", got, want)
		}
		checkCLI(r, want)
		retry := string(toolResult(t, p.call(t, "retry", map[string]any{"requestId": r.RequestId}), true))
		if retry != remote.Error() {
			t.Fatalf("MCP retry error differs: %q", retry)
		}
		cmd := exec.Command(f.binary, "retry", "--profile-dir", f.dir, "--request", r.RequestId)
		cmd.Env = testsupport.FixtureCLIEnv()
		out, err := cmd.CombinedOutput()
		if err == nil || string(out) != retry+"\n" {
			t.Fatalf("CLI retry output differs: %v %q", err, out)
		}
	})
}

func TestMCPActualProcessCLIParity(t *testing.T) {
	expectedTools := []string{"identity", "workspaces", "projects", "teams", "members", "resolve", "tickets", "ticket", "messages", "snapshot", "prepare", "link", "publish", "retry"}
	f := fixture(t)
	p := spawn(t, f)
	p.init(t)
	p.next++
	p.send(t, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{}}`, p.next)))
	listed := p.receive(t)
	var catalog struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if json.Unmarshal(listed["result"], &catalog) != nil || len(catalog.Tools) != len(expectedTools) {
		t.Fatal("tool allowlist incorrect", string(listed["result"]))
	}
	for _, tool := range catalog.Tools {
		if !slicesContains(expectedTools, tool.Name) {
			t.Fatal("unexpected tool", tool.Name)
		}
	}
	identity := toolResult(t, p.call(t, "identity", map[string]any{}), false)
	if !bytes.Contains(identity, []byte(connectionID)) || !bytes.Contains(identity, []byte(ownerID)) {
		t.Fatal("identity lost sharedaccount")
	}
	q := newRequest()
	if !bytes.Contains([]byte(q.Content.Summary), []byte{13, 10}) || !bytes.Contains([]byte(q.Content.Summary), []byte{0x65, 0xcc, 0x81}) {
		t.Fatal("fixture lacks actual CRLF/NFD")
	}
	raw := toolResult(t, p.call(t, "prepare", rpcArgs(t, q, "")), false)
	var prepared v1.AuthoredContentSnapshot
	if protojson.Unmarshal(raw, &prepared) != nil || !proto.Equal(prepared.Content, q.Content) || prepared.GetPublishedMessageId() != "" {
		t.Fatal("MCP private exact data changed")
	}

	bad := newRequest()
	bad.ExpectedContentDigest = make([]byte, 32)
	f.api.mu.Lock()
	beforeDigest := f.api.mutations
	f.api.mu.Unlock()
	toolResult(t, p.call(t, "prepare", rpcArgs(t, bad, "")), true)
	f.api.mu.Lock()
	if f.api.mutations != beforeDigest {
		t.Error("MCP mismatched expected digest reached mutation")
	}
	f.api.mu.Unlock()
	// A distinct CLI process shares the exact profile, connection and retained request.
	cmd := exec.Command(f.binary, "retry", "--profile-dir", f.dir, "--request", q.RequestId)
	cmd.Env = testsupport.FixtureCLIEnv()
	cliRaw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatal("CLI could not interleave with idle MCP", e, string(cliRaw))
	}
	var cliSnapshot v1.AuthoredContentSnapshot
	if protojson.Unmarshal(cliRaw, &cliSnapshot) != nil || !proto.Equal(&prepared, &cliSnapshot) || cliSnapshot.Receipt.ClientReported.GetModel() != "first-model" {
		t.Fatal("CLI/MCP retry parity changed attribution")
	}
	linked := proto.Clone(prepared.Content).(*v1.AuthoredContent)
	linked.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: recipientID}}
	link := &v1.LinkContentRequest{RequestId: id(), Current: expect(&prepared), LinkedContent: linked, ClientReported: &v1.ClientReportedProvenance{Model: str("second-model")}}
	raw = toolResult(t, p.call(t, "link", rpcArgs(t, link, q.RequestId)), false)
	var linkedSnapshot v1.AuthoredContentSnapshot
	if protojson.Unmarshal(raw, &linkedSnapshot) != nil || linkedSnapshot.GetPublishedMessageId() != "" || linkedSnapshot.Content.OriginalDestinationIntent != q.Content.OriginalDestinationIntent {
		t.Fatal("link sent/changed intent")
	}
	pub := &v1.PublishContentRequest{WorkspaceId: workspaceID, ExpectedAuthorUserId: ownerID, Current: expect(&linkedSnapshot), RequestId: id(), ClientReported: &v1.ClientReportedProvenance{Model: str("third-model")}}
	raw = toolResult(t, p.call(t, "publish", rpcArgs(t, pub, link.RequestId)), false)
	var published v1.AuthoredContentSnapshot
	if protojson.Unmarshal(raw, &published) != nil || published.GetPublishedMessageId() != messageID {
		t.Fatal("explicit MCP publish failed")
	}
	snapshot := &v1.GetContentSnapshotRequest{WorkspaceId: workspaceID, TicketId: ticketID, Record: &v1.GetContentSnapshotRequest_PublishedMessageId{PublishedMessageId: messageID}}
	raw = toolResult(t, p.call(t, "snapshot", rpcArgs(t, snapshot, pub.RequestId)), false)
	if !bytes.Contains(raw, []byte("retained-client-expectation")) {
		t.Fatal("retained snapshot verification absent")
	}
	raw = toolResult(t, p.call(t, "retry", map[string]any{"requestId": q.RequestId}), false)
	var original v1.AuthoredContentSnapshot
	protojson.Unmarshal(raw, &original)
	if original.Receipt.ClientReported.GetModel() != "first-model" {
		t.Fatal("later MCP model relabelled original")
	}
	f.api.mu.Lock()
	f.api.duplicate = true
	f.api.mu.Unlock()
	raw = toolResult(t, p.call(t, "resolve", map[string]any{"workspace": "Labs", "project": "App"}), true)
	if !bytes.Contains(raw, []byte(`"complete":true`)) || !bytes.Contains(raw, []byte(recipientID)) {
		t.Fatal("ambiguous later-page names not clarified")
	}
	// Current profile/capability is reloaded for every call, without restarting MCP.
	store, e := toolclient.OpenStore(context.Background(), f.dir)
	if e != nil {
		t.Fatal(e)
	}
	profile, e := store.Load()
	if e != nil {
		t.Fatal(e)
	}
	profile.Config.Capabilities = []v1.ToolCapability{1, 2}
	if e = store.Save(profile); e != nil {
		t.Fatal(e)
	}
	store.Close()
	f.api.mu.Lock()
	f.api.p.Config.Capabilities = []v1.ToolCapability{1, 2}
	before := f.api.mutations
	f.api.mu.Unlock()
	raw = toolResult(t, p.call(t, "retry", map[string]any{"requestId": pub.RequestId}), true)
	if !bytes.Contains(raw, []byte("Missing capability PUBLISH")) || !bytes.Contains(raw, []byte("syntra login --reauthorize --prepare --publish")) {
		t.Fatalf("missing recovery guidance for read-only connection: %s", raw)
	}
	f.api.mu.Lock()
	if f.api.mutations != before {
		t.Error("MCP capability denial reached publication")
	}
	f.api.mu.Unlock()
}

func TestBrowserCLIRecoveryThenMCPProcess(t *testing.T) {
	f, device := browserProcessFixture(t)

	firstCommand, firstResult := runBrowserCLI(f)
	select {
	case <-device.beginEvents:
	case <-time.After(10 * time.Second):
		result := awaitCommand(t, firstCommand, firstResult, 5*time.Second)
		t.Fatalf("browser CLI did not reach synthetic Begin: %v", result.err)
	}
	first := awaitCommand(t, firstCommand, firstResult, 10*time.Second)
	if first.err == nil || !strings.Contains(strings.ToLower(string(first.output)), "unavailable") {
		t.Fatalf("synthetic lost Begin response did not produce a safe recoverable error: %v", first.err)
	}
	store, err := toolclient.OpenStore(context.Background(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.LoadPendingBrowserAttempt()
	if err != nil {
		store.Close()
		t.Fatal("lost Begin discarded its durable pairing", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first.output, []byte(pending.ToolSecret)) || bytes.Contains(first.output, []byte(pending.PollProof)) {
		t.Fatal("lost Begin error exposed a retained browser credential")
	}

	get := connect.NewRequest(&v1.GetLoginRequestRequest{UserCode: fixtures.UserCode})
	get.Header().Set("Authorization", fixtures.HumanAuthorization)
	preview, err := device.GetLoginRequest(context.Background(), get)
	if err != nil {
		t.Fatal("synthetic user could not inspect the pending request", err)
	}
	approve := connect.NewRequest(&v1.ApproveLoginRequest{
		UserCode: fixtures.UserCode, ExpectedFingerprint: append([]byte(nil), preview.Msg.Request.Fingerprint...),
		Capabilities:     []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ, v1.ToolCapability_TOOL_CAPABILITY_PREPARE},
		ExpiresInSeconds: 300,
	})
	approve.Header().Set("Authorization", fixtures.HumanAuthorization)
	if _, err = device.ApproveLogin(context.Background(), approve); err != nil {
		t.Fatal("synthetic reduced-scope consent failed", err)
	}
	device.approvals.Add(1)

	secondCommand, secondResult := runBrowserCLI(f)
	select {
	case <-device.beginEvents:
	case <-time.After(10 * time.Second):
		result := awaitCommand(t, secondCommand, secondResult, 5*time.Second)
		t.Fatalf("CLI restart did not replay the retained Begin: %v", result.err)
	}
	second := awaitCommand(t, secondCommand, secondResult, 20*time.Second)
	if second.err != nil || !bytes.Contains(second.output, []byte("Connected account")) {
		t.Fatalf("approved exact retry did not activate: %v %s", second.err, safeBrowserOutput(second.output, pending))
	}
	if bytes.Contains(second.output, []byte(pending.ToolSecret)) || bytes.Contains(second.output, []byte(pending.PollProof)) {
		t.Fatal("browser login output exposed a retained credential")
	}
	requests := device.requests()
	if len(requests) != 2 || !proto.Equal(requests[0], requests[1]) || requests[0].RequestId != pending.RequestID || device.approvals.Load() != 1 {
		t.Fatal("browser restart changed the intent or issued a duplicate consent connection")
	}

	store, err = toolclient.OpenStore(context.Background(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.Load()
	if err != nil {
		store.Close()
		t.Fatal("compiled CLI did not persist its v2 browser profile", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if profile.Version != 2 || profile.Method != "browser" || profile.State != "active" || profile.AccountID != "33333333-3333-4333-8333-333333333333" || profile.ConnectionID != "22222222-2222-4222-8222-222222222222" || profile.Config.LifetimeSeconds != 300 || !equalCapabilities(profile.Config.Capabilities, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ, v1.ToolCapability_TOOL_CAPABILITY_PREPARE}) {
		t.Fatal("activated CLI profile does not contain the actual reduced grant")
	}

	process := spawn(t, f)
	process.init(t)
	process.next++
	process.send(t, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{}}`, process.next)))
	listed := process.receive(t)
	expectedTools := []string{"identity", "workspaces", "projects", "teams", "members", "resolve", "tickets", "ticket", "messages", "snapshot", "prepare", "link", "publish", "retry"}
	var catalog struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if json.Unmarshal(listed["result"], &catalog) != nil || len(catalog.Tools) != 14 {
		t.Fatal("browser profile MCP tool allowlist changed", string(listed["result"]))
	}
	for _, tool := range catalog.Tools {
		if !slicesContains(expectedTools, tool.Name) {
			t.Fatal("browser profile exposed an unexpected MCP tool", tool.Name)
		}
	}
	identity := toolResult(t, process.call(t, "identity", map[string]any{}), false)
	if !bytes.Contains(identity, []byte(profile.AccountID)) || !bytes.Contains(identity, []byte(profile.ConnectionID)) {
		t.Fatal("MCP identity does not match the browser account and connection")
	}
	workspaces := toolResult(t, process.call(t, "workspaces", map[string]any{}), false)
	if bytes.Count(workspaces, []byte(`"workspaceId"`)) < 2 || !bytes.Contains(workspaces, []byte(workspaceID)) || !bytes.Contains(workspaces, []byte(secondWorkspaceID)) || !bytes.Contains(workspaces, []byte(profile.AccountID)) {
		t.Fatal("MCP did not expose both workspaces authorized for the browser account", string(workspaces))
	}

	request := newRequest()
	request.Content.ExpectedAuthorUserId = profile.AccountID
	preparedRaw := toolResult(t, process.call(t, "prepare", rpcArgs(t, request, "")), false)
	var prepared v1.AuthoredContentSnapshot
	if protojson.Unmarshal(preparedRaw, &prepared) != nil || !proto.Equal(prepared.Content, request.Content) || prepared.GetPublishedMessageId() != "" {
		t.Fatal("browser grant did not create an exact private prepare")
	}

	cli := exec.Command(f.binary, "retry", "--profile-dir", f.dir, "--request", request.RequestId)
	cli.Env = testsupport.FixtureCLIEnv()
	cliOutput, err := cli.CombinedOutput()
	var retried v1.AuthoredContentSnapshot
	if err != nil || protojson.Unmarshal(cliOutput, &retried) != nil || !proto.Equal(&prepared, &retried) {
		t.Fatalf("CLI retry did not return the exact MCP private prepare: %v", err)
	}

	linkedContent := proto.Clone(prepared.Content).(*v1.AuthoredContent)
	linkedContent.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: recipientID}}
	link := &v1.LinkContentRequest{RequestId: id(), Current: expect(&prepared), LinkedContent: linkedContent, ClientReported: &v1.ClientReportedProvenance{Model: str("browser-link")}}
	linkedRaw := toolResult(t, process.call(t, "link", rpcArgs(t, link, request.RequestId)), false)
	var linked v1.AuthoredContentSnapshot
	if protojson.Unmarshal(linkedRaw, &linked) != nil || linked.GetPublishedMessageId() != "" || linked.Content.Destination.GetLinkedMemberId() != recipientID {
		t.Fatal("browser grant did not retain the private link")
	}

	f.api.mu.Lock()
	mutationsBefore, publishesBefore := f.api.mutations, f.api.publishes
	f.api.mu.Unlock()
	publish := &v1.PublishContentRequest{
		WorkspaceId: workspaceID, ExpectedAuthorUserId: profile.AccountID, Current: expect(&linked), RequestId: id(),
		ClientReported: &v1.ClientReportedProvenance{Model: str("browser-publish-denied")},
	}
	toolResult(t, process.call(t, "publish", rpcArgs(t, publish, link.RequestId)), true)
	f.api.mu.Lock()
	if f.api.mutations != mutationsBefore || f.api.publishes != publishesBefore {
		t.Error("reduced browser scope reached the PublishContent RPC")
	}
	f.api.mu.Unlock()
}

func slicesContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestMCPRawInvalidInputAndOutputBounds(t *testing.T) {
	f := fixture(t)
	valid, _ := protojson.Marshal(newRequest())
	nested := string(valid)
	cases := []struct{ name, args string }{
		{"unknown-outer", `{"request":` + nested + `,"file":"forbidden-path-marker"}`},
		{"duplicate-outer", `{"request":` + nested + `,"request":` + nested + `}`},
		{"null-outer", `{"request":null}`},
		{"wrong-type", `{"request":"not-an-object"}`},
		{"unknown-inner", `{"request":` + strings.Replace(nested, `"summary":`, `"unknownField":`, 1) + `}`},
		{"alias-inner", `{"request":` + strings.Replace(nested, `"formatVersion":`, `"format_version":`, 1) + `}`},
		{"contradictory-destination", `{"request":` + strings.Replace(nested, `"unlinkedLabel":"Pedro"`, `"unlinkedLabel":"Pedro","linkedMemberId":""`, 1) + `}`},
		{"lone-surrogate", `{"request":` + strings.Replace(nested, `"title":`, `"bad":"\ud800","title":`, 1) + `}`},
		{"invalid-utf8", `{"request":` + strings.Replace(nested, "Pedro", string([]byte{255}), 1) + `}`},
		{"oversized-arguments", `{"request":{"summary":"` + strings.Repeat("x", MaxArgumentsBytes) + `"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := spawn(t, f)
			p.init(t)
			f.api.mu.Lock()
			beforeMut, beforeID := f.api.mutations, f.api.identities
			f.api.mu.Unlock()
			raw := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"prepare","arguments":` + tc.args + `}}`)
			p.send(t, raw)
			select {
			case line, ok := <-p.lines:
				if ok {
					var response map[string]json.RawMessage
					if json.Unmarshal(line, &response) != nil {
						t.Fatal("malformed stdout")
					}
					if response["result"] != nil {
						toolResult(t, response, true)
					}
				}
			case <-p.done:
			case <-time.After(5 * time.Second):
				t.Fatal("malformed frame/args hung")
			}
			f.api.mu.Lock()
			if f.api.mutations != beforeMut || f.api.identities != beforeID {
				t.Error("malformed original arguments reached authorization or mutation")
			}
			f.api.mu.Unlock()
			if strings.Contains(p.stderr.String(), "forbidden-path-marker") {
				t.Fatal("malformed payload leaked to stderr")
			}
		})
	}
	t.Run("oversized-frame-terminates", func(t *testing.T) {
		p := spawn(t, f)
		p.init(t)
		done := make(chan struct{})
		go func() {
			p.in.Write([]byte(`{"jsonrpc":"2.0","method":"ignored","params":{"data":"` + strings.Repeat("x", MaxFrameBytes+1) + `"}}` + "\n"))
			close(done)
		}()
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Fatal("oversized frame did not terminate safely")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("oversized sender stuck")
		}
	})
	t.Run("oversized-result-is-error-not-partial-success", func(t *testing.T) {
		p := spawn(t, f)
		p.init(t)
		f.api.mu.Lock()
		f.api.big = 800000
		f.api.mu.Unlock()
		raw := toolResult(t, p.call(t, "projects", map[string]any{"workspaceId": workspaceID}), true)
		if !bytes.Contains(raw, []byte("output bound")) {
			t.Fatal("large result not bounded", string(raw))
		}
		f.api.mu.Lock()
		f.api.big = 0
		f.api.mu.Unlock()
	})
	t.Run("unknown-tools-never-dispatch", func(t *testing.T) {
		p := spawn(t, f)
		p.init(t)
		for _, name := range []string{"login", "exec", "shell", "http", "mark-read", "close", "HumanCreate"} {
			response := p.call(t, name, map[string]any{})
			if response["error"] == nil {
				t.Fatal("unsupported tool reached handler", name)
			}
		}
	})
}
func TestMCPCancellationConcurrencyEOFAndCallDeadline(t *testing.T) {
	f := fixture(t)
	t.Run("cancellation-releases-operation-lock", func(t *testing.T) {
		p := spawn(t, f)
		p.init(t)
		f.api.mu.Lock()
		f.api.hold = true
		f.api.mu.Unlock()
		p.send(t, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"projects","arguments":{"workspaceId":"`+workspaceID+`"}}}`))
		select {
		case <-f.api.held:
		case <-time.After(3 * time.Second):
			t.Fatal("held RPC did not start")
		}
		p.send(t, []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"fixture cancel"}}`))
		p.send(t, []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"identity","arguments":{}}}`))
		found := false
		for i := 0; i < 2; i++ {
			response := p.receive(t)
			if string(response["id"]) == "3" {
				toolResult(t, response, false)
				found = true
				break
			}
		}
		if !found {
			t.Fatal("cancelled call held profile lock")
		}
	})
	t.Run("parallel-tools-share-serialized-lock", func(t *testing.T) {
		p := spawn(t, f)
		p.init(t)
		for i := 2; i < 6; i++ {
			p.send(t, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"identity","arguments":{}}}`, i)))
		}
		ids := map[string]bool{}
		for i := 0; i < 4; i++ {
			r := p.receive(t)
			toolResult(t, r, false)
			ids[string(r["id"])] = true
		}
		if len(ids) != 4 {
			t.Fatal("parallel response IDs conflated")
		}
	})
	t.Run("per-call-lock-deadline-session-survives", func(t *testing.T) {
		// Keep enough time for a successful TLS request on a slower runner.
		// The held store still forces the first operation to reach its deadline.
		p := spawn(t, f, "--call-timeout", "750ms")
		p.init(t)
		store, e := toolclient.OpenStore(context.Background(), f.dir)
		if e != nil {
			t.Fatal(e)
		}
		defer store.Close()
		toolResult(t, p.call(t, "identity", map[string]any{}), true)
		store.Close()
		toolResult(t, p.call(t, "identity", map[string]any{}), false)
	})
	t.Run("idle-session-outlives-call-deadline", func(t *testing.T) {
		p := spawn(t, f, "--call-timeout", "750ms")
		p.init(t)
		time.Sleep(time.Second)
		toolResult(t, p.call(t, "identity", map[string]any{}), false)
	})
	t.Run("EOF-closes-session", func(t *testing.T) {
		p := spawn(t, f)
		p.init(t)
		p.in.Close()
		select {
		case <-p.done:
			if p.waitErr != nil {
				t.Fatal("clean EOF failed", p.waitErr, p.stderr.String())
			}
		case <-time.After(4 * time.Second):
			t.Fatal("EOF did not terminate")
		}
	})

	t.Run("EOF-during-active-call", func(t *testing.T) {
		p := spawn(t, f)
		p.init(t)
		held := make(chan struct{})
		f.api.mu.Lock()
		f.api.held = held
		f.api.hold = true
		f.api.mu.Unlock()
		p.send(t, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"projects","arguments":{"workspaceId":"`+workspaceID+`"}}}`))
		select {
		case <-held:
		case <-time.After(3 * time.Second):
			t.Fatal("active EOF fixture never started")
		}
		p.in.Close()
		select {
		case <-p.done:
		case <-time.After(4 * time.Second):
			t.Fatal("EOF did not cancel active call")
		}
	})
	t.Run("origin-override-never-forwards", func(t *testing.T) {
		p := spawn(t, f, "--api", "https://foreign.invalid")
		p.init(t)
		f.api.mu.Lock()
		before := f.api.identities
		f.api.mu.Unlock()
		toolResult(t, p.call(t, "identity", map[string]any{}), true)
		f.api.mu.Lock()
		if f.api.identities != before {
			t.Error("foreign startup origin forwarded credential")
		}
		f.api.mu.Unlock()
	})
}

// This process intentionally does NOT drain inherited stdout after initialize.
// A large valid response fills the real OS pipe, exercising poller cancellation
// and the production writer deadline rather than a mocked SetWriteDeadline.
func TestMCPInheritedSlowReaderDeadline(t *testing.T) {
	f := fixture(t)
	f.api.mu.Lock()
	f.api.big = 250000
	f.api.mu.Unlock()
	cmd := exec.Command(f.binary, "mcp", "--profile-dir", f.dir)
	cmd.Env = testsupport.FixtureCLIEnv()
	input, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr safeBuffer
	cmd.Stderr = &stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { input.Close(); output.Close(); cmd.Process.Kill() }()
	io.WriteString(input, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"slow-fixture","version":"1"}}}`+"\n")
	reader := bufio.NewReader(output)
	line, e := reader.ReadBytes('\n')
	if e != nil || !json.Valid(line) {
		t.Fatal("slow reader initialize failed", e)
	}
	io.WriteString(input, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	started := time.Now()
	io.WriteString(input, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"projects","arguments":{"workspaceId":"`+workspaceID+`"}}}`+"\n")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("blocked reader unexpectedly succeeded")
		}
		if time.Since(started) < time.Second {
			t.Fatal("test did not exercise blocked pipe deadline")
		}
	case <-time.After(6 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("unread inherited output pipe hung past deadline")
	}
	if strings.Contains(stderr.String(), f.api.secret()) {
		t.Fatal("slow reader stderr leaked credential")
	}
}
