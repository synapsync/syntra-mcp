package toolclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

// The fake server records every AddThreadParticipant it receives; the add path is proven by what reached it.
func (f *contentFixture) AddThreadParticipant(_ context.Context, r *connect.Request[v1.AddThreadParticipantRequest]) (*connect.Response[v1.AddThreadParticipantResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.participantRequests = append(f.participantRequests, r.Msg)
	return connect.NewResponse(&v1.AddThreadParticipantResponse{Thread: &v1.Thread{Id: r.Msg.ThreadId, WorkspaceId: r.Msg.WorkspaceId}, Added: true}), nil
}

var _ syntrav1connect.ThreadServiceHandler = (*contentFixture)(nil)

const addedUserID = "55555555-5555-4555-8555-555555555555"

func TestAddParticipantNeedsThePublishCapabilityBeforeAnyRequest(t *testing.T) {
	app, f, _, _ := setupContent(t)
	read, prepare := v1.ToolCapability_TOOL_CAPABILITY_READ, v1.ToolCapability_TOOL_CAPABILITY_PREPARE
	withoutPublish := []v1.ToolCapability{read, prepare}
	// The local profile and the connection the server reports carry the same reduced grant, so the denial comes from
	// the missing capability itself, not from a mismatch.
	f.mu.Lock()
	f.p.Config.Capabilities = withoutPublish
	f.mu.Unlock()
	app.profile.Config.Capabilities = withoutPublish

	_, e := app.AddParticipant(context.Background(), workspaceID, threadID, addedUserID, randomUUID())
	var missing *MissingCapability
	if !errors.As(e, &missing) || missing.Required != v1.ToolCapability_TOOL_CAPABILITY_PUBLISH {
		t.Fatalf("a connection without publish must be refused locally as a missing publish capability: %v", e)
	}
	if !strings.Contains(e.Error(), "--publish") {
		t.Fatalf("the denial must name the login step that grants publish: %q", e.Error())
	}
	f.mu.Lock()
	sent := len(f.participantRequests)
	f.mu.Unlock()
	if sent != 0 {
		t.Fatalf("a denied add reached the server %d times", sent)
	}
}

func TestAddParticipantSendsTheExactRequestForTheConnectedWorkspace(t *testing.T) {
	app, f, _, _ := setupContent(t)
	request := randomUUID()
	out, e := app.AddParticipant(context.Background(), workspaceID, threadID, addedUserID, request)
	if e != nil {
		t.Fatal(e)
	}
	if !out.Added || out.Thread.GetId() != threadID || out.Thread.GetWorkspaceId() != workspaceID {
		t.Fatalf("the response must carry the thread and whether the person was added: %+v", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.participantRequests) != 1 {
		t.Fatalf("want exactly one request, got %d", len(f.participantRequests))
	}
	got := f.participantRequests[0]
	if got.WorkspaceId != workspaceID || got.ThreadId != threadID || got.UserId != addedUserID || got.RequestId != request {
		t.Fatalf("the request must carry the arguments unchanged: %+v", got)
	}
}

func TestAddParticipantRejectsMalformedIdentifiersWithoutRequests(t *testing.T) {
	app, f, _, _ := setupContent(t)
	ctx := context.Background()
	for name, args := range map[string][4]string{
		"workspace":  {"NOT-A-UUID", threadID, addedUserID, randomUUID()},
		"thread":     {workspaceID, "", addedUserID, randomUUID()},
		"member":     {workspaceID, threadID, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", randomUUID()},
		"request id": {workspaceID, threadID, addedUserID, "retry"},
	} {
		_, e := app.AddParticipant(ctx, args[0], args[1], args[2], args[3])
		if e == nil {
			t.Fatalf("%s: a malformed identifier must be refused", name)
		}
		if IsConnectionDiagnostic(e) {
			t.Fatalf("%s: a local input error must not be reported as a connection problem: %v", name, e)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.participantRequests) != 0 {
		t.Fatalf("malformed input reached the server %d times", len(f.participantRequests))
	}
}
