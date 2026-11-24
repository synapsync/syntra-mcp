package toolclient

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
	ci "syntra.local/mcp/internal/contentintegrity"
	"syntra.local/mcp/internal/testsupport"
)

const workspaceID = "44444444-4444-4444-8444-444444444444"
const projectID = "55555555-5555-4555-8555-555555555555"
const backendTeamID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const qaTeamID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
const formerPedroID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
const otherID = "66666666-6666-4666-8666-666666666666"
const threadID = "77777777-7777-4777-8777-777777777777"
const preparedID = "88888888-8888-4888-8888-888888888888"
const messageID = "99999999-9999-4999-8999-999999999999"

func rev(n byte) string    { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32)) }
func ptr(s string) *string { return &s }

type contentFixture struct {
	syntrav1connect.UnimplementedToolConnectionServiceHandler
	syntrav1connect.UnimplementedAuthoredContentServiceHandler
	syntrav1connect.UnimplementedProjectServiceHandler
	syntrav1connect.UnimplementedProjectTeamServiceHandler
	syntrav1connect.UnimplementedWorkspaceServiceHandler
	syntrav1connect.UnimplementedMessageServiceHandler
	syntrav1connect.UnimplementedThreadServiceHandler
	mu                  sync.Mutex
	p                   *Profile
	attempts            map[string]proto.Message
	snapshots           map[string]*v1.AuthoredContentSnapshot
	calls               int
	pages               int
	duplicate           bool
	noTeams             bool
	expire              bool
	incomplete          bool
	deny                bool
	closed              bool
	threadReads         int
	threadState         v1.ThreadState
	threadPrivate       bool
	participantRequests []*v1.AddThreadParticipantRequest
	tamper              string
	hold                bool
	committed           chan struct{}
	snapshot            *v1.AuthoredContentSnapshot
	record              *v1.GetContentSnapshotResponse
}

func (f *contentFixture) GetIdentity(context.Context, *connect.Request[v1.GetIdentityRequest]) (*connect.Response[v1.GetIdentityResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("DO-NOT-LEAK-REMOTE"))
	}
	a, _ := time.Parse(time.RFC3339Nano, f.p.CreatedAt)
	b, _ := time.Parse(time.RFC3339Nano, f.p.ExpiresAt)
	return connect.NewResponse(&v1.GetIdentityResponse{Connection: &v1.ToolConnection{Id: f.p.ConnectionID, OwnerUserId: f.p.AccountID, Label: f.p.Config.Label, Capabilities: f.p.Config.Capabilities, CreatedAt: timestamppb.New(a), ExpiresAt: timestamppb.New(b), State: 1}}), nil
}
func (f *contentFixture) ListAccessibleWorkspaces(_ context.Context, r *connect.Request[v1.ListAccessibleWorkspacesRequest]) (*connect.Response[v1.ListAccessibleWorkspacesResponse], error) {
	return connect.NewResponse(&v1.ListAccessibleWorkspacesResponse{Workspaces: []*v1.AccessibleWorkspace{{WorkspaceId: workspaceID, MemberUserId: accountA, Name: "Labs"}}}), nil
}
func (f *contentFixture) ListProjects(_ context.Context, r *connect.Request[v1.ListProjectsRequest]) (*connect.Response[v1.ListProjectsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages++
	if r.Msg.PageToken != "" {
		if f.incomplete {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("hidden"))
		}
		if f.expire {
			f.expire = false
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("expired"))
		}
		name := "Other"
		if f.duplicate {
			name = "App"
		}
		return connect.NewResponse(&v1.ListProjectsResponse{Projects: []*v1.Project{{Id: otherID, WorkspaceId: workspaceID, Name: name}}}), nil
	}
	return connect.NewResponse(&v1.ListProjectsResponse{Projects: []*v1.Project{{Id: projectID, WorkspaceId: workspaceID, Name: "App"}}, NextPageToken: "second"}), nil
}
func (f *contentFixture) ListProjectTeams(_ context.Context, r *connect.Request[v1.ListProjectTeamsRequest]) (*connect.Response[v1.ListProjectTeamsResponse], error) {
	f.mu.Lock()
	none := f.noTeams
	f.mu.Unlock()
	if none || r.Msg.ProjectId != projectID {
		return connect.NewResponse(&v1.ListProjectTeamsResponse{}), nil
	}
	if r.Msg.PageToken == "" {
		return connect.NewResponse(&v1.ListProjectTeamsResponse{ProjectTeams: []*v1.ProjectTeam{{WorkspaceId: workspaceID, ProjectId: projectID, TeamId: backendTeamID, TeamName: "Backend", ExcludedUserIds: []string{accountB}, Members: []*v1.ProjectTeamMember{{UserId: accountA, DisplayName: "Roimer", Active: true}}}}, NextPageToken: "teams-2"}), nil
	}
	return connect.NewResponse(&v1.ListProjectTeamsResponse{ProjectTeams: []*v1.ProjectTeam{{WorkspaceId: workspaceID, ProjectId: projectID, TeamId: qaTeamID, TeamName: "QA", Members: []*v1.ProjectTeamMember{{UserId: accountB, DisplayName: "Pedro", Active: true}, {UserId: formerPedroID, DisplayName: "Pedro", Active: false}}}}}), nil
}
func (f *contentFixture) ListMembers(_ context.Context, r *connect.Request[v1.ListMembersRequest]) (*connect.Response[v1.ListMembersResponse], error) {
	if r.Msg.PageToken == "" {
		return connect.NewResponse(&v1.ListMembersResponse{Members: []*v1.WorkspaceMember{{UserId: accountA, DisplayName: "Roimer"}}, NextPageToken: "later"}), nil
	}
	return connect.NewResponse(&v1.ListMembersResponse{Members: []*v1.WorkspaceMember{{UserId: accountB, DisplayName: "Pedro"}}}), nil
}
func (f *contentFixture) apply(ctx context.Context, id string, req proto.Message, c *v1.AuthoredContent, p *v1.ClientReportedProvenance, publish bool) (*v1.AuthoredContentSnapshot, error) {
	f.mu.Lock()
	f.calls++
	if f.closed {
		f.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("DO-NOT-LEAK-CLOSED"))
	}
	if f.deny {
		f.mu.Unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("DO-NOT-LEAK-REMOTE"))
	}
	old := f.attempts[id]
	if old != nil && !proto.Equal(old, req) {
		f.mu.Unlock()
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("changed"))
	}
	s := f.snapshots[id]
	if s == nil {
		h, _ := digest(c)
		receipt := &v1.ContentProvenanceReceipt{ContentDigest: h, OwnerUserId: accountA, ConnectionId: ptr(f.p.ConnectionID), RequestId: id, ThreadId: threadID, RecordedAt: timestamppb.Now(), ClientReported: p, IntegrityOrigin: 1}
		s = &v1.AuthoredContentSnapshot{Content: proto.Clone(c).(*v1.AuthoredContent), Receipt: receipt, ThreadRevision: rev(byte(len(f.snapshots) + 1)), Record: &v1.AuthoredContentSnapshot_PreparedMessage{PreparedMessage: &v1.PrivateContentRevision{PreparedMessageId: preparedID, Revision: rev(byte(len(f.snapshots) + 1))}}}
		if publish {
			receipt.MessageId = messageID
			s.Record = &v1.AuthoredContentSnapshot_PublishedMessageId{PublishedMessageId: messageID}
		}
		switch q := req.(type) {
		case *v1.PrepareContentRequest:
			if q.Update != nil {
				s.ThreadRevision = q.Update.ExpectedThreadRevision
			} else if q.ExpectedThreadRevision != nil {
				s.ThreadRevision = *q.ExpectedThreadRevision
			}
		case *v1.LinkContentRequest:
			if q.LinkedContent.ExistingThreadId != nil {
				s.ThreadRevision = q.Current.ExpectedThreadRevision
			}
		}
		f.attempts[id] = proto.Clone(req)
		f.snapshots[id] = s
	}
	out := proto.Clone(s).(*v1.AuthoredContentSnapshot)
	hold := f.hold
	f.hold = false
	if hold {
		close(f.committed)
	}
	tamper := f.tamper
	switch tamper {
	case "content":
		out.Content.ContextMarkdown += "altered"
		h, _ := digest(out.Content)
		out.Receipt.ContentDigest = h
	case "owner":
		out.Receipt.OwnerUserId = accountB
	case "connection":
		out.Receipt.ConnectionId = ptr(accountB)
	case "request":
		out.Receipt.RequestId = accountB
	case "reported":
		out.Receipt.ClientReported = &v1.ClientReportedProvenance{Model: ptr("substitute")}
	case "thread":
		out.Receipt.ThreadId = accountB
	case "record":
		out.Receipt.MessageId = messageID
	case "origin":
		out.Receipt.IntegrityOrigin = 2
	case "presence":
		out.Content.ProjectId = ptr(projectID)
		h, _ := digest(out.Content)
		out.Receipt.ContentDigest = h
	case "same-thread-cas":
		switch q := req.(type) {
		case *v1.LinkContentRequest:
			out.ThreadRevision = q.Current.ExpectedThreadRevision
		case *v1.PublishContentRequest:
			out.ThreadRevision = q.Current.ExpectedThreadRevision
		}
	case "thread-cas":
		out.ThreadRevision = rev(99)
	case "timestamp":
		out.Receipt.RecordedAt = nil
	}
	f.mu.Unlock()
	if hold {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return out, nil
}
func (f *contentFixture) PrepareContent(ctx context.Context, r *connect.Request[v1.PrepareContentRequest]) (*connect.Response[v1.PrepareContentResponse], error) {
	s, e := f.apply(ctx, r.Msg.RequestId, r.Msg, r.Msg.Content, r.Msg.ClientReported, false)
	return connect.NewResponse(&v1.PrepareContentResponse{Snapshot: s}), e
}
func (f *contentFixture) LinkContent(ctx context.Context, r *connect.Request[v1.LinkContentRequest]) (*connect.Response[v1.LinkContentResponse], error) {
	s, e := f.apply(ctx, r.Msg.RequestId, r.Msg, r.Msg.LinkedContent, r.Msg.ClientReported, false)
	return connect.NewResponse(&v1.LinkContentResponse{Snapshot: s}), e
}
func (f *contentFixture) PublishContent(ctx context.Context, r *connect.Request[v1.PublishContentRequest]) (*connect.Response[v1.PublishContentResponse], error) {
	f.mu.Lock()
	var content *v1.AuthoredContent
	for _, s := range f.snapshots {
		if bytes.Equal(s.Receipt.ContentDigest, r.Msg.Current.ExpectedContentDigest) {
			content = s.Content
			break
		}
	}
	f.mu.Unlock()
	if content == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no base"))
	}
	s, e := f.apply(ctx, r.Msg.RequestId, r.Msg, content, r.Msg.ClientReported, true)
	return connect.NewResponse(&v1.PublishContentResponse{Snapshot: s}), e
}
func (f *contentFixture) GetContentSnapshot(context.Context, *connect.Request[v1.GetContentSnapshotRequest]) (*connect.Response[v1.GetContentSnapshotResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.record != nil {
		return connect.NewResponse(f.record), nil
	}
	return connect.NewResponse(snapshotRecord(f.snapshot)), nil
}
func setupContent(t *testing.T) (*App, *contentFixture, string, *x509.Certificate) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "profile")
	s, e := OpenStore(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	f := &contentFixture{threadState: v1.ThreadState_THREAD_STATE_OPEN, attempts: map[string]proto.Message{}, snapshots: map[string]*v1.AuthoredContentSnapshot{}, committed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(syntrav1connect.NewToolConnectionServiceHandler(f))
	mux.Handle(syntrav1connect.NewAuthoredContentServiceHandler(f))
	mux.Handle(syntrav1connect.NewProjectServiceHandler(f))
	mux.Handle(syntrav1connect.NewProjectTeamServiceHandler(f))
	mux.Handle(syntrav1connect.NewWorkspaceServiceHandler(f))
	mux.Handle(syntrav1connect.NewMessageServiceHandler(f))
	mux.Handle(syntrav1connect.NewThreadServiceHandler(f))
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c, e := NewClient(srv.URL, pool)
	if e != nil {
		t.Fatal(e)
	}
	cfg := config()
	cfg.APIOrigin = srv.URL
	cfg.Capabilities = []v1.ToolCapability{1, 2, 3, 4}
	p, e := NewPending(cfg, accountA)
	if e != nil {
		t.Fatal(e)
	}
	p.State = "active"
	p.ConnectionID = projectID
	now := time.Now().UTC()
	p.CreatedAt = now.Format(time.RFC3339Nano)
	p.ExpiresAt = now.Add(time.Duration(cfg.LifetimeSeconds) * time.Second).Format(time.RFC3339Nano)
	if e = s.Save(p); e != nil {
		t.Fatal(e)
	}
	f.p = p
	app, e := NewApp(context.Background(), c, s)
	if e != nil {
		t.Fatal(e)
	}
	return app, f, dir, srv.Certificate()
}
func request() *v1.PrepareContentRequest {
	return &v1.PrepareContentRequest{RequestId: randomUUID(), Content: &v1.AuthoredContent{FormatVersion: 1, ExpectedAuthorUserId: accountA, WorkspaceId: workspaceID, Title: "Issue e\u0301", OriginalDestinationIntent: "  Pedro e\u0301  ", Destination: &v1.ContentDestination{Target: &v1.ContentDestination_UnlinkedLabel{UnlinkedLabel: "Pedro"}}, Summary: "  exact\r\n e\u0301  ", ContextMarkdown: "```go\r\nvar token = `input`\r\n```\r\n/api/v1?q=$HOME"}, ClientReported: &v1.ClientReportedProvenance{Model: ptr("first-model")}}
}
func TestProjectTeamDiscoveryReturnsEffectiveMembersOnly(t *testing.T) {
	a, _, _, _ := setupContent(t)
	ctx := context.Background()
	teams, err := a.ProjectTeams(ctx, workspaceID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 2 || len(teams[0].Members) != 1 || teams[0].Members[0].UserId != accountA {
		t.Fatal("effective roster was not preserved")
	}
	raw, err := protojson.Marshal(&v1.ListProjectTeamsResponse{ProjectTeams: teams})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("excludedUserIds")) {
		t.Fatal("administrative exclusions leaked into discovery output")
	}
}

func TestCompleteResolutionAndClarification(t *testing.T) {
	a, f, _, _ := setupContent(t)
	ctx := context.Background()
	r, e := a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Team: "QA", Recipient: "Pedro"})
	if e != nil || *r.RecipientID != accountB || *r.TeamID != qaTeamID {
		t.Fatalf("project team roster member: %v %v", r, e)
	}
	// Only project: union of the active project rosters (the inactive duplicate never competes).
	r, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Recipient: "Pedro"})
	if e != nil || *r.RecipientID != accountB || r.TeamID != nil {
		t.Fatalf("union of rosters: %v %v", r, e)
	}
	// Team UUID selects the same team; a person outside that team's roster is not a candidate.
	r, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Team: qaTeamID, Recipient: accountB})
	if e != nil || *r.RecipientID != accountB {
		t.Fatalf("team by id: %v %v", r, e)
	}
	var c *Clarification
	_, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Team: "Backend", Recipient: "Pedro"})
	if !errors.As(e, &c) || !c.Complete || len(c.Candidates) != 0 {
		t.Fatal("recipient outside the team roster resolved", e)
	}
	// A team that is not assigned to the project is a named not-found error.
	var nf *NotFound
	_, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Team: "Design"})
	if !errors.As(e, &nf) || nf.Query != "Design" || !strings.Contains(e.Error(), "Design") {
		t.Fatal("unassigned team accepted", e)
	}
	if _, e = a.Resolve(ctx, Selection{Workspace: "Labs", Team: "QA"}); e == nil {
		t.Fatal("team without project accepted")
	}
	// A project with no assigned team falls back to the workspace members.
	f.mu.Lock()
	f.noTeams = true
	f.mu.Unlock()
	r, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Recipient: "Pedro"})
	if e != nil || *r.RecipientID != accountB {
		t.Fatalf("workspace fallback: %v %v", r, e)
	}
	f.mu.Lock()
	f.noTeams = false
	f.mu.Unlock()
	f.mu.Lock()
	f.duplicate = true
	f.mu.Unlock()
	_, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App"})
	if !errors.As(e, &c) || !c.Complete || len(c.Candidates) != 2 {
		t.Fatal("later-page duplicate did not clarify", e)
	}
	f.mu.Lock()
	f.incomplete = true
	f.mu.Unlock()
	_, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App"})
	if !errors.As(e, &c) || c.Complete {
		t.Fatal("partial list claimed unique")
	}
	f.mu.Lock()
	f.incomplete = false
	f.duplicate = false
	f.expire = true
	before := f.pages
	f.mu.Unlock()
	if _, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App"}); e != nil {
		t.Fatal(e)
	}
	if f.pages-before != 4 {
		t.Fatal("cursor did not restart from first page")
	}
	_, e = a.Resolve(ctx, Selection{Workspace: "Labs", Recipient: "Unknown"})
	if !errors.As(e, &c) || len(c.Candidates) != 0 {
		t.Fatal("unknown silently linked")
	}
	if f.calls != 0 {
		t.Fatal("resolution mutated")
	}
}
func TestExactContentLinkPublishRetryAndModelSwitch(t *testing.T) {
	a, f, _, _ := setupContent(t)
	ctx := context.Background()
	r := request()
	s, e := a.Prepare(ctx, r, "")
	if e != nil {
		t.Fatal(e)
	}
	same, e := a.Prepare(ctx, r, "")
	if e != nil || !proto.Equal(s, same) {
		t.Fatal("identical typed request did not resume", e)
	}
	if !proto.Equal(s.Content, r.Content) || s.Content.ProjectId != nil || s.GetPublishedMessageId() != "" {
		t.Fatal("private exact bytes/presence changed")
	}
	x, _ := expectation(s)
	linked := proto.Clone(s.Content).(*v1.AuthoredContent)
	linked.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: accountB}}
	link := &v1.LinkContentRequest{RequestId: randomUUID(), Current: x, LinkedContent: linked, ClientReported: &v1.ClientReportedProvenance{Model: ptr("second-model")}}
	s2, e := a.Link(ctx, link, r.RequestId)
	if e != nil {
		t.Fatal(e)
	}
	if s2.Content.OriginalDestinationIntent != r.Content.OriginalDestinationIntent || s2.GetPublishedMessageId() != "" || f.calls != 3 {
		t.Fatal("link sent or changed intent")
	}
	x2, _ := expectation(s2)
	pub := &v1.PublishContentRequest{WorkspaceId: workspaceID, ExpectedAuthorUserId: accountA, Current: x2, RequestId: randomUUID(), ClientReported: &v1.ClientReportedProvenance{Model: ptr("third-model")}}
	s3, e := a.Publish(ctx, pub, link.RequestId)
	if e != nil {
		t.Fatal(e)
	}
	if s3.GetPublishedMessageId() != messageID {
		t.Fatal("explicit publish absent")
	}
	again, e := a.Retry(ctx, pub.RequestId)
	if e != nil || !proto.Equal(again, s3) {
		t.Fatal("consumed prepared retry changed immutable publication", e)
	}
	first, e := a.Retry(ctx, r.RequestId)
	if e != nil || first.Receipt.ClientReported.GetModel() != "first-model" {
		t.Fatal("model switch relabelled first", e)
	}
	f.mu.Lock()
	f.snapshot = s3
	f.mu.Unlock()
	got, e := a.Snapshot(ctx, &v1.GetContentSnapshotRequest{WorkspaceId: workspaceID, ThreadId: threadID, Record: &v1.GetContentSnapshotRequest_PublishedMessageId{PublishedMessageId: messageID}}, pub.RequestId)
	if e != nil || got.Verification != "retained-client-expectation" {
		t.Fatal(e)
	}
	got, e = a.Snapshot(ctx, &v1.GetContentSnapshotRequest{WorkspaceId: workspaceID, ThreadId: threadID, Record: &v1.GetContentSnapshotRequest_PublishedMessageId{PublishedMessageId: messageID}}, "")
	if e != nil || got.Verification != "receipt-consistency-only" {
		t.Fatal(e)
	}
}
func TestContentResponseSubstitutionRejected(t *testing.T) {
	for _, kind := range []string{"content", "owner", "connection", "request", "reported", "record", "origin", "presence", "timestamp"} {
		t.Run(kind, func(t *testing.T) {
			a, f, _, _ := setupContent(t)
			f.mu.Lock()
			f.tamper = kind
			f.mu.Unlock()
			if _, e := a.Prepare(context.Background(), request(), ""); e == nil {
				t.Fatal("accepted substituted response", kind)
			}
		})
	}
}
func TestRetainedTamperingAndCapabilities(t *testing.T) {
	a, f, dir, _ := setupContent(t)
	ctx := context.Background()
	r := request()
	s, e := a.Prepare(ctx, r, "")
	if e != nil {
		t.Fatal(e)
	}
	if s.GetPreparedMessage() == nil {
		t.Fatal("missing private")
	}
	path := filepath.Join(dir, "attempt-"+r.RequestId+".json")
	raw, _ := os.ReadFile(path)
	raw = bytes.ReplaceAll(raw, []byte("first-model"), []byte("other-model"))
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	before := f.calls
	if _, e = a.Retry(ctx, r.RequestId); e == nil || f.calls != before {
		t.Fatal("tampered attempt reached network mutation")
	}
	f.mu.Lock()
	f.p.Config.Capabilities = []v1.ToolCapability{1}
	f.mu.Unlock()
	a.profile.Config.Capabilities = []v1.ToolCapability{1}
	if _, e = a.Prepare(ctx, request(), ""); e == nil || f.calls != before {
		t.Fatal("missing prepare accepted")
	}
	f.mu.Lock()
	f.deny = true
	f.mu.Unlock()
	if _, e = a.Workspaces(ctx); e == nil || strings.Contains(e.Error(), "DO-NOT-LEAK") {
		t.Fatal("revocation/error redaction failed")
	}
}
func TestStrictContentInput(t *testing.T) {
	valid := `{"content":{"formatVersion":1,"expectedAuthorUserId":"` + accountA + `","workspaceId":"` + workspaceID + `","title":"x","originalDestinationIntent":"Pedro","destination":{"unlinkedLabel":"Pedro"},"summary":"e\u0301\r\n","contextMarkdown":"code"}}`
	for _, raw := range []string{strings.Replace(valid, `"title":"x"`, `"title":"x","title":"y"`, 1), strings.Replace(valid, `"title":"x"`, `"unknown":"x"`, 1), strings.Replace(valid, `"title":"x"`, `"title":null`, 1), strings.Replace(valid, `"unlinkedLabel":"Pedro"`, `"unlinkedLabel":"Pedro","linkedMemberId":""`, 1), strings.Replace(valid, `"formatVersion":1`, `"format_version":1`, 1), strings.Replace(valid, "code", string([]byte{255}), 1)} {
		if e := ReadContentInput(strings.NewReader(raw), &v1.PrepareContentRequest{}); e == nil {
			t.Fatal("strict gate accepted malformed input")
		}
	}
	var r v1.PrepareContentRequest
	if e := ReadContentInput(strings.NewReader(valid), &r); e != nil || r.Content.Summary != "e\u0301\r\n" {
		t.Fatal("exact decoder failed", e)
	}
	if e := ReadContentInput(strings.NewReader(strings.Repeat(" ", maxResponseBytes+1)), &r); e == nil {
		t.Fatal("oversized accepted")
	}
}
func cliCall(t *testing.T, binary string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = testsupport.FixtureCLIEnv()
	return cmd.CombinedOutput()
}
func TestCompiledCLIProcessCrashRetryAndLocking(t *testing.T) {
	a, f, dir, certificate := setupContent(t)
	binary := testsupport.BuildCLI(t, "../../cmd/syntra", certificate)
	r := request()
	if !bytes.Contains([]byte(r.Content.Summary), []byte{13, 10}) || !bytes.Contains([]byte(r.Content.Summary), []byte{0x65, 0xcc, 0x81}) {
		t.Fatal("compiled fixture lacks actual CRLF/NFD")
	}
	raw, _ := protojson.Marshal(r)
	input := filepath.Join(t.TempDir(), "request.json")
	if e := os.WriteFile(input, raw, 0600); e != nil {
		t.Fatal(e)
	}
	secret := a.profile.Secret
	a.store.Close()
	f.mu.Lock()
	f.hold = true
	f.mu.Unlock()
	cmd := exec.Command(binary, "prepare", "--profile-dir", dir, "--file", input)
	cmd.Env = testsupport.FixtureCLIEnv()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.committed:
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("prepare did not reach committed fixture", output.String())
	}
	// The first live process owns flock; a second binary must time out, not send.
	out, e := cliCall(t, binary, "retry", "--profile-dir", dir, "--request", r.RequestId, "--timeout", "80ms")
	if e == nil || !bytes.Contains(out, []byte("lock")) {
		t.Fatal("concurrent process bypassed kernel lock", e, string(out))
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	cmd.Wait()
	out, e = cliCall(t, binary, "retry", "--profile-dir", dir, "--request", r.RequestId)
	if e != nil {
		t.Fatal(e, string(out))
	}
	var got v1.AuthoredContentSnapshot
	if protojson.Unmarshal(out, &got) != nil || !proto.Equal(got.Content, r.Content) || got.Receipt.GetClientReported().GetModel() != "first-model" {
		t.Fatal("actual retry changed exact original")
	}
	if len(f.snapshots) != 1 || f.calls != 2 {
		t.Fatal("retry duplicated fixture commit")
	}
	if bytes.Contains(out, []byte(secret)) || strings.Contains(output.String(), secret) {
		t.Fatal("CLI leaked credential")
	}
	out, e = cliCall(t, binary, "resolve", "--profile-dir", dir, "--workspace", "Labs", "--project", "App", "--recipient", "Pedro")
	if e != nil || !bytes.Contains(out, []byte(accountB)) {
		t.Fatal("compiled discovery failed", e, string(out))
	}

	out, e = cliCall(t, binary, "messages", "--profile-dir", dir, "--workspace", workspaceID, "--thread", threadID)
	var history v1.ListMessagesResponse
	if e != nil || protojson.Unmarshal(out, &history) != nil || len(history.Messages) != 2 {
		t.Fatal("compiled message discovery failed", e, string(out))
	}
	out, e = cliCall(t, binary, "threads", "--profile-dir", dir, "--workspace", workspaceID, "--view", "own-prepared")
	var listing v1.ListThreadsResponse
	if e != nil || protojson.Unmarshal(out, &listing) != nil || len(listing.Threads) != 1 {
		t.Fatal("compiled explicit thread view failed", e, string(out))
	}
	current, _ := expectation(&got)
	linkedContent := proto.Clone(got.Content).(*v1.AuthoredContent)
	linkedContent.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: accountB}}
	link := &v1.LinkContentRequest{RequestId: randomUUID(), Current: current, LinkedContent: linkedContent, ClientReported: &v1.ClientReportedProvenance{Model: ptr("next-cli-model")}}
	raw, _ = protojson.Marshal(link)
	os.WriteFile(input, raw, 0600)
	out, e = cliCall(t, binary, "link", "--profile-dir", dir, "--file", input, "--base", r.RequestId)
	var linkedSnapshot v1.AuthoredContentSnapshot
	if e != nil || protojson.Unmarshal(out, &linkedSnapshot) != nil || linkedSnapshot.GetPublishedMessageId() != "" || linkedSnapshot.Content.OriginalDestinationIntent != r.Content.OriginalDestinationIntent {
		t.Fatal("compiled link changed intent/sent", e, string(out))
	}
	current, _ = expectation(&linkedSnapshot)
	publish := &v1.PublishContentRequest{RequestId: randomUUID(), WorkspaceId: workspaceID, ExpectedAuthorUserId: accountA, Current: current}
	raw, _ = protojson.Marshal(publish)
	os.WriteFile(input, raw, 0600)
	out, e = cliCall(t, binary, "publish", "--profile-dir", dir, "--file", input, "--base", link.RequestId)
	var published v1.AuthoredContentSnapshot
	if e != nil || protojson.Unmarshal(out, &published) != nil || published.GetPublishedMessageId() != messageID {
		t.Fatal("compiled explicit publication failed", e, string(out))
	}
	f.mu.Lock()
	f.snapshot = &published
	f.mu.Unlock()
	snapshotRequest := &v1.GetContentSnapshotRequest{WorkspaceId: workspaceID, ThreadId: threadID, Record: &v1.GetContentSnapshotRequest_PublishedMessageId{PublishedMessageId: messageID}}
	raw, _ = protojson.Marshal(snapshotRequest)
	os.WriteFile(input, raw, 0600)
	out, e = cliCall(t, binary, "snapshot", "--profile-dir", dir, "--file", input, "--base", publish.RequestId)
	if e != nil || !bytes.Contains(out, []byte("retained-client-expectation")) {
		t.Fatal("compiled retained snapshot failed", e, string(out))
	}
	f.mu.Lock()
	f.duplicate = true
	f.mu.Unlock()
	out, e = cliCall(t, binary, "resolve", "--profile-dir", dir, "--workspace", "Labs", "--project", "App")
	if e == nil || !bytes.Contains(out, []byte(`"complete":true`)) {
		t.Fatal("compiled ambiguity not structured", e, string(out))
	}
	out, e = cliCall(t, binary, "retry", "--profile-dir", dir, "--request", r.RequestId, "--api", "https://foreign.invalid")
	if e == nil || !bytes.Contains(out, []byte("origin")) {
		t.Fatal("origin override accepted")
	}

	for _, unused := range []string{"--file", "--base"} {
		out, e = cliCall(t, binary, "workspaces", "--profile-dir", dir, unused, input)
		if e == nil || !bytes.Contains(out, []byte("option unavailable for this command")) {
			t.Fatal("compiled workspaces silently accepted unused option", unused, e, string(out))
		}
	}
	// A different command payload with the same retained request ID is rejected.
	changed := proto.Clone(r).(*v1.PrepareContentRequest)
	changed.ClientReported.Model = ptr("later-model")
	raw, _ = protojson.Marshal(changed)
	os.WriteFile(input, raw, 0600)
	before := f.calls
	out, e = cliCall(t, binary, "prepare", "--profile-dir", dir, "--file", input)
	if e == nil || f.calls != before {
		t.Fatal("CLI relabelled retained request", e, string(out))
	}
}
func TestRecordNoOverwriteAndUnsafeObjects(t *testing.T) {
	a, _, dir, _ := setupContent(t)
	id := randomUUID()
	if e := a.store.retain("attempt", id, []byte("one")); e != nil {
		t.Fatal(e)
	}
	if e := a.store.retain("attempt", id, []byte("two")); e == nil {
		t.Fatal("overwrote original")
	}
	for _, kind := range []string{"symlink", "hardlink", "mode", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			id := randomUUID()
			name := filepath.Join(dir, "attempt-"+id+".sha256")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(filepath.Join(dir, "profile.json"), name)
				if symlinkPermissionUnavailable(err) {
					t.Skipf("creating a Windows symlink requires permission: %v", err)
				}
			case "hardlink":
				err = os.Link(filepath.Join(dir, "profile.json"), name)
			case "mode":
				if runtime.GOOS == "windows" {
					t.Skip("Windows enforces DACLs, covered by the Windows storage tests")
				}
				err = os.WriteFile(name, []byte("x"), 0600)
				if err == nil {
					err = os.Chmod(name, 0644)
				}
			case "fifo":
				if runtime.GOOS == "windows" {
					t.Skip("Windows filesystem paths cannot name a POSIX FIFO")
				}
				err = exec.Command("mkfifo", name).Run()
			}
			if err != nil {
				t.Fatal(err)
			}
			if e := a.store.retain("attempt", id, []byte("x")); e == nil {
				t.Fatal("unsafe retained record accepted", kind)
			}
		})
	}
}

func TestNilExplicitIDEditCASAndPublicationCapability(t *testing.T) {
	a, f, _, _ := setupContent(t)
	ctx := context.Background()
	for _, call := range []func() error{func() error { _, e := a.Prepare(ctx, nil, ""); return e }, func() error { _, e := a.Link(ctx, nil, ""); return e }, func() error { _, e := a.Publish(ctx, nil, ""); return e }} {
		if call() == nil {
			t.Fatal("nil input accepted")
		}
	}
	q := request()
	q.RequestId = ""
	if _, e := a.Prepare(ctx, q, ""); e == nil {
		t.Fatal("implicit request ID accepted")
	}
	q = request()
	s, e := a.Prepare(ctx, q, "")
	if e != nil {
		t.Fatal(e)
	}
	x, _ := expectation(s)
	edited := proto.Clone(s.Content).(*v1.AuthoredContent)
	edited.Summary = "new exact summary\r\n"
	edit := &v1.PrepareContentRequest{Content: edited, RequestId: randomUUID(), Update: x}
	s2, e := a.Prepare(ctx, edit, q.RequestId)
	if e != nil || s2.Content.Summary != edited.Summary {
		t.Fatal("permitted edit failed", e)
	}
	bad := proto.Clone(edit).(*v1.PrepareContentRequest)
	bad.RequestId = randomUUID()
	other := randomUUID()
	bad.Content.ExistingThreadId = &other
	before := f.calls
	if _, e = a.Prepare(ctx, bad, q.RequestId); e == nil || f.calls != before {
		t.Fatal("edit changed the thread identity")
	}
	bad = proto.Clone(edit).(*v1.PrepareContentRequest)
	bad.RequestId = randomUUID()
	bad.Update.ExpectedThreadRevision = rev(99)
	if _, e = a.Prepare(ctx, bad, q.RequestId); e == nil || f.calls != before {
		t.Fatal("stale CAS reached mutation")
	}
	// Prepare a linked private record, then deny publication locally without PUBLISH.
	linked := request()
	linked.Content.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: accountB}}
	ls, e := a.Prepare(ctx, linked, "")
	if e != nil {
		t.Fatal(e)
	}
	lx, _ := expectation(ls)
	pub := &v1.PublishContentRequest{WorkspaceId: workspaceID, ExpectedAuthorUserId: accountA, Current: lx, RequestId: randomUUID()}
	f.mu.Lock()
	f.p.Config.Capabilities = []v1.ToolCapability{1, 2}
	f.mu.Unlock()
	a.profile.Config.Capabilities = []v1.ToolCapability{1, 2}
	before = f.calls
	if _, e = a.Publish(ctx, pub, linked.RequestId); e == nil || f.calls != before {
		t.Fatal("missing PUBLISH accepted")
	}
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	if _, e = a.Prepare(ctx, request(), ""); e == nil || strings.Contains(e.Error(), "DO-NOT-LEAK") {
		t.Fatal("closed mutation/error handling failed")
	}
}
func TestStoredResultTamperAndImmutableRetryMetadata(t *testing.T) {
	a, f, dir, _ := setupContent(t)
	ctx := context.Background()
	q := request()
	if _, e := a.Prepare(ctx, q, ""); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.tamper = "thread"
	f.mu.Unlock()
	if _, e := a.Retry(ctx, q.RequestId); e == nil {
		t.Fatal("retry changed immutable resource ID")
	}
	f.mu.Lock()
	f.tamper = ""
	f.mu.Unlock()
	path := filepath.Join(dir, "result-"+q.RequestId+".json")
	raw, _ := os.ReadFile(path)
	raw = bytes.ReplaceAll(raw, []byte("first-model"), []byte("other-model"))
	os.WriteFile(path, raw, 0600)
	before := f.calls
	if _, e := a.Retry(ctx, q.RequestId); e == nil || f.calls != before {
		t.Fatal("tampered local receipt reached mutation")
	}
}

func TestRetainedRecordDirectorySyncOnFirstAndExistingRetry(t *testing.T) {
	a, _, _, _ := setupContent(t)
	calls := 0
	syncDir := func() error {
		calls++
		return nil
	}
	name := "attempt-" + randomUUID() + ".json"
	if e := a.store.writeOnceWithSync(name, []byte("exact"), syncDir); e != nil || calls != 1 {
		t.Fatal("first record not synced", e, calls)
	}
	if e := a.store.writeOnceWithSync(name, []byte("exact"), syncDir); e != nil || calls != 2 {
		t.Fatal("existing retry skipped sync", e, calls)
	}
	failure := func() error { return errors.New("injected-dir-sync-failure") }
	if e := a.store.writeOnceWithSync(name, []byte("exact"), failure); e == nil {
		t.Fatal("existing retry ignored directory sync failure")
	}
	newName := "attempt-" + randomUUID() + ".json"
	if e := a.store.writeOnceWithSync(newName, []byte("exact"), failure); e == nil {
		t.Fatal("new record ignored directory sync failure")
	}
	if e := a.store.writeOnceWithSync(newName, []byte("exact"), failure); e == nil {
		t.Fatal("visible interrupted install bypassed retry durability")
	}
}

func TestActualUnicodeCRLFAndLiteralBackslashRemainDistinct(t *testing.T) {
	a, _, _, _ := setupContent(t)
	q := request()
	if !bytes.Contains([]byte(q.Content.Summary), []byte{13, 10}) || !bytes.Contains([]byte(q.Content.Summary), []byte{0x65, 0xcc, 0x81}) {
		t.Fatal("fixture must contain actual CRLF and decomposed e-acute")
	}
	first, e := a.Prepare(context.Background(), q, "")
	if e != nil {
		t.Fatal(e)
	}
	literal := request()
	literal.Content.Summary = `  exact\r\n e\u0301  `
	second, e := a.Prepare(context.Background(), literal, "")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(first.Receipt.ContentDigest, second.Receipt.ContentDigest) || first.Content.Summary == second.Content.Summary {
		t.Fatal("literal backslashes normalized into actual Unicode/newlines")
	}
}
func TestSnapshotMessageContextAndPrivateOwnerSubstitution(t *testing.T) {
	for _, kind := range []string{"good", "prepared-workspace", "prepared-thread", "prepared-owner", "published-workspace", "published-thread", "authored-private-owner", "authored-missing-thread", "authored-missing-record", "authored-cross-thread"} {
		t.Run(kind, func(t *testing.T) {
			a, f, _, _ := setupContent(t)
			ctx := context.Background()
			d := &v1.PreparedMessage{Id: preparedID, WorkspaceId: workspaceID, ThreadId: threadID, AuthorUserId: accountA, Revision: rev(1)}
			l := &v1.GetContentSnapshotResponse{Thread: &v1.Thread{Id: threadID, WorkspaceId: workspaceID}, Record: &v1.GetContentSnapshotResponse_CurrentPreparedMessage{CurrentPreparedMessage: d}}
			r := &v1.GetContentSnapshotRequest{WorkspaceId: workspaceID, ThreadId: threadID, Record: &v1.GetContentSnapshotRequest_PreparedMessage{PreparedMessage: &v1.PrivateContentRevision{PreparedMessageId: preparedID, Revision: rev(1)}}}
			switch kind {
			case "prepared-workspace":
				d.WorkspaceId = accountB
			case "prepared-thread":
				d.ThreadId = accountB
			case "prepared-owner":
				d.AuthorUserId = accountB
			case "published-workspace", "published-thread":
				m := &v1.PublishedMessage{Id: messageID, WorkspaceId: workspaceID, ThreadId: threadID}
				if kind == "published-workspace" {
					m.WorkspaceId = accountB
				} else {
					m.ThreadId = accountB
				}
				l.Record = &v1.GetContentSnapshotResponse_PublishedMessage{PublishedMessage: m}
				r.Record = &v1.GetContentSnapshotRequest_PublishedMessageId{PublishedMessageId: messageID}
			case "authored-missing-thread", "authored-missing-record", "authored-cross-thread":
				original, e := a.Prepare(ctx, request(), "")
				if e != nil {
					t.Fatal(e)
				}
				malformed := snapshotRecord(original)
				switch kind {
				case "authored-missing-thread":
					malformed.Thread = nil
				case "authored-missing-record":
					malformed.Record = nil
				case "authored-cross-thread":
					malformed.Thread.Id = accountB
				}
				f.mu.Lock()
				f.record = malformed
				f.mu.Unlock()
				got, e := a.Snapshot(ctx, r, "")
				if e == nil || got != nil {
					t.Fatal("authored evidence bypassed common message identity binding")
				}
				return

			case "authored-private-owner":
				q := request()
				s, e := a.Prepare(ctx, q, "")
				if e != nil {
					t.Fatal(e)
				}
				other := proto.Clone(s).(*v1.AuthoredContentSnapshot)
				other.Content.ExpectedAuthorUserId = accountB
				other.Receipt.OwnerUserId = accountB
				other.Receipt.ContentDigest, _ = digest(other.Content)
				f.mu.Lock()
				f.snapshot = other
				f.mu.Unlock()
				got, e := a.Snapshot(ctx, r, "")
				if e == nil || got != nil {
					t.Fatal("foreign authored private owner accepted")
				}
				return
			}
			f.mu.Lock()
			f.record = l
			f.mu.Unlock()
			got, e := a.Snapshot(ctx, r, "")
			if kind == "good" {
				if e != nil || got.Verification != "server-record" || got.Snapshot != nil {
					t.Fatal("record invented integrity", e)
				}
			} else if e == nil || got != nil {
				t.Fatal("inconsistent record projection accepted")
			}
		})
	}
}
func TestDirectoryAggregateByteBoundAndCursorLoop(t *testing.T) {
	ctx := context.Background()
	calls := 0
	_, e := pages(ctx, &App{store: &Store{}, profile: &Profile{}}, func(context.Context, string) ([]*v1.Project, string, error) {
		calls++
		rows := []*v1.Project{}
		for i := 0; i < 8; i++ {
			rows = append(rows, &v1.Project{Id: fmt.Sprintf("%08x-5555-4555-8555-555555555555", calls*10+i), Description: strings.Repeat("x", 200000)})
		}
		return rows, fmt.Sprint(calls), nil
	}, func(p *v1.Project) string { return p.Id })
	if e == nil || !strings.Contains(e.Error(), "byte bound") || calls > 7 {
		t.Fatal("aggregate output unbounded", e, calls)
	}
	calls = 0
	_, e = pages(ctx, &App{store: &Store{}, profile: &Profile{}}, func(context.Context, string) ([]*v1.Project, string, error) {
		calls++
		return []*v1.Project{{Id: fmt.Sprintf("%08x-5555-4555-8555-555555555555", calls)}}, "same", nil
	}, func(p *v1.Project) string { return p.Id })
	if e == nil || calls != 2 {
		t.Fatal("cursor loop not bounded", e, calls)
	}
}

func (f *contentFixture) ListMessages(_ context.Context, r *connect.Request[v1.ListMessagesRequest]) (*connect.Response[v1.ListMessagesResponse], error) {
	if r.Msg.View == v1.MessageView_MESSAGE_VIEW_OWN_PREPARED {
		return connect.NewResponse(&v1.ListMessagesResponse{PreparedMessages: []*v1.PreparedMessage{{Id: preparedID, WorkspaceId: workspaceID, ThreadId: threadID, AuthorUserId: accountA}}}), nil
	}
	if r.Msg.View != v1.MessageView_MESSAGE_VIEW_PUBLISHED {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("view required"))
	}
	if r.Msg.PageToken == "" {
		return connect.NewResponse(&v1.ListMessagesResponse{Messages: []*v1.PublishedMessage{{Id: messageID, WorkspaceId: workspaceID, ThreadId: threadID}}, NextPageToken: "later"}), nil
	}
	return connect.NewResponse(&v1.ListMessagesResponse{Messages: []*v1.PublishedMessage{{Id: otherID, WorkspaceId: workspaceID, ThreadId: threadID}}}), nil
}
func (f *contentFixture) ListThreads(_ context.Context, r *connect.Request[v1.ListThreadsRequest]) (*connect.Response[v1.ListThreadsResponse], error) {
	if r.Msg.Visibility != v1.ThreadVisibility_THREAD_VISIBILITY_OWN_PREPARED && r.Msg.Visibility != v1.ThreadVisibility_THREAD_VISIBILITY_PUBLISHED {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("explicit visibility required"))
	}
	return connect.NewResponse(&v1.ListThreadsResponse{Threads: []*v1.Thread{{Id: threadID, WorkspaceId: workspaceID}}}), nil
}
func (f *contentFixture) GetThread(_ context.Context, r *connect.Request[v1.GetThreadRequest]) (*connect.Response[v1.GetThreadResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threadReads++
	state := f.threadState
	if f.threadPrivate {
		state = v1.ThreadState_THREAD_STATE_PENDING
	}
	return connect.NewResponse(&v1.GetThreadResponse{Thread: &v1.Thread{Id: r.Msg.ThreadId, WorkspaceId: r.Msg.WorkspaceId, CreatorUserId: accountB, Title: "Fixture title", ProjectId: projectID, State: state, Revision: rev(88)}}), nil
}
func TestCompleteMessagesAndExplicitThreadViews(t *testing.T) {
	a, _, _, _ := setupContent(t)
	ctx := context.Background()
	messages, e := a.Messages(ctx, workspaceID, threadID, v1.MessageView_MESSAGE_VIEW_PUBLISHED)
	if e != nil || len(messages.Messages) != 2 || messages.NextPageToken != "" {
		t.Fatal("complete publication discovery failed", e)
	}
	prepared_messages, e := a.Messages(ctx, workspaceID, threadID, v1.MessageView_MESSAGE_VIEW_OWN_PREPARED)
	if e != nil || len(prepared_messages.PreparedMessages) != 1 || len(prepared_messages.Messages) != 0 {
		t.Fatal("private message view failed", e)
	}
	if _, e = a.Messages(ctx, workspaceID, threadID, v1.MessageView_MESSAGE_VIEW_UNSPECIFIED); e == nil {
		t.Fatal("implicit message view")
	}
	for _, view := range []v1.ThreadVisibility{v1.ThreadVisibility_THREAD_VISIBILITY_OWN_PREPARED, v1.ThreadVisibility_THREAD_VISIBILITY_PUBLISHED} {
		rows, e := a.Threads(ctx, workspaceID, view)
		if e != nil || len(rows) != 1 {
			t.Fatal("explicit thread view failed", e)
		}
	}
}
func TestReturnedThreadCASModeRules(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"edit-preserves", "link-initial-advances", "link-reply-preserves", "publish-advances"} {
		t.Run(kind, func(t *testing.T) {
			a, f, _, _ := setupContent(t)
			q := request()
			if kind == "link-reply-preserves" {
				q.Content.ExistingThreadId = ptr(threadID)
				q.Content.ProjectId = ptr(projectID)
				q.Content.Title = "Fixture title"
				q.ExpectedThreadRevision = ptr(rev(88))
			}
			s, e := a.Prepare(ctx, q, "")
			if e != nil {
				t.Fatal(e)
			}
			x, _ := expectation(s)
			switch kind {
			case "edit-preserves":
				edited := proto.Clone(s.Content).(*v1.AuthoredContent)
				edited.Summary = "edit"
				f.mu.Lock()
				f.tamper = "thread-cas"
				f.mu.Unlock()
				_, e = a.Prepare(ctx, &v1.PrepareContentRequest{RequestId: randomUUID(), Content: edited, Update: x}, q.RequestId)
			case "link-initial-advances", "link-reply-preserves":
				c := proto.Clone(s.Content).(*v1.AuthoredContent)
				c.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: accountB}}
				f.mu.Lock()
				f.tamper = "same-thread-cas"
				if kind == "link-reply-preserves" {
					f.tamper = "thread-cas"
				}
				f.mu.Unlock()
				_, e = a.Link(ctx, &v1.LinkContentRequest{RequestId: randomUUID(), Current: x, LinkedContent: c}, q.RequestId)
			case "publish-advances":
				linked := request()
				linked.Content.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: accountB}}
				s, e = a.Prepare(ctx, linked, "")
				if e != nil {
					t.Fatal(e)
				}
				x, _ = expectation(s)
				f.mu.Lock()
				f.tamper = "same-thread-cas"
				f.mu.Unlock()
				_, e = a.Publish(ctx, &v1.PublishContentRequest{RequestId: randomUUID(), WorkspaceId: workspaceID, ExpectedAuthorUserId: accountA, Current: x}, linked.RequestId)
			}
			if e == nil {
				t.Fatal("incorrect returned thread CAS accepted", kind)
			}
		})
	}
}

func TestPendingRetainedAnchorRejectsTamperedRequest(t *testing.T) {
	a, f, dir, _ := setupContent(t)
	ctx := context.Background()
	q := request()
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	if _, e := a.Prepare(ctx, q, ""); e == nil {
		t.Fatal("fixture must leave pending request")
	}
	path := filepath.Join(dir, "attempt-"+q.RequestId+".json")
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	raw = bytes.ReplaceAll(raw, []byte("first-model"), []byte("other-model"))
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.closed = false
	before := f.calls
	f.mu.Unlock()
	if _, e := a.Retry(ctx, q.RequestId); e == nil || f.calls != before {
		t.Fatal("pending tampered request reached network mutation")
	}
}

func TestRetainedAccountAndOriginMismatchBeforeReplay(t *testing.T) {
	a, f, _, _ := setupContent(t)
	ctx := context.Background()
	q := request()
	if _, e := a.Prepare(ctx, q, ""); e != nil {
		t.Fatal(e)
	}
	before := f.calls
	original := a.profile.AccountID
	a.profile.AccountID = accountB
	if _, e := a.Retry(ctx, q.RequestId); e == nil || f.calls != before {
		t.Fatal("foreign account disclosed/replayed retained request")
	}
	a.profile.AccountID = original
	foreign, e := NewClient("https://other.invalid", nil)
	if e != nil {
		t.Fatal(e)
	}
	a.client = foreign
	if _, e := a.Retry(ctx, q.RequestId); e == nil || f.calls != before {
		t.Fatal("foreign origin replayed retained request")
	}
}

func TestCursorErrorRestartBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        connect.Code
		persistent  bool
		first       bool
		wantCalls   int
		wantSuccess bool
	}{
		{"released-invalid-argument-expiry", connect.CodeInvalidArgument, false, false, 4, true},
		{"persistent-invalid-argument-expiry", connect.CodeInvalidArgument, true, false, 4, false},
		{"failed-precondition-expiry", connect.CodeFailedPrecondition, false, false, 4, true},
		{"permission-denied-never-restarts", connect.CodePermissionDenied, true, false, 2, false},
		{"unauthenticated-never-restarts", connect.CodeUnauthenticated, true, false, 2, false},
		{"invalid-first-page-never-restarts", connect.CodeInvalidArgument, true, true, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			expired := false
			rows, e := pages(context.Background(), &App{store: &Store{}, profile: &Profile{}}, func(_ context.Context, token string) ([]*v1.Project, string, error) {
				calls++
				if tc.first || token != "" && (!expired || tc.persistent) {
					expired = true
					return nil, "", connect.NewError(tc.code, errors.New("do-not-inspect-upstream-text"))
				}
				if token == "" {
					return []*v1.Project{{Id: projectID, Name: "App"}}, "later", nil
				}
				return []*v1.Project{{Id: otherID, Name: "Other"}}, "", nil
			}, func(p *v1.Project) string { return p.Id })
			if calls != tc.wantCalls {
				t.Fatalf("cursor error restart count %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantSuccess {
				if e != nil || len(rows) != 2 {
					t.Fatal("one-expiry refresh did not complete", e)
				}
			} else {
				if e == nil || rows != nil {
					t.Fatal("error returned partial/unique candidates", e)
				}
				if strings.Contains(e.Error(), "do-not-inspect") {
					t.Fatal("upstream body leaked")
				}
			}
		})
	}
}

func TestPrepareReportsInvalidReplyFieldsWithoutContent(t *testing.T) {
	a, _, _, _ := setupContent(t)
	for _, tc := range []struct {
		name   string
		change func(*v1.PrepareContentRequest)
		field  string
	}{
		{"missing title", func(r *v1.PrepareContentRequest) { r.Content.Title = "" }, "content.title"},
		{"missing revision", func(r *v1.PrepareContentRequest) { r.Content.ExistingThreadId = ptr(projectID) }, "expectedThreadRevision"},
		{"documented numeric revision", func(r *v1.PrepareContentRequest) {
			r.Content.ExistingThreadId = ptr(projectID)
			r.ExpectedThreadRevision = ptr("12")
		}, "expectedThreadRevision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request()
			r.Content.Summary = "PRIVATE-SENTINEL"
			tc.change(r)
			_, err := a.Prepare(context.Background(), r, "")
			if !errors.Is(err, ci.ErrWire) || !strings.Contains(err.Error(), tc.field) || strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
				t.Fatalf("expected safe field diagnostic %q; got %v", tc.field, err)
			}
		})
	}
}

func TestCompiledCLIReplyRevision(t *testing.T) {
	a, f, dir, ca := setupContent(t)
	binary := testsupport.BuildCLI(t, "../../cmd/syntra", ca)
	r := request()
	r.Content.ProjectId = ptr(projectID)
	r.Content.Title = "Fixture title"
	r.Content.ExistingThreadId = ptr(threadID)
	r.Content.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: accountB}}
	input := t.TempDir() + "/reply.json"
	a.store.Close()
	for _, revision := range []string{"12", rev(88)} {
		r.ExpectedThreadRevision = ptr(revision)
		raw, err := protojson.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, raw, 0600); err != nil {
			t.Fatal(err)
		}
		out, err := cliCall(t, binary, "prepare", "--profile-dir", dir, "--file", input)
		if revision == "12" {
			if err == nil || !bytes.Contains(out, []byte("expectedThreadRevision")) || f.calls != 0 {
				t.Fatalf("invalid revision: %v %s", err, out)
			}
		} else if err != nil || f.calls != 1 {
			t.Fatalf("valid revision: %v %s", err, out)
		}
	}
}

func TestNestedThreadRevisionNormalization(t *testing.T) {
	raw := `{"requestId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","content":{"formatVersion":1,"expectedAuthorUserId":"11111111-1111-4111-8111-111111111111","workspaceId":"22222222-2222-4222-8222-222222222222","projectId":"33333333-3333-4333-8333-333333333333","existingThreadId":"44444444-4444-4444-8444-444444444444","expectedThreadRevision":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","title":"...","originalDestinationIntent":"...","summary":"...","contextMarkdown":"","destination":{"unlinkedLabel":"..."}},"clientReported":{"tool":"Claude Code","model":"..."}}`
	var request v1.PrepareContentRequest
	var correction string
	if err := ReadContentInputWithCorrections(strings.NewReader(raw), &request, func(s string) { correction = s }); err != nil {
		t.Fatal(err)
	}
	if request.GetExpectedThreadRevision() != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" || correction == "" {
		t.Fatalf("revision or correction missing")
	}
	first, err := digest(request.Content)
	if err != nil {
		t.Fatal(err)
	}
	correct := strings.Replace(raw, `"content":{`, `"expectedThreadRevision":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","content":{`, 1)
	correct = strings.Replace(correct, `"expectedThreadRevision":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","title"`, `"title"`, 1)
	var canonical v1.PrepareContentRequest
	if err := ReadContentInput(strings.NewReader(correct), &canonical); err != nil {
		t.Fatal(err)
	}
	second, err := digest(canonical.Content)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("normalization changed content digest")
	}
	if err := ReadContentInput(strings.NewReader(strings.Replace(raw, `"summary":"..."`, `"summary":"TODO: SYNTRA summary"`, 1)), &request); err == nil {
		t.Fatal("placeholder accepted")
	}
}

func TestCompiledReplyPublishesOnlyOnExplicitCommand(t *testing.T) {
	a, f, dir, ca := setupContent(t)
	binary := testsupport.BuildCLI(t, "../../cmd/syntra", ca)
	a.store.Close()
	contextPath := filepath.Join(t.TempDir(), "context.md")
	if err := os.WriteFile(contextPath, []byte("Details"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := cliCall(t, binary, "reply", "--profile-dir", dir, "--workspace", workspaceID, "--thread", threadID, "--summary", "Resolved", "--context-file", contextPath)
	if err != nil {
		t.Fatalf("reply: %v %s", err, out)
	}
	parts := strings.SplitN(string(out), "Publish explicitly with:\n", 2)
	if len(parts) != 2 || !strings.Contains(parts[0], preparedID) {
		t.Fatalf("missing next command: %s", out)
	}
	f.mu.Lock()
	before, reads := f.calls, f.threadReads
	f.mu.Unlock()
	if before != 1 || reads != 1 {
		t.Fatalf("reply sent %d mutations and %d thread reads", before, reads)
	}
	cmd := exec.Command("bash", "-c", strings.TrimSpace(parts[1]))
	cmd.Env = append(testsupport.FixtureCLIEnv(), "PATH="+filepath.Dir(binary)+":"+os.Getenv("PATH"))
	published, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("publish: %v %s", err, published)
	}
	var snapshot v1.AuthoredContentSnapshot
	if protojson.Unmarshal(published, &snapshot) != nil || snapshot.GetPublishedMessageId() != messageID {
		t.Fatalf("not published: %s", published)
	}
}

func TestCompiledTemplateToPrepare(t *testing.T) {
	a, f, dir, ca := setupContent(t)
	binary := testsupport.BuildCLI(t, "../../cmd/syntra", ca)
	a.store.Close()
	out, err := cliCall(t, binary, "prepare", "--profile-dir", dir, "--template", "reply", "--workspace", workspaceID, "--thread", threadID)
	if err != nil {
		t.Fatalf("template: %v %s", err, out)
	}
	var raw map[string]any
	if err = json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	content := raw["content"].(map[string]any)
	if raw["expectedThreadRevision"] != rev(88) || content["originalDestinationIntent"] != "everyone in the thread" {
		t.Fatalf("incorrect template: %s", out)
	}
	input := filepath.Join(t.TempDir(), "reply.json")
	if err = os.WriteFile(input, out, 0600); err != nil {
		t.Fatal(err)
	}
	rejected, err := cliCall(t, binary, "prepare", "--profile-dir", dir, "--file", input)
	if err == nil || !bytes.Contains(rejected, []byte("TODO: SYNTRA")) {
		t.Fatalf("placeholder: %v %s", err, rejected)
	}
	content["originalDestinationIntent"] = "Pedro"
	content["summary"] = "Done"
	content["contextMarkdown"] = "Details"
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := cliCall(t, binary, "prepare", "--profile-dir", dir, "--file", input)
	if err != nil {
		t.Fatalf("prepare: %v %s", err, result)
	}
	var snap v1.AuthoredContentSnapshot
	if err = protojson.Unmarshal(result, &snap); err != nil || snap.Content.GetOriginalDestinationIntent() != "Pedro" || snap.GetPreparedMessage() == nil || f.calls != 1 {
		t.Fatalf("bad result %v %s", err, result)
	}
}

func snapshotRecord(s *v1.AuthoredContentSnapshot) *v1.GetContentSnapshotResponse {
	if s == nil {
		return &v1.GetContentSnapshotResponse{}
	}
	out := &v1.GetContentSnapshotResponse{Authored: s, Thread: &v1.Thread{Id: s.Receipt.ThreadId, WorkspaceId: s.Content.WorkspaceId}}
	if d := s.GetPreparedMessage(); d != nil {
		out.Record = &v1.GetContentSnapshotResponse_CurrentPreparedMessage{CurrentPreparedMessage: &v1.PreparedMessage{Id: d.PreparedMessageId, Revision: d.Revision, WorkspaceId: s.Content.WorkspaceId, ThreadId: s.Receipt.ThreadId, AuthorUserId: s.Content.ExpectedAuthorUserId, Summary: s.Content.Summary, ContextMarkdown: s.Content.ContextMarkdown}}
	} else {
		out.Record = &v1.GetContentSnapshotResponse_PublishedMessage{PublishedMessage: &v1.PublishedMessage{Id: s.GetPublishedMessageId(), WorkspaceId: s.Content.WorkspaceId, ThreadId: s.Receipt.ThreadId, AuthorUserId: s.Content.ExpectedAuthorUserId, Summary: s.Content.Summary, ContextMarkdown: s.Content.ContextMarkdown}}
	}
	return out
}

func TestHistoricalAuthoredEvidenceIsSeparateFromCurrentPrivateRecord(t *testing.T) {
	a, f, _, _ := setupContent(t)
	ctx := context.Background()
	old, e := a.Prepare(ctx, request(), "")
	if e != nil {
		t.Fatal(e)
	}
	response := snapshotRecord(old)
	response.GetCurrentPreparedMessage().Revision = rev(99)
	response.GetCurrentPreparedMessage().Summary = "Current edited server text"
	f.mu.Lock()
	f.record = response
	f.mu.Unlock()
	r := &v1.GetContentSnapshotRequest{WorkspaceId: workspaceID, ThreadId: threadID, Record: &v1.GetContentSnapshotRequest_PreparedMessage{PreparedMessage: old.GetPreparedMessage()}}
	got, e := a.Snapshot(ctx, r, "")
	if e != nil || !proto.Equal(got.Snapshot, old) || got.Snapshot.Content.Summary == response.GetCurrentPreparedMessage().Summary {
		t.Fatal("historical authored evidence conflated with current record", e)
	}
}

func TestResolveTeamAndSeveralPeopleDestinations(t *testing.T) {
	a, _, _, _ := setupContent(t)
	ctx := context.Background()
	// An explicit team destination: the whole selected project team, never inferred.
	r, e := a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Team: "QA", AsTeam: true})
	if e != nil || r.Destination["teamId"] != qaTeamID || r.RecipientID != nil {
		t.Fatalf("team destination: %+v %v", r, e)
	}
	// A recipient name that is a project team resolves to that team as a destination.
	r, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Recipient: "QA"})
	if e != nil || r.Destination["teamId"] != qaTeamID || *r.TeamID != qaTeamID {
		t.Fatalf("team by recipient name: %+v %v", r, e)
	}
	// Several people resolve to one set destination with unambiguous identifiers, in the given order.
	r, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Recipients: []string{"Pedro", "Roimer"}})
	set, _ := r.Destination["linkedMemberSet"].(map[string]any)
	if e != nil || set == nil || len(r.RecipientIDs) != 2 || r.RecipientIDs[0] != accountB || r.RecipientIDs[1] != accountA {
		t.Fatalf("set destination: %+v %v", r, e)
	}
	// One person keeps the single destination form.
	r, e = a.Resolve(ctx, Selection{Workspace: "Labs", Project: "App", Recipients: []string{"Pedro"}})
	if e != nil || r.Destination["linkedMemberId"] != accountB {
		t.Fatalf("single destination: %+v %v", r, e)
	}
	for name, s := range map[string]Selection{
		"duplicate":        {Workspace: "Labs", Project: "App", Recipients: []string{"Pedro", accountB}},
		"both forms":       {Workspace: "Labs", Project: "App", Recipient: "Pedro", Recipients: []string{"Roimer"}},
		"as team no team":  {Workspace: "Labs", Project: "App", AsTeam: true},
		"as team and name": {Workspace: "Labs", Project: "App", Team: "QA", AsTeam: true, Recipient: "Pedro"},
		"too many":         {Workspace: "Labs", Project: "App", Recipients: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}},
	} {
		if _, e = a.Resolve(ctx, s); e == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
