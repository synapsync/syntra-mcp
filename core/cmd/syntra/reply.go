package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/toolclient"
)

func freshRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("cannot generate requestId")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func reply(ctx context.Context, app *toolclient.App, account, workspace, ticketID, summary, contextFile, profileDir string) error {
	if workspace == "" || ticketID == "" || summary == "" || contextFile == "" {
		return errors.New("reply requires --workspace, --ticket, --summary and --context-file")
	}
	var input io.Reader = os.Stdin
	if contextFile != "-" {
		f, e := openRequestFile(contextFile)
		if e != nil {
			return errors.New("context file unavailable")
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Size() > 131072 {
			return errors.New("context file must be a bounded regular file (at most 131072 bytes)")
		}
		input = f
	}
	type readResult struct {
		b []byte
		e error
	}
	ch := make(chan readResult, 1)
	go func() { b, e := io.ReadAll(io.LimitReader(input, 131073)); ch <- readResult{b, e} }()
	var data []byte
	select {
	case v := <-ch:
		if v.e != nil || len(v.b) > 131072 {
			return errors.New("context file exceeds limit or cannot be read")
		}
		data = v.b
	case <-ctx.Done():
		return errors.New("context input cancelled or deadline exceeded")
	}
	t, e := app.Ticket(ctx, workspace, ticketID)
	if e != nil {
		return e
	}
	if t.CreatorUserId == "" || t.CreatorUserId == account {
		return errors.New("cannot infer a different recipient from ticket creator; use the prepare template and select a recipient")
	}
	members, e := app.Members(ctx, workspace)
	if e != nil {
		return e
	}
	name := ""
	for _, m := range members {
		if m.UserId == t.CreatorUserId {
			name = m.DisplayName
			break
		}
	}
	if name == "" {
		return errors.New("ticket creator is not an active named member; use the prepare template and resolve a recipient")
	}
	id, e := freshRequestID()
	if e != nil {
		return e
	}
	next, e := freshRequestID()
	if e != nil {
		return e
	}
	content := &v1.AuthoredContent{FormatVersion: 1, ExpectedAuthorUserId: account, WorkspaceId: workspace, ExistingTicketId: &t.Id, Title: t.Title, OriginalDestinationIntent: name, Destination: &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberId{LinkedMemberId: t.CreatorUserId}}, Summary: summary, ContextMarkdown: string(data)}
	if t.ProjectId != "" {
		content.ProjectId = &t.ProjectId
	}
	snap, e := app.Prepare(ctx, &v1.PrepareContentRequest{Content: content, RequestId: id, ExpectedTicketRevision: &t.Revision}, "")
	if e != nil {
		return e
	}
	if snap.GetPrivateDraft() == nil || snap.Receipt == nil {
		return errors.New("prepared snapshot lacks private draft; retain requestId for retry")
	}
	request := &v1.PublishContentRequest{WorkspaceId: workspace, ExpectedAuthorUserId: account, RequestId: next, Current: &v1.ContentDraftExpectation{TicketId: snap.Receipt.TicketId, DraftId: snap.GetPrivateDraft().DraftId, ExpectedTicketRevision: snap.TicketRevision, ExpectedDraftRevision: snap.GetPrivateDraft().Revision, ExpectedContentDigest: snap.Receipt.ContentDigest}}
	raw, e := protojson.Marshal(request)
	if e != nil {
		return fmt.Errorf("draft prepared; retain requestId %s for recovery: cannot encode publish command", id)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if _, e = fmt.Fprintf(os.Stdout, "Draft %s prepared; request %s.\nPublish explicitly with:\nprintf '%%s' %s | syntra publish --file - --base %s --profile-dir %s\n", snap.GetPrivateDraft().DraftId, id, quote(string(raw)), quote(id), quote(profileDir)); e != nil {
		return fmt.Errorf("draft prepared; retain requestId %s for recovery: output failed", id)
	}
	return nil
}
