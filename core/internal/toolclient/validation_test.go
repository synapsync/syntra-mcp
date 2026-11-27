package toolclient

import (
	"strings"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"testing"
)

func TestClientDiagnosticClasses(t *testing.T) {
	const base = `{"requestId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","content":{"formatVersion":3,"expectedAuthorUserId":"11111111-1111-4111-8111-111111111111","workspaceId":"44444444-4444-4444-8444-444444444444","title":"secret-message-content","originalDestinationIntent":"Alex","contextMarkdown":"ok","destination":{"unlinkedLabel":"Alex"}}}`
	cases := []struct{ name, raw, want string }{
		{"unknown", strings.Replace(base, `"contextMarkdown":"ok"`, `"unknownKey":"ok"`, 1), "unknownKey"},
		{"misplaced", strings.Replace(base, `"contextMarkdown":"ok"`, `"preparedMessageId":"ok"`, 1), "it belongs at request.update.preparedMessageId"},
		{"duplicate", strings.Replace(base, `"contextMarkdown":"ok"`, `"contextMarkdown":"ok","contextMarkdown":"again"`, 1), "duplicate key contextMarkdown"},
		{"type", strings.Replace(base, `"contextMarkdown":"ok"`, `"contextMarkdown":12`, 1), "wrong JSON type"},
		{"no-title", strings.Replace(base, `"contextMarkdown":"ok"`, `"summary":"ok"`, 1), "Messages have no title"},
		{"null", strings.Replace(base, `"contextMarkdown":"ok"`, `"contextMarkdown":null`, 1), "cannot be null"},
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
		return &v1.AuthoredContent{FormatVersion: 3, ExpectedAuthorUserId: owner, WorkspaceId: ws, Title: "title", OriginalDestinationIntent: "Alex", ContextMarkdown: "contextMarkdown", Destination: &v1.ContentDestination{Target: &v1.ContentDestination_UnlinkedLabel{UnlinkedLabel: "Alex"}}}
	}
	for _, name := range []string{"formatVersion", "expectedAuthorUserId", "workspaceId", "title", "originalDestinationIntent", "contextMarkdown", "destination"} {
		t.Run(name, func(t *testing.T) {
			c := makeContent()
			switch name {
			case "formatVersion":
				c.FormatVersion = 1
			case "expectedAuthorUserId":
				c.ExpectedAuthorUserId = ""
			case "workspaceId":
				c.WorkspaceId = ""
			case "title":
				c.Title = ""
			case "originalDestinationIntent":
				c.OriginalDestinationIntent = ""
			case "contextMarkdown":
				c.ContextMarkdown = ""
			case "destination":
				c.Destination = nil
			}
			want := name
			if name == "formatVersion" {
				want = "LEGACY_CONTENT_FORMAT"
			}
			if e := clientContent("content", c, owner); e == nil || !strings.Contains(e.Error(), want) {
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
	if e := clientRevision("expectedThreadRevision", "bad"); e == nil || !strings.Contains(e.Error(), "syntra thread") {
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
		mutate func(*v1.PreparedContentExpectation)
	}{{"threadId", func(x *v1.PreparedContentExpectation) { x.ThreadId = "" }}, {"preparedMessageId", func(x *v1.PreparedContentExpectation) { x.PreparedMessageId = "" }}, {"expectedThreadRevision", func(x *v1.PreparedContentExpectation) { x.ExpectedThreadRevision = "" }}, {"expectedPreparedMessageRevision", func(x *v1.PreparedContentExpectation) { x.ExpectedPreparedMessageRevision = "" }}, {"expectedContentDigest", func(x *v1.PreparedContentExpectation) { x.ExpectedContentDigest = nil }}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := &v1.PreparedContentExpectation{ThreadId: id, PreparedMessageId: id, ExpectedThreadRevision: revision, ExpectedPreparedMessageRevision: revision, ExpectedContentDigest: make([]byte, 32)}
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
