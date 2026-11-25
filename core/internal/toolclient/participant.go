package toolclient

import (
	"context"

	"connectrpc.com/connect"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

// AddParticipant includes an active workspace member in a published thread (open or closed) without sending a
// message. It needs the publishing capability of this connection and acts only for the connected human; the server
// enforces the same rule. A retry with the same requestId returns the same outcome. It never marks anything read or
// reviewed, and it never removes a participant.
func (a *App) AddParticipant(ctx context.Context, workspace, thread, member, requestID string) (*v1.AddThreadParticipantResponse, error) {
	if e := firstError(clientID("workspaceId", workspace), clientID("threadId", thread), clientID("userId", member), clientID("requestId", requestID)); e != nil {
		return nil, e
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_PUBLISH); e != nil {
		return nil, e
	}
	c := syntrav1connect.NewThreadServiceClient(a.client, a.client.origin, RPCOptions()...)
	r, e := c.AddThreadParticipant(ctx, bearer(connect.NewRequest(&v1.AddThreadParticipantRequest{WorkspaceId: workspace, ThreadId: thread, UserId: member, RequestId: requestID}), a.profile.Secret))
	if e != nil {
		return nil, a.authoredRPCError(e)
	}
	if r.Msg.Thread == nil || r.Msg.Thread.Id != thread || r.Msg.Thread.WorkspaceId != workspace {
		return nil, ErrProfile
	}
	return r.Msg, nil
}

func firstError(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
