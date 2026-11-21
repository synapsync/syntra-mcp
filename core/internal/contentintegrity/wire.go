package contentintegrity

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

var ErrWire = errors.New("invalid authored content request")
var ErrIntegrity = errors.New("authored content integrity mismatch")
var canonicalID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func invalidField(field string) error { return fmt.Errorf("%w: invalid or missing %s", ErrWire, field) }

func validID(s string) bool {
	return canonicalID.MatchString(s) && s != "00000000-0000-0000-0000-000000000000"
}
func validRevision(s string) bool {
	b, e := base64.RawURLEncoding.DecodeString(s)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}

// Scoped to the singular, non-map request graph frozen in content_service.proto.
// Reject future collection fields until the adapter deliberately supports them.
func noUnknown(m proto.Message) bool {
	if m == nil || !m.ProtoReflect().IsValid() {
		return false
	}
	r := m.ProtoReflect()
	if len(r.GetUnknown()) != 0 {
		return false
	}
	ok := true
	r.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		// Only repeated strings (a recipient set) are a deliberately supported collection.
		if f.IsMap() || (f.IsList() && f.Kind() != protoreflect.StringKind) {
			ok = false
			return false
		}
		if f.IsList() {
			return true
		}
		if f.Kind() == protoreflect.MessageKind {
			ok = noUnknown(v.Message().Interface())
		}
		return ok
	})
	return ok
}

// FromProto adapts typed oneof/presence without altering the frozen encoder.
// IDs gain domain syntax here; current membership/reference checks are separate.
func FromProto(m *v1.AuthoredContent) (Content, error) {
	if m == nil || !noUnknown(m) {
		return Content{}, ErrWire
	}
	c := Content{FormatVersion: m.FormatVersion, ExpectedAuthorUserID: m.ExpectedAuthorUserId, WorkspaceID: m.WorkspaceId, ProjectID: m.ProjectId, ExistingThreadID: m.ExistingThreadId, Title: m.Title, OriginalDestinationIntent: m.OriginalDestinationIntent, Summary: m.Summary, ContextMarkdown: m.ContextMarkdown}
	if c.FormatVersion != 1 {
		return Content{}, invalidField("content.formatVersion")
	}
	for _, f := range []struct {
		name, value string
		max         int
		empty       bool
	}{
		{"content.title", c.Title, 1024, false},
		{"content.originalDestinationIntent", c.OriginalDestinationIntent, 1024, false},
		{"content.summary", c.Summary, 8192, false},
		{"content.contextMarkdown", c.ContextMarkdown, 131072, true},
	} {
		if len(f.value) > f.max || (!f.empty && len(f.value) == 0) || !utf8.ValidString(f.value) || strings.ContainsRune(f.value, 0) {
			return Content{}, invalidField(f.name)
		}
	}
	if !validID(c.ExpectedAuthorUserID) {
		return Content{}, invalidField("content.expectedAuthorUserId")
	}
	if !validID(c.WorkspaceID) {
		return Content{}, invalidField("content.workspaceId")
	}
	for _, f := range []struct {
		name string
		id   *string
	}{{"content.projectId", c.ProjectID}, {"content.existingThreadId", c.ExistingThreadID}} {
		if f.id != nil && !validID(*f.id) {
			return Content{}, invalidField(f.name)
		}
	}
	if m.Destination == nil {
		return Content{}, invalidField("content.destination")
	}
	switch d := m.Destination.Target.(type) {
	case *v1.ContentDestination_LinkedMemberId:
		if d == nil || !validID(d.LinkedMemberId) {
			return Content{}, invalidField("content.destination.linkedMemberId")
		}
		c.Destination = Destination{Kind: "linked", MemberID: d.LinkedMemberId}
	case *v1.ContentDestination_UnlinkedLabel:
		if d == nil || len(d.UnlinkedLabel) == 0 || len(d.UnlinkedLabel) > 1024 || !utf8.ValidString(d.UnlinkedLabel) || strings.ContainsRune(d.UnlinkedLabel, 0) {
			return Content{}, invalidField("content.destination.unlinkedLabel")
		}
		c.Destination = Destination{Kind: "unlinked", Label: d.UnlinkedLabel}
	case *v1.ContentDestination_LinkedMemberSet:
		if d == nil || d.LinkedMemberSet == nil || len(d.LinkedMemberSet.MemberIds) < 2 || len(d.LinkedMemberSet.MemberIds) > MaxRecipients {
			return Content{}, invalidField("content.destination.linkedMemberSet")
		}
		seen := map[string]bool{}
		for _, id := range d.LinkedMemberSet.MemberIds {
			if !validID(id) || seen[id] {
				return Content{}, invalidField("content.destination.linkedMemberSet")
			}
			seen[id] = true
		}
		c.Destination = Destination{Kind: "linkedSet", MemberIDs: append([]string(nil), d.LinkedMemberSet.MemberIds...)}
	case *v1.ContentDestination_TeamId:
		if d == nil || !validID(d.TeamId) {
			return Content{}, invalidField("content.destination.teamId")
		}
		c.Destination = Destination{Kind: "team", TeamID: d.TeamId}
	case *v1.ContentDestination_ReplyAll:
		// Only true, and only as a reply: there must be an existing thread whose participants are addressed.
		if d == nil || !d.ReplyAll || c.ExistingThreadID == nil {
			return Content{}, invalidField("content.destination.replyAll")
		}
		c.Destination = Destination{Kind: "replyAll", ReplyAll: true}
	default:
		return Content{}, invalidField("content.destination")
	}
	for _, text := range []string{c.Title, c.OriginalDestinationIntent, c.Destination.Label, c.Summary, c.ContextMarkdown} {
		if strings.ContainsRune(text, 0) {
			return Content{}, ErrWire
		}
	}
	if _, e := Encode(c); e != nil {
		return Content{}, ErrWire
	}
	return c, nil
}
func verifyContent(m *v1.AuthoredContent, expected []byte, owner string) error {
	c, e := FromProto(m)
	if e != nil {
		return e
	}
	if c.ExpectedAuthorUserID != owner || len(expected) != 32 {
		return ErrIntegrity
	}
	d, e := Digest(c)
	if e != nil || !bytes.Equal(d[:], expected) {
		return ErrIntegrity
	}
	return nil
}
func provenance(p *v1.ClientReportedProvenance) bool {
	if p == nil {
		return true
	}
	if !noUnknown(p) {
		return false
	}
	for _, s := range []*string{p.Provider, p.Model, p.Tool, p.Run} {
		if s != nil && (len(*s) == 0 || len(*s) > 256 || !utf8.ValidString(*s) || strings.ContainsRune(*s, 0)) {
			return false
		}
	}
	return true
}
func expectation(c *v1.PreparedContentExpectation) bool {
	return c != nil && noUnknown(c) && validID(c.ThreadId) && validID(c.PreparedMessageId) && validRevision(c.ExpectedThreadRevision) && validRevision(c.ExpectedPreparedMessageRevision) && len(c.ExpectedContentDigest) == 32
}

// ValidatePrepare is pure input/integrity validation, not authorization or CAS.
// T2.3 must call it before mutation and compare persisted context/replacement.
func ValidatePrepare(r *v1.PrepareContentRequest, owner string) error {
	if r == nil || !noUnknown(r) || !validID(r.RequestId) || !provenance(r.ClientReported) {
		return ErrWire
	}
	if e := verifyContent(r.Content, r.ExpectedContentDigest, owner); e != nil {
		return e
	}
	if r.Update != nil {
		if !expectation(r.Update) || r.ExpectedThreadRevision != nil {
			return ErrWire
		}
		if r.Content.ExistingThreadId != nil && *r.Content.ExistingThreadId != r.Update.ThreadId {
			return ErrWire
		}
	} else if r.Content.ExistingThreadId != nil {
		if r.ExpectedThreadRevision == nil || !validRevision(*r.ExpectedThreadRevision) {
			return invalidField("expectedThreadRevision")
		}
	} else if r.ExpectedThreadRevision != nil {
		return ErrWire
	}
	return nil
}

// ValidatePrepareUpdate verifies both digests against the retained private preimage.
// Actual revision comparison, ownership and initial thread-ID binding stay in T2.3.
func ValidatePrepareUpdate(r *v1.PrepareContentRequest, prior *v1.AuthoredContent, owner string) error {
	// An initial draft (no existing thread) was never sent, so it may be re-titled and re-routed.
	return validatePrepareUpdate(r, prior, owner, prior != nil && prior.ExistingThreadId == nil)
}

func validatePrepareUpdate(r *v1.PrepareContentRequest, prior *v1.AuthoredContent, owner string, draft bool) error {
	if e := ValidatePrepare(r, owner); e != nil {
		return e
	}
	if r.Update == nil {
		return ErrWire
	}
	if e := verifyContent(prior, r.Update.ExpectedContentDigest, owner); e != nil {
		return e
	}
	if draft {
		return ValidateDraftReplacement(prior, r.Content)
	}
	return ValidateReplacement(prior, r.Content)
}

// ValidateDraftReplacement lets an unsent initial draft change everything an author chose
// (title, destination and its intent, summary, Markdown) but never its identity:
// format, author, workspace or the thread it replies to.
func ValidateDraftReplacement(prior, next *v1.AuthoredContent) error {
	if _, e := FromProto(prior); e != nil {
		return e
	}
	if _, e := FromProto(next); e != nil {
		return e
	}
	copy := proto.Clone(prior).(*v1.AuthoredContent)
	copy.Title = next.Title
	copy.OriginalDestinationIntent = next.OriginalDestinationIntent
	copy.Destination = next.Destination
	copy.Summary = next.Summary
	copy.ContextMarkdown = next.ContextMarkdown
	if !proto.Equal(copy, next) {
		return fmt.Errorf("%w: %w", ErrWire, actionable.New(actionable.PreparedRoutingChange))
	}
	return nil
}

// ValidateReplacement restricts Prepare updates to exact summary/Markdown edits.
// Changing routing, subject, intent or recipient is not an incidental prepared edit.
func ValidateReplacement(prior, next *v1.AuthoredContent) error {
	if _, e := FromProto(prior); e != nil {
		return e
	}
	if _, e := FromProto(next); e != nil {
		return e
	}
	copy := proto.Clone(prior).(*v1.AuthoredContent)
	copy.Summary = next.Summary
	copy.ContextMarkdown = next.ContextMarkdown
	if !proto.Equal(copy, next) {
		return fmt.Errorf("%w: %w", ErrWire, actionable.New(actionable.PreparedRoutingChange))
	}
	return nil
}

// ValidateLink requires the retained preimage; only destination may change.
// Membership, private ownership, thread state and actual revisions are T2.3.
func ValidateLink(r *v1.LinkContentRequest, prior *v1.AuthoredContent, owner string) error {
	if r == nil || !noUnknown(r) || !expectation(r.Current) || !validID(r.RequestId) || !provenance(r.ClientReported) {
		return ErrWire
	}
	if e := verifyContent(prior, r.Current.ExpectedContentDigest, owner); e != nil {
		return e
	}
	if e := verifyContent(r.LinkedContent, r.ExpectedContentDigest, owner); e != nil {
		return e
	}
	if prior.ExistingThreadId != nil && *prior.ExistingThreadId != r.Current.ThreadId {
		return ErrWire
	}
	if _, ok := prior.Destination.Target.(*v1.ContentDestination_UnlinkedLabel); !ok {
		return fmt.Errorf("%w: %w", ErrWire, actionable.New(actionable.PreparedAlreadyLinked))
	}
	if !LinkedDestination(r.LinkedContent.Destination) {
		return ErrWire
	}
	copy := proto.Clone(prior).(*v1.AuthoredContent)
	copy.Destination = proto.Clone(r.LinkedContent.Destination).(*v1.ContentDestination)
	if !proto.Equal(copy, r.LinkedContent) {
		return fmt.Errorf("%w: %w", ErrWire, actionable.New(actionable.LinkContentChange))
	}
	return nil
}
func ValidatePublishRequest(r *v1.PublishContentRequest, owner string) error {
	if r == nil || !noUnknown(r) || !expectation(r.Current) || !validID(r.WorkspaceId) || !validID(r.ExpectedAuthorUserId) || !validID(r.RequestId) || !provenance(r.ClientReported) {
		return ErrWire
	}
	if r.ExpectedAuthorUserId != owner {
		return ErrIntegrity
	}
	return nil
}

// ValidatePublishBinding verifies the exact preimage without checking delivery
// readiness. The service can report a missing link after locking the own prepared.
func ValidatePublishBinding(r *v1.PublishContentRequest, prior *v1.AuthoredContent, owner string) error {
	if e := ValidatePublishRequest(r, owner); e != nil {
		return e
	}
	if e := verifyContent(prior, r.Current.ExpectedContentDigest, owner); e != nil {
		return e
	}
	if prior.WorkspaceId != r.WorkspaceId || (prior.ExistingThreadId != nil && *prior.ExistingThreadId != r.Current.ThreadId) {
		return ErrWire
	}
	return nil
}

func ValidatePublish(r *v1.PublishContentRequest, prior *v1.AuthoredContent, owner string) error {
	if e := ValidatePublishBinding(r, prior, owner); e != nil {
		return e
	}
	if !LinkedDestination(prior.Destination) {
		return ErrWire
	}
	return nil
}

// LinkedDestination reports whether the destination names deliverable recipients (one person, several people
// or an explicit team), as opposed to an unlinked private label.
func LinkedDestination(d *v1.ContentDestination) bool {
	switch d.GetTarget().(type) {
	case *v1.ContentDestination_LinkedMemberId, *v1.ContentDestination_LinkedMemberSet, *v1.ContentDestination_TeamId, *v1.ContentDestination_ReplyAll:
		return true
	}
	return false
}
func ValidateSnapshotRequest(r *v1.GetContentSnapshotRequest) error {
	if r == nil || !noUnknown(r) || !validID(r.WorkspaceId) || !validID(r.ThreadId) {
		return ErrWire
	}
	switch s := r.Record.(type) {
	case *v1.GetContentSnapshotRequest_PreparedMessage:
		if s == nil || s.PreparedMessage == nil || !validID(s.PreparedMessage.PreparedMessageId) || !validRevision(s.PreparedMessage.Revision) {
			return ErrWire
		}
	case *v1.GetContentSnapshotRequest_PublishedMessageId:
		if s == nil || !validID(s.PublishedMessageId) {
			return ErrWire
		}
	default:
		return ErrWire
	}
	return nil
}
