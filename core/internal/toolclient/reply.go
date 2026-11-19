package toolclient

import (
	"context"
	"errors"
	"os"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

// CheckReplyState also serves the CLI template, which only reads a thread.
func CheckReplyState(t *v1.Thread) error {
	if t == nil {
		return ErrProfile
	}
	if t.State == v1.ThreadState_THREAD_STATE_PENDING {
		return actionable.New(actionable.ThreadNotPublished)
	}
	switch t.State {
	case v1.ThreadState_THREAD_STATE_CLOSED:
		return actionable.New(actionable.ThreadClosed)
	case v1.ThreadState_THREAD_STATE_OPEN:
		return nil
	default:
		return ErrProfile
	}
}

func (a *App) preflightReply(ctx context.Context, r *v1.PrepareContentRequest, thread *v1.Thread) error {
	// A retained request must still reach the server's immutable retry path.
	// Its expected revision can predate a successful write.
	if _, err := a.store.retained("attempt", r.RequestId); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := r.Content
	if thread == nil {
		var err error
		thread, err = a.readThread(ctx, content.WorkspaceId, content.GetExistingThreadId())
		if err != nil {
			return err
		}
	}
	if thread.Id != content.GetExistingThreadId() || thread.WorkspaceId != content.WorkspaceId {
		return ErrProfile
	}
	if err := CheckReplyState(thread); err != nil {
		return err
	}
	revision := r.GetExpectedThreadRevision()
	if r.Update != nil {
		revision = r.Update.ExpectedThreadRevision
	}
	switch {
	case content.Title != thread.Title:
		return actionable.TitleMismatch(thread.Title)
	case content.GetProjectId() != thread.ProjectId:
		return actionable.New(actionable.ProjectMismatch)
	case revision != thread.Revision:
		return actionable.New(actionable.StaleThreadRevision)
	default:
		return nil
	}
}
