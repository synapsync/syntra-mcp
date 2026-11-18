package toolclient

import (
	"bytes"
	"encoding/base64"

	"google.golang.org/protobuf/proto"

	v1 "syntra.local/mcp/gen/syntra/v1"
	ci "syntra.local/mcp/internal/contentintegrity"
)

func revision(s string) bool {
	b, e := base64.RawURLEncoding.DecodeString(s)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}
func reportedEqual(a, b *v1.ClientReportedProvenance) bool {
	if a != nil && a.Provider == nil && a.Model == nil && a.Tool == nil && a.Run == nil {
		a = nil
	}
	if b != nil && b.Provider == nil && b.Model == nil && b.Tool == nil && b.Run == nil {
		b = nil
	}
	return proto.Equal(a, b)
}
func snapshotShape(s *v1.AuthoredContentSnapshot) error {
	if s == nil || s.Receipt == nil || s.Content == nil || !revision(s.TicketRevision) {
		return ci.ErrIntegrity
	}
	r := s.Receipt
	if len(s.ProtoReflect().GetUnknown()) != 0 || len(r.ProtoReflect().GetUnknown()) != 0 || r.RecordedAt != nil && len(r.RecordedAt.ProtoReflect().GetUnknown()) != 0 {
		return ci.ErrIntegrity
	}
	h, e := digest(s.Content)
	if e != nil || !bytes.Equal(h, r.ContentDigest) || r.OwnerUserId != s.Content.ExpectedAuthorUserId || !validUUID(r.RequestId) || !validUUID(r.TicketId) || r.RecordedAt == nil || r.RecordedAt.CheckValid() != nil || r.IntegrityOrigin != v1.ContentIntegrityOrigin_CONTENT_INTEGRITY_ORIGIN_CLIENT_EXPECTED {
		return ci.ErrIntegrity
	}
	if r.ConnectionId != nil && !validUUID(*r.ConnectionId) {
		return ci.ErrIntegrity
	}
	if s.Content.ExistingTicketId != nil && *s.Content.ExistingTicketId != r.TicketId {
		return ci.ErrIntegrity
	}
	check := &v1.PrepareContentRequest{Content: s.Content, ExpectedContentDigest: r.ContentDigest, RequestId: r.RequestId, ClientReported: r.ClientReported}
	if s.Content.ExistingTicketId != nil {
		check.ExpectedTicketRevision = &s.TicketRevision
	}
	if ci.ValidatePrepare(check, r.OwnerUserId) != nil {
		return ci.ErrIntegrity
	}
	switch x := s.Record.(type) {
	case *v1.AuthoredContentSnapshot_PrivateDraft:
		if x == nil || x.PrivateDraft == nil || !validUUID(x.PrivateDraft.DraftId) || !revision(x.PrivateDraft.Revision) || r.MessageId != "" || len(x.PrivateDraft.ProtoReflect().GetUnknown()) != 0 {
			return ci.ErrIntegrity
		}
	case *v1.AuthoredContentSnapshot_PublishedMessageId:
		if x == nil || !validUUID(x.PublishedMessageId) || r.MessageId != x.PublishedMessageId || s.Content.Destination.GetLinkedMemberId() == "" {
			return ci.ErrIntegrity
		}
	default:
		return ci.ErrIntegrity
	}
	return nil
}
func expectation(s *v1.AuthoredContentSnapshot) (*v1.ContentDraftExpectation, error) {
	if snapshotShape(s) != nil || s.GetPrivateDraft() == nil {
		return nil, ci.ErrIntegrity
	}
	return &v1.ContentDraftExpectation{TicketId: s.Receipt.TicketId, DraftId: s.GetPrivateDraft().DraftId, ExpectedTicketRevision: s.TicketRevision, ExpectedDraftRevision: s.GetPrivateDraft().Revision, ExpectedContentDigest: append([]byte(nil), s.Receipt.ContentDigest...)}, nil
}
func (a *App) checkResult(t *attempt, req proto.Message, c *v1.AuthoredContent, s *v1.AuthoredContentSnapshot) error {
	if snapshotShape(s) != nil || !proto.Equal(c, s.Content) || s.Receipt.OwnerUserId != t.Account || s.Receipt.ConnectionId == nil || *s.Receipt.ConnectionId != t.Connection || s.Receipt.RequestId != t.RequestID {
		return ci.ErrIntegrity
	}
	var reported *v1.ClientReportedProvenance
	var current *v1.ContentDraftExpectation
	switch r := req.(type) {
	case *v1.PrepareContentRequest:
		if r.RequestId != t.RequestID || ci.ValidatePrepare(r, t.Account) != nil || !proto.Equal(r.Content, c) {
			return ci.ErrIntegrity
		}
		reported = r.ClientReported
		current = r.Update
		if r.Update != nil && s.TicketRevision != r.Update.ExpectedTicketRevision {
			return ci.ErrIntegrity
		}
		if r.Update == nil && r.ExpectedTicketRevision != nil && s.TicketRevision != *r.ExpectedTicketRevision {
			return ci.ErrIntegrity
		}
		if s.GetPrivateDraft() == nil {
			return ci.ErrIntegrity
		}
	case *v1.LinkContentRequest:
		if r.RequestId != t.RequestID || !proto.Equal(r.LinkedContent, c) {
			return ci.ErrIntegrity
		}
		reported = r.ClientReported
		current = r.Current
		if current == nil {
			return ci.ErrIntegrity
		}
		if c.ExistingTicketId == nil && s.TicketRevision == current.ExpectedTicketRevision || c.ExistingTicketId != nil && s.TicketRevision != current.ExpectedTicketRevision {
			return ci.ErrIntegrity
		}
		if s.GetPrivateDraft() == nil {
			return ci.ErrIntegrity
		}
	case *v1.PublishContentRequest:
		if r.RequestId != t.RequestID || ci.ValidatePublish(r, c, t.Account) != nil || s.GetPublishedMessageId() == "" {
			return ci.ErrIntegrity
		}
		reported = r.ClientReported
		current = r.Current
		if s.TicketRevision == current.ExpectedTicketRevision {
			return ci.ErrIntegrity
		}
	default:
		return ci.ErrIntegrity
	}
	if !reportedEqual(reported, s.Receipt.ClientReported) {
		return ci.ErrIntegrity
	}
	if current != nil {
		if s.Receipt.TicketId != current.TicketId {
			return ci.ErrIntegrity
		}
		if t.Method != "publish" && (s.GetPrivateDraft().DraftId != current.DraftId || s.GetPrivateDraft().Revision == current.ExpectedDraftRevision) {
			return ci.ErrIntegrity
		}
	}
	return nil
}
