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
	ThreadClosed                           Reason = "THREAD_CLOSED"
	ThreadNotPublished                     Reason = "THREAD_NOT_PUBLISHED"
	ThreadTitleMismatch                    Reason = "THREAD_TITLE_MISMATCH"
	ProjectMismatch                        Reason = "PROJECT_MISMATCH"
	StaleThreadRevision                    Reason = "STALE_THREAD_REVISION"
	StalePreparedMessageRevision           Reason = "STALE_PREPARED_MESSAGE_REVISION"
	NotAuthoredPrepared                    Reason = "NOT_AUTHORED_PREPARED"
	RecipientNotEligible                   Reason = "RECIPIENT_NOT_ELIGIBLE"
	InitialPreparedMessageAlreadyPublished Reason = "INITIAL_PREPARED_ALREADY_PUBLISHED"
	RecipientNotLinked                     Reason = "RECIPIENT_NOT_LINKED"
	PreparedRoutingChange                  Reason = "PREPARED_ROUTING_CHANGE"
	PreparedAlreadyLinked                  Reason = "PREPARED_ALREADY_LINKED"
	LinkContentChange                      Reason = "LINK_CONTENT_CHANGE"
	RequestIDConflict                      Reason = "REQUEST_ID_CONFLICT"
	RecipientLimitExceeded                 Reason = "RECIPIENT_LIMIT_EXCEEDED"
	NoOtherParticipants                    Reason = "NO_OTHER_PARTICIPANTS"
)

const (
	ForkParticipantLimit       Reason = "FORK_PARTICIPANT_LIMIT"
	ForkSourcePending          Reason = "FORK_SOURCE_PENDING"
	AnchorUnavailable          Reason = "ANCHOR_UNAVAILABLE"
	ForkExtraMemberIneligible  Reason = "FORK_EXTRA_MEMBER_INELIGIBLE"
	ForkSourceMemberInactive   Reason = "FORK_SOURCE_MEMBER_INACTIVE"
	ForkWorkspaceUnavailable   Reason = "FORK_WORKSPACE_UNAVAILABLE"
	ReplyParticipationRequired Reason = "REPLY_PARTICIPATION_REQUIRED"
)

type guidance struct {
	code connect.Code
	text string
}

var reasons = map[Reason]guidance{
	ForkParticipantLimit:                   {connect.CodeInvalidArgument, "The fork participant limit is 11 including the creator; select a smaller explicit list of source participants with --source-member or sourceMemberIds, or use --no-source-members or an empty sourceMemberIds array. Reduce extra members if needed."},
	ForkSourcePending:                      {connect.CodeFailedPrecondition, "Publish the source thread's initial message before forking from it."},
	AnchorUnavailable:                      {connect.CodeNotFound, "Choose a published message from the selected thread. Use syntra messages or the MCP messages tool to find its message ID."},
	ForkExtraMemberIneligible:              {connect.CodeFailedPrecondition, "Select only active extra members of the workspace. Refresh the member list before retrying."},
	ForkSourceMemberInactive:               {connect.CodeFailedPrecondition, "Select only active participants of the source thread with --source-member or sourceMemberIds. Use syntra thread or the MCP thread tool to check its participants."},
	ForkWorkspaceUnavailable:               {connect.CodeNotFound, "Choose a workspace where your connected account is an active member. Check syntra workspaces or the MCP workspaces tool."},
	ReplyParticipationRequired:             {connect.CodePermissionDenied, "To reply, your connected account must be a thread participant or workspace administrator. Ask a participant or administrator to add you to the thread."},
	ThreadClosed:                           {connect.CodeFailedPrecondition, "The thread is closed. Closed threads accept no agent publication until a person reopens the thread in the Syntra web app."},
	ThreadNotPublished:                     {connect.CodeFailedPrecondition, "The thread is still a private prepared. Publish its initial message before preparing or publishing a reply."},
	ThreadTitleMismatch:                    {connect.CodeFailedPrecondition, "A reply's title and project must match the thread exactly. Use syntra prepare --template reply or syntra reply. Current thread title: "},
	ProjectMismatch:                        {connect.CodeFailedPrecondition, "The reply project differs from the thread's project. Copy the exact projectId from syntra thread, or omit it if the thread has no project. Use syntra prepare --template reply to fill it automatically."},
	StaleThreadRevision:                    {connect.CodeAborted, "The thread revision is stale. Re-read it with syntra thread and retry with the new revision and a fresh requestId."},
	StalePreparedMessageRevision:           {connect.CodeAborted, "The prepared revision is stale. Get the current prepared revision with syntra messages --view own-prepared, read it with syntra snapshot, and retry with the current verified base and a fresh requestId."},
	NotAuthoredPrepared:                    {connect.CodeFailedPrecondition, "The private prepared does not use authored content v1. Use syntra prepare to create a new authored prepared."},
	RecipientNotEligible:                   {connect.CodeNotFound, "The recorded recipient is no longer eligible for this workspace. Check syntra members, then prepare a new prepared for an eligible recipient."},
	InitialPreparedMessageAlreadyPublished: {connect.CodeFailedPrecondition, "This initial prepared cannot be published on an already published thread. Use syntra thread to refresh the state, then prepare a reply with syntra prepare --template reply."},
	RecipientNotLinked:                     {connect.CodeInvalidArgument, "The prepared has no linked recipient. Resolve an active recipient with syntra members and link the prepared with syntra link before publishing."},
	PreparedRoutingChange:                  {connect.CodeInvalidArgument, "A prepared update may change only the summary and Markdown. Keep its thread, title, project and recipient unchanged, or prepare a new prepared."},
	PreparedAlreadyLinked:                  {connect.CodeInvalidArgument, "The private prepared already has a linked recipient. Publish the current prepared if it has the intended recipient, or prepare a new prepared for a different person."},
	LinkContentChange:                      {connect.CodeInvalidArgument, "Linking may change only the prepared's destination. Copy the verified base content exactly and replace the destination with the selected linked member."},
	RecipientLimitExceeded:                 {connect.CodeInvalidArgument, "The destination names more recipients than the configured per-message recipient limit allows. Address fewer people, or send separate messages. Nothing was written."},
	NoOtherParticipants:                    {connect.CodeFailedPrecondition, "This thread has no other active participant to reply to. A reply to all needs at least one other participant; start a new thread addressed to someone instead."},
	RequestIDConflict:                      {connect.CodeFailedPrecondition, "This requestId is already bound to a different request. Use syntra retry with the original requestId to recover the original result, or use a fresh requestId for changed input."},
}

type Error struct {
	reason    Reason
	title     string
	forkRetry bool
}

func New(reason Reason) *Error { return &Error{reason: reason} }

func TitleMismatch(title string) *Error {
	return &Error{reason: ThreadTitleMismatch, title: title}
}

// FromForkConnect accepts only fixed reasons that the fork RPC can return.
// Fork retries resend the full request instead of consulting a local receipt.
func FromForkConnect(err error) *Error {
	public := FromConnect(err)
	if public == nil {
		return nil
	}
	switch public.reason {
	case RequestIDConflict:
		return &Error{reason: RequestIDConflict, forkRetry: true}
	case ForkParticipantLimit, ForkSourcePending, AnchorUnavailable, ForkSourceMemberInactive, ForkWorkspaceUnavailable:
		return public
	default:
		return nil
	}
}

func (e *Error) Error() string {
	if e.forkRetry {
		return "reason=REQUEST_ID_CONFLICT; This requestId is already bound to a different fork. Resend the original fork arguments and requestId to recover its result, or use a fresh requestId for changed input."
	}
	message := "reason=" + string(e.reason) + "; " + reasons[e.reason].text
	if e.reason == ThreadTitleMismatch {
		message += strconv.Quote(e.title)
	}
	return message
}

func (e *Error) Connect() error {
	return connect.NewError(reasons[e.reason].code, e)
}

// FromConnect accepts only the known code and canonical message for each reason.
// The sole dynamic field is an authorized thread title, escaped onto one line.
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
	if reason == ThreadTitleMismatch {
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
