package contentintegrity

import (
	"bytes"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

var ErrWire = errors.New("invalid authored content request")
var ErrIntegrity = errors.New("authored content integrity mismatch")
var canonicalID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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
		if f.IsList() || f.IsMap() {
			ok = false
			return false
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
	c := Content{FormatVersion: m.FormatVersion, ExpectedAuthorUserID: m.ExpectedAuthorUserId, WorkspaceID: m.WorkspaceId, ProjectID: m.ProjectId, ExistingTicketID: m.ExistingTicketId, Title: m.Title, OriginalDestinationIntent: m.OriginalDestinationIntent, Summary: m.Summary, ContextMarkdown: m.ContextMarkdown}
	if !validID(c.ExpectedAuthorUserID) || !validID(c.WorkspaceID) {
		return Content{}, ErrWire
	}
	for _, id := range []*string{c.ProjectID, c.ExistingTicketID} {
		if id != nil && !validID(*id) {
			return Content{}, ErrWire
		}
	}
	if m.Destination == nil {
		return Content{}, ErrWire
	}
	switch d := m.Destination.Target.(type) {
	case *v1.ContentDestination_LinkedMemberId:
		if d == nil || !validID(d.LinkedMemberId) {
			return Content{}, ErrWire
		}
		c.Destination = Destination{Kind: "linked", MemberID: d.LinkedMemberId}
	case *v1.ContentDestination_UnlinkedLabel:
		if d == nil {
			return Content{}, ErrWire
		}
		c.Destination = Destination{Kind: "unlinked", Label: d.UnlinkedLabel}
	default:
		return Content{}, ErrWire
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
func expectation(c *v1.ContentDraftExpectation) bool {
	return c != nil && noUnknown(c) && validID(c.TicketId) && validID(c.DraftId) && validRevision(c.ExpectedTicketRevision) && validRevision(c.ExpectedDraftRevision) && len(c.ExpectedContentDigest) == 32
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
		if !expectation(r.Update) || r.ExpectedTicketRevision != nil {
			return ErrWire
		}
		if r.Content.ExistingTicketId != nil && *r.Content.ExistingTicketId != r.Update.TicketId {
			return ErrWire
		}
	} else if r.Content.ExistingTicketId != nil {
		if r.ExpectedTicketRevision == nil || !validRevision(*r.ExpectedTicketRevision) {
			return ErrWire
		}
	} else if r.ExpectedTicketRevision != nil {
		return ErrWire
	}
	return nil
}

// ValidatePrepareUpdate verifies both digests against the retained private preimage.
// Actual revision comparison, ownership and initial ticket-ID binding stay in T2.3.
func ValidatePrepareUpdate(r *v1.PrepareContentRequest, prior *v1.AuthoredContent, owner string) error {
	if e := ValidatePrepare(r, owner); e != nil {
		return e
	}
	if r.Update == nil {
		return ErrWire
	}
	if e := verifyContent(prior, r.Update.ExpectedContentDigest, owner); e != nil {
		return e
	}
	return ValidateReplacement(prior, r.Content)
}

// ValidateReplacement restricts Prepare updates to exact summary/Markdown edits.
// Changing routing, subject, intent or recipient is not an incidental draft edit.
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
		return ErrWire
	}
	return nil
}

// ValidateLink requires the retained preimage; only destination may change.
// Membership, private ownership, ticket state and actual revisions are T2.3.
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
	if prior.ExistingTicketId != nil && *prior.ExistingTicketId != r.Current.TicketId {
		return ErrWire
	}
	if _, ok := prior.Destination.Target.(*v1.ContentDestination_UnlinkedLabel); !ok {
		return ErrWire
	}
	if _, ok := r.LinkedContent.Destination.Target.(*v1.ContentDestination_LinkedMemberId); !ok {
		return ErrWire
	}
	copy := proto.Clone(prior).(*v1.AuthoredContent)
	copy.Destination = proto.Clone(r.LinkedContent.Destination).(*v1.ContentDestination)
	if !proto.Equal(copy, r.LinkedContent) {
		return ErrWire
	}
	return nil
}
func ValidatePublish(r *v1.PublishContentRequest, prior *v1.AuthoredContent, owner string) error {
	if r == nil || !noUnknown(r) || !expectation(r.Current) || !validID(r.WorkspaceId) || !validID(r.ExpectedAuthorUserId) || !validID(r.RequestId) || !provenance(r.ClientReported) {
		return ErrWire
	}
	if r.ExpectedAuthorUserId != owner {
		return ErrIntegrity
	}
	if e := verifyContent(prior, r.Current.ExpectedContentDigest, owner); e != nil {
		return e
	}
	if prior.WorkspaceId != r.WorkspaceId || (prior.ExistingTicketId != nil && *prior.ExistingTicketId != r.Current.TicketId) {
		return ErrWire
	}
	if _, ok := prior.Destination.Target.(*v1.ContentDestination_LinkedMemberId); !ok {
		return ErrWire
	}
	return nil
}
func ValidateSnapshotRequest(r *v1.GetContentSnapshotRequest) error {
	if r == nil || !noUnknown(r) || !validID(r.WorkspaceId) || !validID(r.TicketId) {
		return ErrWire
	}
	switch s := r.Record.(type) {
	case *v1.GetContentSnapshotRequest_PrivateDraft:
		if s == nil || s.PrivateDraft == nil || !validID(s.PrivateDraft.DraftId) || !validRevision(s.PrivateDraft.Revision) {
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
