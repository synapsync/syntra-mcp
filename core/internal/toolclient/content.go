package toolclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	v1 "syntra.local/mcp/gen/syntra/v1"
	ci "syntra.local/mcp/internal/contentintegrity"
)

type attempt struct {
	Origin     string          `json:"origin"`
	Account    string          `json:"account"`
	Connection string          `json:"connection"`
	Method     string          `json:"method"`
	RequestID  string          `json:"requestId"`
	Request    json.RawMessage `json:"request"`
	Expected   json.RawMessage `json:"expected"`
	BaseID     string          `json:"baseId"`
}

var attemptKeys = []string{"origin", "account", "connection", "method", "requestId", "request", "expected", "baseId"}

type VerifiedSnapshot struct {
	Verification string                      `json:"verification"`
	Snapshot     *v1.AuthoredContentSnapshot `json:"-"`
	Legacy       *v1.LegacyContentSnapshot   `json:"-"`
}

func (a *App) loadAttempt(id string) (*attempt, proto.Message, *v1.AuthoredContent, error) {
	raw, e := a.store.retained("attempt", id)
	if e != nil {
		return nil, nil, nil, e
	}
	var t attempt
	if strictRecord(raw, attemptKeys, &t) != nil || t.RequestID != id || t.Origin != a.client.origin || t.Account != a.profile.AccountID || t.Connection != a.profile.ConnectionID {
		return nil, nil, nil, ErrProfile
	}
	var req proto.Message
	switch t.Method {
	case "prepare":
		req = &v1.PrepareContentRequest{}
	case "link":
		req = &v1.LinkContentRequest{}
	case "publish":
		req = &v1.PublishContentRequest{}
	default:
		return nil, nil, nil, ErrProfile
	}
	if ci.DecodeStrictJSON(t.Request, req) != nil {
		return nil, nil, nil, ErrProfile
	}
	// Expected is gated through the same frozen singular-message raw decoder.
	wrapper := append([]byte(`{"content":`), t.Expected...)
	wrapper = append(wrapper, '}')
	var p v1.PrepareContentRequest
	if ci.DecodeStrictJSON(wrapper, &p) != nil {
		return nil, nil, nil, ErrProfile
	}
	return &t, req, p.Content, nil
}
func (a *App) retainedResult(id string) (*v1.AuthoredContentSnapshot, error) {
	t, req, c, e := a.loadAttempt(id)
	if e != nil {
		return nil, e
	}
	raw, e := a.store.retained("result", id)
	if e != nil {
		return nil, e
	}
	s := &v1.AuthoredContentSnapshot{}
	if protojson.Unmarshal(raw, s) != nil {
		return nil, ci.ErrIntegrity
	}
	if e = a.checkResult(t, req, c, s); e != nil {
		return nil, e
	}
	return s, nil
}
func (a *App) base(id string) (*v1.AuthoredContentSnapshot, error) {
	if !validUUID(id) {
		return nil, errors.New("a retained verified base request ID is required")
	}
	return a.retainedResult(id)
}

// Prepare permits new private content; replacement requires an exact retained
// base. No name lookup, invitations, publication or lifecycle action is implicit.
func (a *App) Prepare(ctx context.Context, in *v1.PrepareContentRequest, baseID string) (*v1.AuthoredContentSnapshot, error) {
	if in == nil || !validUUID(in.RequestId) {
		return nil, errors.New("explicit canonical requestId required before mutation")
	}
	r := proto.Clone(in).(*v1.PrepareContentRequest)
	if e := fillDigest(r.Content, &r.ExpectedContentDigest); e != nil {
		return nil, e
	}
	if e := ci.ValidatePrepare(r, a.profile.AccountID); e != nil {
		return nil, e
	}
	if r.Update != nil {
		s, e := a.base(baseID)
		if e != nil {
			return nil, e
		}
		x, e := expectation(s)
		if e != nil || !proto.Equal(x, r.Update) || ci.ValidatePrepareUpdate(r, s.Content, a.profile.AccountID) != nil {
			return nil, ci.ErrIntegrity
		}
	} else if baseID != "" {
		return nil, ci.ErrWire
	}
	return a.executeNew(ctx, "prepare", r, r.Content, r.RequestId, baseID)
}
func (a *App) Link(ctx context.Context, in *v1.LinkContentRequest, baseID string) (*v1.AuthoredContentSnapshot, error) {
	if in == nil || !validUUID(in.RequestId) {
		return nil, errors.New("explicit canonical requestId required before mutation")
	}
	r := proto.Clone(in).(*v1.LinkContentRequest)
	s, e := a.base(baseID)
	if e != nil {
		return nil, e
	}
	if e = fillDigest(r.LinkedContent, &r.ExpectedContentDigest); e != nil {
		return nil, e
	}
	x, e := expectation(s)
	if e != nil || !proto.Equal(x, r.Current) || ci.ValidateLink(r, s.Content, a.profile.AccountID) != nil {
		return nil, ci.ErrIntegrity
	}
	return a.executeNew(ctx, "link", r, r.LinkedContent, r.RequestId, baseID)
}
func (a *App) Publish(ctx context.Context, in *v1.PublishContentRequest, baseID string) (*v1.AuthoredContentSnapshot, error) {
	if in == nil || !validUUID(in.RequestId) {
		return nil, errors.New("explicit canonical requestId required before mutation")
	}
	r := proto.Clone(in).(*v1.PublishContentRequest)
	s, e := a.base(baseID)
	if e != nil {
		return nil, e
	}
	x, e := expectation(s)
	if e != nil || !proto.Equal(x, r.Current) || ci.ValidatePublish(r, s.Content, a.profile.AccountID) != nil {
		return nil, ci.ErrIntegrity
	}
	return a.executeNew(ctx, "publish", r, s.Content, r.RequestId, baseID)
}
func (a *App) executeNew(ctx context.Context, method string, req proto.Message, c *v1.AuthoredContent, id, baseID string) (*v1.AuthoredContentSnapshot, error) {
	if e := a.authorize(ctx, methodCap(method)); e != nil {
		return nil, e
	}
	q, e := jsonProto(req)
	if e != nil {
		return nil, e
	}
	expected, e := jsonProto(c)
	if e != nil {
		return nil, e
	}
	t := attempt{a.client.origin, a.profile.AccountID, a.profile.ConnectionID, method, id, q, expected, baseID}
	raw, e := json.Marshal(t)
	if e != nil {
		return nil, e
	}
	if e = a.store.retain("attempt", id, raw); e != nil {
		return nil, e
	}
	result, err := a.Retry(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("retained request %s: %w", id, err)
	}
	return result, nil
}
func methodCap(method string) v1.ToolCapability {
	if method == "publish" {
		return v1.ToolCapability_TOOL_CAPABILITY_PUBLISH
	}
	return v1.ToolCapability_TOOL_CAPABILITY_PREPARE
}

// Retry always sends the original request, even after an observed result, to
// recheck current authorization/closed state before disclosing retained content.
func (a *App) Retry(ctx context.Context, id string) (*v1.AuthoredContentSnapshot, error) {
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	t, req, c, e := a.loadAttempt(id)
	if e != nil {
		return nil, e
	}
	if e = a.authorize(ctx, methodCap(t.Method)); e != nil {
		return nil, e
	}
	// Re-validate retained base and transformations before any mutation.
	if t.BaseID != "" {
		prior, e := a.base(t.BaseID)
		if e != nil {
			return nil, e
		}
		x, e := expectation(prior)
		if e != nil {
			return nil, e
		}
		switch r := req.(type) {
		case *v1.PrepareContentRequest:
			if !proto.Equal(x, r.Update) || ci.ValidatePrepareUpdate(r, prior.Content, a.profile.AccountID) != nil {
				return nil, ci.ErrIntegrity
			}
		case *v1.LinkContentRequest:
			if !proto.Equal(x, r.Current) || ci.ValidateLink(r, prior.Content, a.profile.AccountID) != nil {
				return nil, ci.ErrIntegrity
			}
		case *v1.PublishContentRequest:
			if !proto.Equal(x, r.Current) || !proto.Equal(c, prior.Content) || ci.ValidatePublish(r, c, a.profile.AccountID) != nil {
				return nil, ci.ErrIntegrity
			}
		}
	} else if t.Method != "prepare" || req.(*v1.PrepareContentRequest).Update != nil {
		return nil, ci.ErrIntegrity
	}
	if r, ok := req.(*v1.PrepareContentRequest); ok && (ci.ValidatePrepare(r, a.profile.AccountID) != nil || !proto.Equal(r.Content, c)) {
		return nil, ci.ErrIntegrity
	}
	var old *v1.AuthoredContentSnapshot
	raw, e := a.store.retained("result", id)
	if e == nil {
		old = &v1.AuthoredContentSnapshot{}
		if protojson.Unmarshal(raw, old) != nil || a.checkResult(t, req, c, old) != nil {
			return nil, ci.ErrIntegrity
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	var s *v1.AuthoredContentSnapshot
	switch r := req.(type) {
	case *v1.PrepareContentRequest:
		res, e := a.content().PrepareContent(ctx, bearer(connect.NewRequest(r), a.profile.Secret))
		if e != nil {
			return nil, SafeRPCError(e)
		}
		s = res.Msg.Snapshot
	case *v1.LinkContentRequest:
		res, e := a.content().LinkContent(ctx, bearer(connect.NewRequest(r), a.profile.Secret))
		if e != nil {
			return nil, SafeRPCError(e)
		}
		s = res.Msg.Snapshot
	case *v1.PublishContentRequest:
		res, e := a.content().PublishContent(ctx, bearer(connect.NewRequest(r), a.profile.Secret))
		if e != nil {
			return nil, SafeRPCError(e)
		}
		s = res.Msg.Snapshot
	}
	if a.checkResult(t, req, c, s) != nil || old != nil && !proto.Equal(old, s) {
		return nil, ci.ErrIntegrity
	}
	raw, e = jsonProto(s)
	if e != nil {
		return nil, e
	}
	if e = a.store.retain("result", id, raw); e != nil {
		return nil, e
	}
	return proto.Clone(s).(*v1.AuthoredContentSnapshot), nil
}
func (a *App) Snapshot(ctx context.Context, r *v1.GetContentSnapshotRequest, anchorID string) (*VerifiedSnapshot, error) {
	if ci.ValidateSnapshotRequest(r) != nil {
		return nil, ci.ErrWire
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	var local *v1.AuthoredContentSnapshot
	var e error
	if anchorID != "" {
		local, e = a.base(anchorID)
		if e != nil {
			return nil, e
		}
	}
	res, e := a.content().GetContentSnapshot(ctx, bearer(connect.NewRequest(r), a.profile.Secret))
	if e != nil {
		return nil, SafeRPCError(e)
	}
	if l := res.Msg.GetLegacy(); l != nil {
		if local != nil || l.Ticket == nil || l.Ticket.Id != r.TicketId || l.Ticket.WorkspaceId != r.WorkspaceId {
			return nil, ci.ErrIntegrity
		}
		if d := r.GetPrivateDraft(); d != nil {
			if l.GetPrivateDraft() == nil || l.GetPrivateDraft().Id != d.DraftId || l.GetPrivateDraft().Revision != d.Revision || l.GetPrivateDraft().WorkspaceId != r.WorkspaceId || l.GetPrivateDraft().TicketId != r.TicketId || l.GetPrivateDraft().AuthorUserId != a.profile.AccountID {
				return nil, ci.ErrIntegrity
			}
		} else if l.GetPublishedMessage() == nil || l.GetPublishedMessage().Id != r.GetPublishedMessageId() || l.GetPublishedMessage().WorkspaceId != r.WorkspaceId || l.GetPublishedMessage().TicketId != r.TicketId {
			return nil, ci.ErrIntegrity
		}
		return &VerifiedSnapshot{Verification: "legacy-no-client-integrity", Legacy: l}, nil
	}
	s := res.Msg.GetAuthored()
	if snapshotShape(s) != nil || s.Content.WorkspaceId != r.WorkspaceId || s.Receipt.TicketId != r.TicketId {
		return nil, ci.ErrIntegrity
	}
	if d := r.GetPrivateDraft(); d != nil {
		if !proto.Equal(d, s.GetPrivateDraft()) || s.Content.ExpectedAuthorUserId != a.profile.AccountID {
			return nil, ci.ErrIntegrity
		}
	} else if s.GetPublishedMessageId() != r.GetPublishedMessageId() {
		return nil, ci.ErrIntegrity
	}
	mode := "receipt-consistency-only"
	if local != nil {
		if !proto.Equal(local, s) {
			return nil, ci.ErrIntegrity
		}
		mode = "retained-client-expectation"
	}
	return &VerifiedSnapshot{Verification: mode, Snapshot: s}, nil
}
