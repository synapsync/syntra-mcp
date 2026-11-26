package toolclient

import (
	"context"

	"connectrpc.com/connect"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

// Fork creates an open thread and lets the server deduplicate an exact retry.
func (a *App) Fork(ctx context.Context, q *v1.ForkThreadRequest) (*v1.ForkThreadResponse, error) {
	if e := firstError(clientID("workspaceId", q.WorkspaceId), clientID("sourceThreadId", q.SourceThreadId), clientID("anchorMessageId", q.AnchorMessageId), clientID("requestId", q.RequestId)); e != nil {
		return nil, e
	}
	for _, ids := range [][]string{q.ExtraMemberIds, q.SourceParticipants.GetUserIds()} {
		for _, id := range ids {
			if e := clientID("member ID", id); e != nil {
				return nil, e
			}
		}
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_PUBLISH); e != nil {
		return nil, e
	}
	c := syntrav1connect.NewThreadServiceClient(a.client, a.client.origin, RPCOptions()...)
	r, e := c.ForkThread(ctx, bearer(connect.NewRequest(q), a.profile.Secret))
	if e != nil {
		mapped := ConnectionError(a.store.dir, a.profile, e)
		if IsConnectionDiagnostic(mapped) {
			return nil, mapped
		}
		if public := actionable.FromForkConnect(e); public != nil {
			return nil, public
		}
		return nil, SafeRPCError(e)
	}
	t := r.Msg.Thread
	if t.GetId() == "" || t.GetId() == q.SourceThreadId || t.GetWorkspaceId() != q.WorkspaceId ||
		t.GetForkedFrom().GetThreadId() != q.SourceThreadId || t.GetForkedFrom().GetMessageId() != q.AnchorMessageId {
		return nil, ErrProfile
	}
	return r.Msg, nil
}
