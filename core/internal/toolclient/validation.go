package toolclient

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	v1 "syntra.local/mcp/gen/syntra/v1"
	ci "syntra.local/mcp/internal/contentintegrity"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

func clientField(name, message string) error {
	return fmt.Errorf("%w: %s %s", ci.ErrWire, name, message)
}
func clientID(name, value string) error {
	if value == "" {
		return clientField(name, "is required (canonical lowercase UUID)")
	}
	if !validUUID(value) {
		return clientField(name, "must be a canonical lowercase UUID")
	}
	return nil
}
func clientRevision(name, value string) error {
	b, e := base64.RawURLEncoding.DecodeString(value)
	if e != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != value {
		return clientField(name, "must be the opaque revision; copy `revision` exactly from `syntra thread` (for prepared revision use the retained private prepared)")
	}
	return nil
}
func clientExpectation(name string, e *v1.PreparedContentExpectation) error {
	if e == nil {
		return clientField(name, "is required (copy the retained snapshot expectation)")
	}
	for _, f := range []struct{ name, value string }{{"threadId", e.ThreadId}, {"preparedMessageId", e.PreparedMessageId}} {
		if err := clientID(name+"."+f.name, f.value); err != nil {
			return err
		}
	}
	for _, f := range []struct{ name, value string }{{"expectedThreadRevision", e.ExpectedThreadRevision}, {"expectedPreparedMessageRevision", e.ExpectedPreparedMessageRevision}} {
		if err := clientRevision(name+"."+f.name, f.value); err != nil {
			return err
		}
	}
	if len(e.ExpectedContentDigest) != 32 {
		return clientField(name+".expectedContentDigest", "must be the retained 32-byte content digest encoded as base64")
	}
	return nil
}

// ValidateReplyAnchor checks the anchor before opening a profile or sending a request.
func ValidateReplyAnchor(field, thread, message string) error {
	if thread == "" {
		return clientField(field, "requires an existing thread")
	}
	return clientID(field, message)
}

func clientContent(name string, c *v1.AuthoredContent, owner string) error {
	if c == nil {
		return clientField(name, "is required")
	}
	if c.Summary != "" {
		return actionable.New(actionable.MessageHasNoTitle)
	}
	if c.FormatVersion != ci.CurrentFormat {
		return actionable.New(actionable.LegacyContentFormat)
	}
	if c.ReplyToMessageId != nil {
		if err := ValidateReplyAnchor(name+".replyToMessageId", c.GetExistingThreadId(), *c.ReplyToMessageId); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.ContextMarkdown) == "" {
		return actionable.New(actionable.MessageBodyRequired)
	}
	for _, f := range []struct{ name, value string }{{"expectedAuthorUserId", c.ExpectedAuthorUserId}, {"workspaceId", c.WorkspaceId}} {
		if err := clientID(name+"."+f.name, f.value); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		name  string
		value *string
	}{{"projectId", c.ProjectId}, {"existingThreadId", c.ExistingThreadId}} {
		if f.value != nil {
			if err := clientID(name+"."+f.name, *f.value); err != nil {
				return err
			}
		}
	}
	if owner != "" && c.ExpectedAuthorUserId != owner {
		return fmt.Errorf("%w: %s.expectedAuthorUserId must equal the account shown by syntra identity", ci.ErrIntegrity, name)
	}
	for _, f := range []struct{ name, value string }{{"title", c.Title}, {"originalDestinationIntent", c.OriginalDestinationIntent}} {
		if f.value == "" {
			return clientField(name+"."+f.name, "is required and must be a non-empty string")
		}
	}
	for _, f := range []struct {
		name, value string
		max         int
	}{{"title", c.Title, 1024}, {"originalDestinationIntent", c.OriginalDestinationIntent, 1024}, {"contextMarkdown", c.ContextMarkdown, 131072}} {
		if len(f.value) > f.max || !utf8.ValidString(f.value) || strings.ContainsRune(f.value, 0) {
			return clientField(name+"."+f.name, fmt.Sprintf("must be valid UTF-8 without NUL and at most %d bytes", f.max))
		}
	}
	if c.Destination == nil || c.Destination.Target == nil {
		return clientField(name+".destination", "is required (exactly one of linkedMemberId, linkedMemberSet, teamId, replyAll or unlinkedLabel)")
	}
	switch x := c.Destination.Target.(type) {
	case *v1.ContentDestination_LinkedMemberId:
		if err := clientID(name+".destination.linkedMemberId", x.LinkedMemberId); err != nil {
			return err
		}
	case *v1.ContentDestination_LinkedMemberSet:
		ids := x.LinkedMemberSet.GetMemberIds()
		if len(ids) < 2 || len(ids) > ci.MaxRecipients {
			return clientField(name+".destination.linkedMemberSet.memberIds", fmt.Sprintf("must list 2 to %d distinct member UUIDs (use linkedMemberId for one person)", ci.MaxRecipients))
		}
		seen := map[string]bool{}
		for i, id := range ids {
			if err := clientID(fmt.Sprintf("%s.destination.linkedMemberSet.memberIds[%d]", name, i), id); err != nil {
				return err
			}
			if seen[id] {
				return clientField(name+".destination.linkedMemberSet.memberIds", "must not repeat a member")
			}
			seen[id] = true
		}
	case *v1.ContentDestination_ReplyAll:
		if !x.ReplyAll || c.ExistingThreadId == nil {
			return clientField(name+".destination.replyAll", "must be true and is only valid for a reply (existingThreadId present)")
		}
	case *v1.ContentDestination_TeamId:
		if err := clientID(name+".destination.teamId", x.TeamId); err != nil {
			return err
		}
	case *v1.ContentDestination_UnlinkedLabel:
		if len(x.UnlinkedLabel) > 1024 || !utf8.ValidString(x.UnlinkedLabel) || strings.ContainsRune(x.UnlinkedLabel, 0) {
			return clientField(name+".destination.unlinkedLabel", "must be valid UTF-8 without NUL and at most 1024 bytes")
		}
		if x.UnlinkedLabel == "" {
			return clientField(name+".destination.unlinkedLabel", "must be non-empty")
		}
	}
	return checkPlaceholders(c)
}

// ValidateLocalContentRequest checks all identity-independent properties before
// loading a local profile. Authentication and retained-base checks remain in App.
func ValidateLocalContentRequest(target proto.Message, baseID string) error {
	switch q := target.(type) {
	case *v1.DiscardContentRequest:
		if q == nil || baseID != "" {
			return clientField("request", "is required and takes no --base")
		}
		for name, value := range map[string]string{"workspaceId": q.WorkspaceId, "threadId": q.ThreadId, "preparedMessageId": q.PreparedMessageId, "requestId": q.RequestId} {
			if err := clientID(name, value); err != nil {
				return err
			}
		}
		if q.ExpectedThreadRevision == "" || q.ExpectedPreparedMessageRevision == "" {
			return clientField("expectedThreadRevision", "and expectedPreparedMessageRevision are required")
		}
		return nil
	case *v1.PrepareContentRequest:
		if q == nil {
			return clientField("request", "is required")
		}
		if err := checkPreparePlaceholders(q); err != nil {
			return err
		}
		if err := clientID("requestId", q.RequestId); err != nil {
			return err
		}
		if err := clientContent("content", q.Content, ""); err != nil {
			return err
		}
		if q.Update == nil && baseID != "" {
			return clientField("base", "is allowed only for an existing prepared update")
		}
		if q.Update != nil {
			if err := clientExpectation("update", q.Update); err != nil {
				return err
			}
			if baseID != "" {
				if err := clientID("base", baseID); err != nil {
					return err
				}
			}
		} else if q.Content.ExistingThreadId != nil {
			if err := clientRevision("expectedThreadRevision", q.GetExpectedThreadRevision()); err != nil {
				return err
			}
		}
		clone := proto.Clone(q).(*v1.PrepareContentRequest)
		if err := fillDigest(clone.Content, &clone.ExpectedContentDigest); err != nil {
			return err
		}
		return ci.ValidatePrepare(clone, clone.Content.ExpectedAuthorUserId)
	case *v1.LinkContentRequest:
		if q == nil {
			return clientField("request", "is required")
		}
		if err := clientID("requestId", q.RequestId); err != nil {
			return err
		}
		if err := clientExpectation("current", q.Current); err != nil {
			return err
		}
		if err := clientContent("linkedContent", q.LinkedContent, ""); err != nil {
			return err
		}
		if err := clientID("base", baseID); err != nil {
			return err
		}
		clone := proto.Clone(q).(*v1.LinkContentRequest)
		return fillDigest(clone.LinkedContent, &clone.ExpectedContentDigest)
	case *v1.PublishContentRequest:
		if q == nil {
			return clientField("request", "is required")
		}
		for _, f := range []struct{ name, value string }{{"requestId", q.RequestId}, {"workspaceId", q.WorkspaceId}, {"expectedAuthorUserId", q.ExpectedAuthorUserId}, {"base", baseID}} {
			if err := clientID(f.name, f.value); err != nil {
				return err
			}
		}
		return clientExpectation("current", q.Current)
	case *v1.GetContentSnapshotRequest:
		return ci.ValidateSnapshotRequest(q)
	default:
		return ci.ErrWire
	}
}

// ValidateReplyRouting checks IDs before a CLI command reads the message file.
func ValidateReplyRouting(workspace, thread string) error {
	if err := clientID("workspace", workspace); err != nil {
		return err
	}
	return clientID("thread", thread)
}

// ValidateReplyInput checks locally supplied arguments without touching a profile.
func ValidateReplyInput(workspace, thread, body string) error {
	if err := ValidateReplyRouting(workspace, thread); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return actionable.New(actionable.MessageBodyRequired)
	}
	if len(body) > 131072 || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return clientField("contextMarkdown", "must be valid UTF-8 without NUL and at most 131072 bytes")
	}
	if strings.Contains(body, "TODO: SYNTRA") {
		return clientField("contextMarkdown", "contains a TODO: SYNTRA placeholder; replace it before preparing")
	}
	return nil
}

// ValidateTemplateInput checks the selected template and routing IDs locally.
func ValidateTemplateInput(kind, workspace, project, thread string) error {
	if kind != "new" && kind != "reply" {
		return clientField("template", "must be new or reply")
	}
	if err := clientID("workspace", workspace); err != nil {
		return err
	}
	if project != "" {
		if err := clientID("project", project); err != nil {
			return err
		}
	}
	if kind == "reply" {
		if err := clientID("thread", thread); err != nil {
			return err
		}
	}
	return nil
}
