package toolclient

import (
	"context"
	"errors"
	"os"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

// CheckReplyState also serves the CLI template, which only reads a ticket.
func CheckReplyState(t *v1.Ticket) error {
	switch t.State {
	case v1.TicketState_TICKET_STATE_CLOSED:
		return actionable.New(actionable.TicketClosed)
	case v1.TicketState_TICKET_STATE_DRAFT:
		return actionable.New(actionable.TicketNotPublished)
	default:
		return nil
	}
}

func (a *App) preflightReply(ctx context.Context, r *v1.PrepareContentRequest, ticket *v1.Ticket) error {
	// A retained request must still reach the server's immutable retry path.
	// Its expected revision can predate a successful write.
	if _, err := a.store.retained("attempt", r.RequestId); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := r.Content
	if ticket == nil {
		var err error
		ticket, err = a.readTicket(ctx, content.WorkspaceId, content.GetExistingTicketId())
		if err != nil {
			return err
		}
	}
	if ticket.Id != content.GetExistingTicketId() || ticket.WorkspaceId != content.WorkspaceId {
		return ErrProfile
	}
	if err := CheckReplyState(ticket); err != nil {
		return err
	}
	revision := r.GetExpectedTicketRevision()
	if r.Update != nil {
		revision = r.Update.ExpectedTicketRevision
	}
	switch {
	case content.Title != ticket.Title:
		return actionable.TitleMismatch(ticket.Title)
	case content.GetProjectId() != ticket.ProjectId:
		return actionable.New(actionable.ProjectMismatch)
	case revision != ticket.Revision:
		return actionable.New(actionable.StaleTicketRevision)
	default:
		return nil
	}
}
