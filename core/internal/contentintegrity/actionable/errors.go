// Package actionable defines the public authored-content failure messages shared
// by the API and its CLI and MCP clients.
package actionable

import (
	"errors"
	"strconv"
	"strings"

	"connectrpc.com/connect"
)

type Reason string

const (
	TicketClosed                 Reason = "TICKET_CLOSED"
	TicketNotPublished           Reason = "TICKET_NOT_PUBLISHED"
	TicketTitleMismatch          Reason = "TICKET_TITLE_MISMATCH"
	ProjectMismatch              Reason = "PROJECT_MISMATCH"
	StaleTicketRevision          Reason = "STALE_TICKET_REVISION"
	StaleDraftRevision           Reason = "STALE_DRAFT_REVISION"
	NotAuthoredDraft             Reason = "NOT_AUTHORED_DRAFT"
	RecipientNotEligible         Reason = "RECIPIENT_NOT_ELIGIBLE"
	InitialDraftAlreadyPublished Reason = "INITIAL_DRAFT_ALREADY_PUBLISHED"
	RecipientNotLinked           Reason = "RECIPIENT_NOT_LINKED"
	DraftRoutingChange           Reason = "DRAFT_ROUTING_CHANGE"
	DraftAlreadyLinked           Reason = "DRAFT_ALREADY_LINKED"
	LinkContentChange            Reason = "LINK_CONTENT_CHANGE"
	RequestIDConflict            Reason = "REQUEST_ID_CONFLICT"
)

type guidance struct {
	code connect.Code
	text string
}

var reasons = map[Reason]guidance{
	TicketClosed:                 {connect.CodeFailedPrecondition, "The ticket is closed. Closed tickets accept no agent publication until a person reopens the ticket in the Syntra web app."},
	TicketNotPublished:           {connect.CodeFailedPrecondition, "The ticket is still a private draft. Publish its initial message before preparing or publishing a reply."},
	TicketTitleMismatch:          {connect.CodeFailedPrecondition, "A reply's title and project must match the ticket exactly. Use syntra prepare --template reply or syntra reply. Current ticket title: "},
	ProjectMismatch:              {connect.CodeFailedPrecondition, "The reply project differs from the ticket's project. Copy the exact projectId from syntra ticket, or omit it if the ticket has no project. Use syntra prepare --template reply to fill it automatically."},
	StaleTicketRevision:          {connect.CodeAborted, "The ticket revision is stale. Re-read it with syntra ticket and retry with the new revision and a fresh requestId."},
	StaleDraftRevision:           {connect.CodeAborted, "The draft revision is stale. Get the current draft revision with syntra messages --view own-drafts, read it with syntra snapshot, and retry with the current verified base and a fresh requestId."},
	NotAuthoredDraft:             {connect.CodeFailedPrecondition, "The private draft does not use authored content v1. Use syntra prepare to create a new authored draft."},
	RecipientNotEligible:         {connect.CodeNotFound, "The recorded recipient is no longer eligible for this workspace. Check syntra members, then prepare a new draft for an eligible recipient."},
	InitialDraftAlreadyPublished: {connect.CodeFailedPrecondition, "This initial draft cannot be published on an already published ticket. Use syntra ticket to refresh the state, then prepare a reply with syntra prepare --template reply."},
	RecipientNotLinked:           {connect.CodeInvalidArgument, "The draft has no linked recipient. Resolve an active recipient with syntra members and link the draft with syntra link before publishing."},
	DraftRoutingChange:           {connect.CodeInvalidArgument, "A draft update may change only the summary and Markdown. Keep its ticket, title, project and recipient unchanged, or prepare a new draft."},
	DraftAlreadyLinked:           {connect.CodeInvalidArgument, "The private draft already has a linked recipient. Publish the current draft if it has the intended recipient, or prepare a new draft for a different person."},
	LinkContentChange:            {connect.CodeInvalidArgument, "Linking may change only the draft's destination. Copy the verified base content exactly and replace the destination with the selected linked member."},
	RequestIDConflict:            {connect.CodeFailedPrecondition, "This requestId is already bound to a different request. Use syntra retry with the original requestId to recover the original result, or use a fresh requestId for changed input."},
}

type Error struct {
	reason Reason
	title  string
}

func New(reason Reason) *Error { return &Error{reason: reason} }

func TitleMismatch(title string) *Error {
	return &Error{reason: TicketTitleMismatch, title: title}
}

func (e *Error) Error() string {
	message := "reason=" + string(e.reason) + "; " + reasons[e.reason].text
	if e.reason == TicketTitleMismatch {
		message += strconv.Quote(e.title)
	}
	return message
}

func (e *Error) Connect() error {
	return connect.NewError(reasons[e.reason].code, e)
}

// FromConnect accepts only the known code and canonical message for each reason.
// The sole dynamic field is an authorized ticket title, escaped onto one line.
func FromConnect(err error) *Error {
	var remote *connect.Error
	if !errors.As(err, &remote) {
		return nil
	}
	prefix, body, ok := strings.Cut(remote.Message(), "; ")
	reason := Reason(strings.TrimPrefix(prefix, "reason="))
	g, known := reasons[reason]
	if !ok || !known || remote.Code() != g.code || prefix != "reason="+string(reason) {
		return nil
	}
	if reason == TicketTitleMismatch {
		quoted, ok := strings.CutPrefix(body, g.text)
		if !ok || len(quoted) > 8194 {
			return nil
		}
		title, err := strconv.Unquote(quoted)
		if err != nil || strconv.Quote(title) != quoted {
			return nil
		}
		return TitleMismatch(title)
	}
	if body != g.text {
		return nil
	}
	return New(reason)
}
