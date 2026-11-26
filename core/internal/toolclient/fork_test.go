package toolclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

func (f *contentFixture) ForkThread(_ context.Context, _ *connect.Request[v1.ForkThreadRequest]) (*connect.Response[v1.ForkThreadResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forkRequests++
	return connect.NewResponse(&v1.ForkThreadResponse{Thread: f.forkResponse}), nil
}

func TestForkRejectsMalformedMembersBeforeRPC(t *testing.T) {
	app, f, _, _ := setupContent(t)
	for _, q := range []*v1.ForkThreadRequest{
		{ExtraMemberIds: []string{"bad-id"}},
		{SourceParticipants: &v1.ForkParticipantSelection{UserIds: []string{"bad-id"}}},
	} {
		q.WorkspaceId, q.SourceThreadId, q.AnchorMessageId, q.RequestId = workspaceID, threadID, randomUUID(), randomUUID()
		q.Title = "Follow-up"
		if _, err := app.Fork(context.Background(), q); err == nil || !strings.Contains(err.Error(), "member ID") {
			t.Fatalf("expected a local member ID error, got %v", err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forkRequests != 0 {
		t.Fatal("invalid member ID reached the fork RPC")
	}
}

func TestForkRejectsMismatchedResponse(t *testing.T) {
	app, f, _, _ := setupContent(t)
	q := &v1.ForkThreadRequest{WorkspaceId: workspaceID, SourceThreadId: threadID, AnchorMessageId: randomUUID(), RequestId: randomUUID(), Title: "Follow-up"}
	for _, tc := range []struct {
		name   string
		change func(*v1.Thread)
	}{
		{"missing ID", func(thread *v1.Thread) { thread.Id = "" }},
		{"source ID", func(thread *v1.Thread) { thread.Id = threadID }},
		{"other workspace", func(thread *v1.Thread) { thread.WorkspaceId = randomUUID() }},
		{"other origin thread", func(thread *v1.Thread) { thread.ForkedFrom.ThreadId = randomUUID() }},
		{"other origin anchor", func(thread *v1.Thread) { thread.ForkedFrom.MessageId = randomUUID() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thread := &v1.Thread{Id: randomUUID(), WorkspaceId: workspaceID, ForkedFrom: &v1.ThreadForkOrigin{ThreadId: threadID, MessageId: q.AnchorMessageId}}
			tc.change(thread)
			f.mu.Lock()
			f.forkResponse = thread
			f.mu.Unlock()
			if _, err := app.Fork(context.Background(), q); !errors.Is(err, ErrProfile) {
				t.Fatalf("expected inconsistent response rejection, got %v", err)
			}
		})
	}
}
