package toolclient

import (
	"strings"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"testing"
)

func TestClientDiagnosticClasses(t *testing.T) {
	const base = `{"requestId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","content":{"formatVersion":1,"expectedAuthorUserId":"11111111-1111-4111-8111-111111111111","workspaceId":"44444444-4444-4444-8444-444444444444","title":"secret-message-content","originalDestinationIntent":"Alex","summary":"ok","contextMarkdown":"","destination":{"unlinkedLabel":"Alex"}}}`
	cases := []struct{ name, raw, want string }{
		{"unknown", strings.Replace(base, `"summary":"ok"`, `"unknownKey":"ok"`, 1), "unknownKey"},
		{"misplaced", strings.Replace(base, `"summary":"ok"`, `"draftId":"ok"`, 1), "it belongs at request.update.draftId"},
		{"duplicate", strings.Replace(base, `"summary":"ok"`, `"summary":"ok","summary":"again"`, 1), "duplicate key summary"},
		{"type", strings.Replace(base, `"summary":"ok"`, `"summary":12`, 1), "wrong JSON type"},
		{"null", strings.Replace(base, `"summary":"ok"`, `"summary":null`, 1), "cannot be null"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var q v1.PrepareContentRequest
			err := ReadContentInput(strings.NewReader(c.raw), &q)
			if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "secret-message-content") {
				t.Fatalf("unexpected diagnostic: %v", err)
			}
		})
	}
}
func TestSemanticDiagnostics(t *testing.T) {
	const owner = "11111111-1111-4111-8111-111111111111"
	const ws = "44444444-4444-4444-8444-444444444444"
	makeContent := func() *v1.AuthoredContent {
		return &v1.AuthoredContent{FormatVersion: 1, ExpectedAuthorUserId: owner, WorkspaceId: ws, Title: "title", OriginalDestinationIntent: "Alex", Summary: "summary", Destination: &v1.ContentDestination{Target: &v1.ContentDestination_UnlinkedLabel{UnlinkedLabel: "Alex"}}}
	}
	for _, name := range []string{"formatVersion", "expectedAuthorUserId", "workspaceId", "title", "originalDestinationIntent", "summary", "destination"} {
		t.Run(name, func(t *testing.T) {
			c := makeContent()
			switch name {
			case "formatVersion":
				c.FormatVersion = 0
			case "expectedAuthorUserId":
				c.ExpectedAuthorUserId = ""
			case "workspaceId":
				c.WorkspaceId = ""
			case "title":
				c.Title = ""
			case "originalDestinationIntent":
				c.OriginalDestinationIntent = ""
			case "summary":
				c.Summary = ""
			case "destination":
				c.Destination = nil
			}
			if e := clientContent("content", c, owner); e == nil || !strings.Contains(e.Error(), name) {
				t.Fatalf("%v", e)
			}
		})
	}
	c := makeContent()
	c.ExpectedAuthorUserId = ws
	if e := clientContent("content", c, owner); e == nil || !strings.Contains(e.Error(), "syntra identity") {
		t.Fatal(e)
	}
	if e := clientID("workspaceId", "bad"); e == nil || !strings.Contains(e.Error(), "UUID") {
		t.Fatal(e)
	}
	if e := clientRevision("expectedTicketRevision", "bad"); e == nil || !strings.Contains(e.Error(), "syntra ticket") {
		t.Fatal(e)
	}
	c = makeContent()
	c.OriginalDestinationIntent = "TODO: SYNTRA name"
	if e := clientContent("content", c, owner); e == nil || !strings.Contains(e.Error(), "originalDestinationIntent") {
		t.Fatal(e)
	}
}

func TestExpectationRequiredFields(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const revision = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	cases := []struct {
		name   string
		mutate func(*v1.ContentDraftExpectation)
	}{{"ticketId", func(x *v1.ContentDraftExpectation) { x.TicketId = "" }}, {"draftId", func(x *v1.ContentDraftExpectation) { x.DraftId = "" }}, {"expectedTicketRevision", func(x *v1.ContentDraftExpectation) { x.ExpectedTicketRevision = "" }}, {"expectedDraftRevision", func(x *v1.ContentDraftExpectation) { x.ExpectedDraftRevision = "" }}, {"expectedContentDigest", func(x *v1.ContentDraftExpectation) { x.ExpectedContentDigest = nil }}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := &v1.ContentDraftExpectation{TicketId: id, DraftId: id, ExpectedTicketRevision: revision, ExpectedDraftRevision: revision, ExpectedContentDigest: make([]byte, 32)}
			c.mutate(x)
			err := clientExpectation("current", x)
			if err == nil || !strings.Contains(err.Error(), c.name) {
				t.Fatal(err)
			}
		})
	}
	if err := clientExpectation("current", nil); err == nil || !strings.Contains(err.Error(), "current") {
		t.Fatal(err)
	}
}
