package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/toolclient"
)

type replyOptions struct {
	Account, Workspace, ThreadID, MessageID, Summary, ContextMarkdown, ProfileDir string
}

func reply(ctx context.Context, app *toolclient.App, opts replyOptions) error {
	t, e := app.Thread(ctx, opts.Workspace, opts.ThreadID)
	if e != nil {
		return e
	}
	if e = toolclient.CheckReplyState(t); e != nil {
		return e
	}
	id, e := toolclient.NewRequestID()
	if e != nil {
		return e
	}
	next, e := toolclient.NewRequestID()
	if e != nil {
		return e
	}
	content := &v1.AuthoredContent{FormatVersion: 1, ExpectedAuthorUserId: opts.Account, WorkspaceId: opts.Workspace, ExistingThreadId: &t.Id, Title: t.Title, OriginalDestinationIntent: "everyone in the thread", Destination: &v1.ContentDestination{Target: &v1.ContentDestination_ReplyAll{ReplyAll: true}}, Summary: opts.Summary, ContextMarkdown: opts.ContextMarkdown}
	if opts.MessageID != "" {
		content.FormatVersion = 2
		content.ReplyToMessageId = &opts.MessageID
	}
	if t.ProjectId != "" {
		content.ProjectId = &t.ProjectId
	}
	snap, e := app.PrepareReply(ctx, &v1.PrepareContentRequest{Content: content, RequestId: id, ExpectedThreadRevision: &t.Revision}, t)
	if e != nil {
		return e
	}
	if snap.GetPreparedMessage() == nil || snap.Receipt == nil {
		return errors.New("prepared snapshot lacks a prepared message; retain requestId for retry")
	}
	request := &v1.PublishContentRequest{WorkspaceId: opts.Workspace, ExpectedAuthorUserId: opts.Account, RequestId: next, Current: &v1.PreparedContentExpectation{ThreadId: snap.Receipt.ThreadId, PreparedMessageId: snap.GetPreparedMessage().PreparedMessageId, ExpectedThreadRevision: snap.ThreadRevision, ExpectedPreparedMessageRevision: snap.GetPreparedMessage().Revision, ExpectedContentDigest: snap.Receipt.ContentDigest}}
	raw, e := protojson.Marshal(request)
	if e != nil {
		return fmt.Errorf("prepared message %s retained; requestId %s for recovery: cannot encode publish command", snap.GetPreparedMessage().PreparedMessageId, id)
	}
	return writePreparedReply(os.Stdout, snap.GetPreparedMessage().PreparedMessageId, id, raw, opts.ProfileDir)
}

func writePreparedReply(writer io.Writer, messageID, requestID string, raw []byte, profileDir string) error {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if _, e := fmt.Fprintf(writer, "Prepared message %s; retained request %s.\nThis reply is addressed to every other participant of the thread (reply to all).\nPublish explicitly with:\nprintf '%%s' %s | syntra publish --file - --base %s --profile-dir %s\n", messageID, requestID, quote(string(raw)), quote(requestID), quote(profileDir)); e != nil {
		return fmt.Errorf("prepared message %s retained; requestId %s for recovery: output failed", messageID, requestID)
	}
	return nil
}
