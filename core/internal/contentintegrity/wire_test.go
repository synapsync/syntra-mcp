package contentintegrity

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

const owner = "11111111-1111-4111-8111-111111111111"
const workspace = "22222222-2222-4222-8222-222222222222"
const ticket = "33333333-3333-4333-8333-333333333333"
const draft = "44444444-4444-4444-8444-444444444444"
const request = "55555555-5555-4555-8555-555555555555"
const member = "66666666-6666-4666-8666-666666666666"

func revision() string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) }
func wireContent() *v1.AuthoredContent {
	return &v1.AuthoredContent{FormatVersion: 1, ExpectedAuthorUserId: owner, WorkspaceId: workspace, Title: "  Subject\r\n", OriginalDestinationIntent: "Pedró", Destination: &v1.ContentDestination{Target: &v1.ContentDestination_UnlinkedLabel{UnlinkedLabel: "Pedró"}}, Summary: " plain text\t", ContextMarkdown: ""}
}
func contentDigest(t *testing.T, c *v1.AuthoredContent) []byte {
	t.Helper()
	local, e := FromProto(c)
	if e != nil {
		t.Fatal(e)
	}
	d, e := Digest(local)
	if e != nil {
		t.Fatal(e)
	}
	return d[:]
}
func prepare(t *testing.T) *v1.PrepareContentRequest {
	c := wireContent()
	return &v1.PrepareContentRequest{Content: c, ExpectedContentDigest: contentDigest(t, c), RequestId: request}
}
func current(t *testing.T, c *v1.AuthoredContent) *v1.ContentDraftExpectation {
	return &v1.ContentDraftExpectation{TicketId: ticket, DraftId: draft, ExpectedTicketRevision: revision(), ExpectedDraftRevision: revision(), ExpectedContentDigest: contentDigest(t, c)}
}
func TestPrepareIntegrityAndModes(t *testing.T) {
	r := prepare(t)
	if e := ValidatePrepare(r, owner); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*v1.PrepareContentRequest){
		"missing digest": func(r *v1.PrepareContentRequest) { r.ExpectedContentDigest = nil }, "tampered bytes": func(r *v1.PrepareContentRequest) { r.Content.Summary += " " }, "missing author": func(r *v1.PrepareContentRequest) { r.Content.ExpectedAuthorUserId = "" }, "bad revision": func(r *v1.PrepareContentRequest) { r.ExpectedTicketRevision = proto.String("bad") }, "unknown": func(r *v1.PrepareContentRequest) { r.ProtoReflect().SetUnknown([]byte{0x98, 6, 1}) }, "empty provenance": func(r *v1.PrepareContentRequest) {
			r.ClientReported = &v1.ClientReportedProvenance{Model: proto.String("")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := proto.Clone(r).(*v1.PrepareContentRequest)
			change(c)
			if ValidatePrepare(c, owner) == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	if ValidatePrepare(r, member) == nil {
		t.Fatal("wrong owner accepted")
	}
	reply := prepare(t)
	reply.Content.ExistingTicketId = proto.String(ticket)
	reply.ExpectedContentDigest = contentDigest(t, reply.Content)
	if ValidatePrepare(reply, owner) == nil {
		t.Fatal("reply without CAS")
	}
	reply.ExpectedTicketRevision = proto.String(revision())
	if e := ValidatePrepare(reply, owner); e != nil {
		t.Fatal(e)
	}
	update := prepare(t)
	update.Update = current(t, update.Content)
	if e := ValidatePrepare(update, owner); e != nil {
		t.Fatal(e)
	}
	update.ExpectedTicketRevision = proto.String(revision())
	if ValidatePrepare(update, owner) == nil {
		t.Fatal("ambiguous update mode")
	}
}
func TestLinkOnlyChangesDestinationAndNeverPublish(t *testing.T) {
	prior := wireContent()
	linked := proto.Clone(prior).(*v1.AuthoredContent)
	linked.Destination.Target = &v1.ContentDestination_LinkedMemberId{LinkedMemberId: member}
	r := &v1.LinkContentRequest{Current: current(t, prior), LinkedContent: linked, ExpectedContentDigest: contentDigest(t, linked), RequestId: request}
	if e := ValidateLink(r, prior, owner); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"intent", "summary", "title", "workspace", "digest", "old digest"} {
		t.Run(field, func(t *testing.T) {
			bad := proto.Clone(r).(*v1.LinkContentRequest)
			switch field {
			case "intent":
				bad.LinkedContent.OriginalDestinationIntent = "Carlos"
			case "summary":
				bad.LinkedContent.Summary += "x"
			case "title":
				bad.LinkedContent.Title += "x"
			case "workspace":
				bad.LinkedContent.WorkspaceId = member
			case "digest":
				bad.ExpectedContentDigest[0] ^= 1
			case "old digest":
				bad.Current.ExpectedContentDigest[0] ^= 1
			}
			if field != "digest" && field != "old digest" {
				bad.ExpectedContentDigest = contentDigest(t, bad.LinkedContent)
			}
			if ValidateLink(bad, prior, owner) == nil {
				t.Fatal("invalid link accepted")
			}
		})
	}
	if ValidateReplacement(prior, linked) == nil {
		t.Fatal("Prepare silently linked")
	}
	edit := proto.Clone(prior).(*v1.AuthoredContent)
	edit.ContextMarkdown = "exact\r\n```\n\tcode\n```"
	if e := ValidateReplacement(prior, edit); e != nil {
		t.Fatal(e)
	}
	pub := &v1.PublishContentRequest{WorkspaceId: workspace, ExpectedAuthorUserId: owner, Current: current(t, prior), RequestId: request}
	if ValidatePublish(pub, prior, owner) == nil {
		t.Fatal("unlinked published")
	}
	pub.Current = current(t, linked)
	if e := ValidatePublish(pub, linked, owner); e != nil {
		t.Fatal(e)
	}
	pub.Current.ExpectedContentDigest[0] ^= 1
	if ValidatePublish(pub, linked, owner) == nil {
		t.Fatal("publish digest ignored")
	}
}
func TestDomainBytePolicy(t *testing.T) {
	c := wireContent()
	c.Summary = " \t\r\n"
	c.ContextMarkdown = "\x01\x7f"
	if _, e := FromProto(c); e != nil {
		t.Fatal("non-NUL controls/whitespace must remain exact", e)
	}
	c.Summary = "NUL\x00"
	if _, e := FromProto(c); e == nil {
		t.Fatal("PostgreSQL NUL accepted")
	}
	c = wireContent()
	c.Destination.ProtoReflect().SetUnknown([]byte{0x98, 6, 1})
	if _, e := FromProto(c); e == nil {
		t.Fatal("nested unknown accepted")
	}
}
func TestProvenanceBytePolicy(t *testing.T) {
	for _, field := range []string{"provider", "model", "tool", "run"} {
		for _, text := range []string{"\x00", "bad\x00suffix", "", strings.Repeat("é", 129), string([]byte{0xff})} {
			p := &v1.ClientReportedProvenance{}
			switch field {
			case "provider":
				p.Provider = &text
			case "model":
				p.Model = &text
			case "tool":
				p.Tool = &text
			case "run":
				p.Run = &text
			}
			r := prepare(t)
			r.ClientReported = p
			if ValidatePrepare(r, owner) == nil {
				t.Fatal("invalid reported context accepted", field)
			}
		}
	}
	accepted := " \t\r\n\x01é"
	r := prepare(t)
	r.ClientReported = &v1.ClientReportedProvenance{Model: &accepted}
	if e := ValidatePrepare(r, owner); e != nil {
		t.Fatal(e)
	}
}
func TestPrepareUpdateVerifiesPriorDigest(t *testing.T) {
	prior := wireContent()
	r := prepare(t)
	r.Update = current(t, prior)
	r.Content.Summary = "next"
	r.ExpectedContentDigest = contentDigest(t, r.Content)
	if e := ValidatePrepareUpdate(r, prior, owner); e != nil {
		t.Fatal(e)
	}
	r.Update.ExpectedContentDigest[0] ^= 1
	if ValidatePrepareUpdate(r, prior, owner) == nil {
		t.Fatal("prior digest bypassed")
	}
}

func TestStrictJSONPresenceGate(t *testing.T) {
	r := prepare(t)
	raw, e := protojson.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	target := &v1.PrepareContentRequest{}
	if e = DecodeStrictJSON(raw, target); e != nil {
		t.Fatal(e)
	}
	if !proto.Equal(r, target) {
		t.Fatal("JSON changed content")
	}
	// Decoder normally treats null as absent, so every variant is tested raw.
	for _, inactive := range []string{`""`, `null`, `"Carlos"`} {
		bad := strings.Replace(string(raw), `"unlinkedLabel":"Pedró"`, `"unlinkedLabel":"Pedró","linkedMemberId":`+inactive, 1)
		if bad == string(raw) {
			t.Fatal("fixture replacement missed")
		}
		if DecodeStrictJSON([]byte(bad), target) == nil {
			t.Fatalf("inactive variant accepted: %s", inactive)
		}
	}
	for _, bad := range []string{`{"content":null}`, `{"content":{},"content":{}}`, `{"expected_content_digest":"AA=="}`, `{"unexpected":1}`, `{"content":{"summary":"\ud800"}}`, `{"content":{"summary":"\udc00"}}`, `{"content":{"summary":"\ud800x"}}`, `{"content":{"projectId":null}}`, `{"content":{"destination":{"unlinkedLabel":"Pedro","unlinkedLabel":"Pedro"}}}`} {
		before := proto.Clone(target)
		if DecodeStrictJSON([]byte(bad), target) == nil {
			t.Fatalf("bad JSON accepted: %s", bad)
		}
		if !proto.Equal(before, target) {
			t.Fatal("failed decode mutated target")
		}
	}
	if DecodeStrictJSON([]byte("{\"content\":{\"summary\":\"\xff\"}}"), target) == nil {
		t.Fatal("invalid raw UTF8 accepted")
	}
	if e := DecodeStrictJSON([]byte(`{"content":{"summary":"\ud83d\ude80"}}`), target); e != nil {
		t.Fatal("valid surrogate pair rejected", e)
	}
}
func TestStrictJSONRequiresSemanticGate(t *testing.T) {
	r := &v1.PrepareContentRequest{}
	if e := DecodeStrictJSON([]byte(`{}`), r); e != nil {
		t.Fatal(e)
	}
	if ValidatePrepare(r, owner) == nil {
		t.Fatal("missing tool integrity accepted")
	}
}
func TestSnapshotSelector(t *testing.T) {
	r := &v1.GetContentSnapshotRequest{WorkspaceId: workspace, TicketId: ticket, Record: &v1.GetContentSnapshotRequest_PrivateDraft{PrivateDraft: &v1.PrivateContentRevision{DraftId: draft, Revision: revision()}}}
	if e := ValidateSnapshotRequest(r); e != nil {
		t.Fatal(e)
	}
	r.GetPrivateDraft().Revision = ""
	if ValidateSnapshotRequest(r) == nil {
		t.Fatal("missing exact private revision")
	}
	r.Record = &v1.GetContentSnapshotRequest_PublishedMessageId{PublishedMessageId: member}
	if e := ValidateSnapshotRequest(r); e != nil {
		t.Fatal(e)
	}
	if DecodeStrictJSON([]byte(`{"privateDraft":{},"publishedMessageId":null}`), r) == nil {
		t.Fatal("ambiguous selector accepted")
	}
}
