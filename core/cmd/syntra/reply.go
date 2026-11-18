package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/contentintegrity/actionable"
	"syntra.local/mcp/internal/toolclient"
)

type replyOptions struct {
	Account, Workspace, TicketID, Summary, ContextMarkdown, ProfileDir string
}

func reply(ctx context.Context, app *toolclient.App, opts replyOptions) error {
	t, e := app.Ticket(ctx, opts.Workspace, opts.TicketID)
	if e != nil {
		return e
	}
	if e = toolclient.CheckReplyState(t); e != nil {
		return e
	}
	if t.CreatorUserId == "" || t.CreatorUserId == opts.Account {
		return errors.New("cannot infer a different recipient from ticket creator; use the prepare template and select a recipient")
	}
	members, e := app.Members(ctx, opts.Workspace)
	if e != nil {
		return e
	}
	var creator *v1.WorkspaceMember
	for _, m := range members {
		if m.UserId == t.CreatorUserId {
			creator = m
			break
		}
	}
	if creator == nil {
		return actionable.New(actionable.RecipientNotEligible)
	}
	if creator.DisplayName == "" {
		return errors.New("the ticket creator has no display name; use the prepare template and provide the recipient name")
	}
	id, e := toolclient.NewRequestID()
	if e != nil {
		return e
	}
	next, e := toolclient.NewRequestID()
	if e != nil {
		return e
	}
	content := &v1.AuthoredContent{FormatVersion: 1, ExpectedAuthorUserId: opts.Account, WorkspaceId: opts.Workspace, ExistingTicketId: &t.Id, Title: t.Title, OriginalDestinationIntent: creator.DisplayName, Destination: &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: t.CreatorUserId}}, Summary: opts.Summary, ContextMarkdown: opts.ContextMarkdown}
	if t.ProjectId != "" {
		content.ProjectId = &t.ProjectId
	}
	snap, e := app.PrepareReply(ctx, &v1.PrepareContentRequest{Content: content, RequestId: id, ExpectedTicketRevision: &t.Revision}, t)
	if e != nil {
		return e
	}
	if snap.GetPrivateDraft() == nil || snap.Receipt == nil {
		return errors.New("prepared snapshot lacks private draft; retain requestId for retry")
	}
	request := &v1.PublishContentRequest{WorkspaceId: opts.Workspace, ExpectedAuthorUserId: opts.Account, RequestId: next, Current: &v1.ContentDraftExpectation{TicketId: snap.Receipt.TicketId, DraftId: snap.GetPrivateDraft().DraftId, ExpectedTicketRevision: snap.TicketRevision, ExpectedDraftRevision: snap.GetPrivateDraft().Revision, ExpectedContentDigest: snap.Receipt.ContentDigest}}
	raw, e := protojson.Marshal(request)
	if e != nil {
		return fmt.Errorf("draft prepared; retain requestId %s for recovery: cannot encode publish command", id)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if _, e = fmt.Fprintf(os.Stdout, "Draft %s prepared; request %s.\nPublish explicitly with:\nprintf '%%s' %s | syntra publish --file - --base %s --profile-dir %s\n", snap.GetPrivateDraft().DraftId, id, quote(string(raw)), quote(id), quote(opts.ProfileDir)); e != nil {
		return fmt.Errorf("draft prepared; retain requestId %s for recovery: output failed", id)
	}
	return nil
}
