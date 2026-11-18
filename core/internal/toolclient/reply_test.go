package toolclient

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/contentintegrity/actionable"
	"syntra.local/mcp/internal/testsupport"
)

func fixtureReply() *v1.PrepareContentRequest {
	r := request()
	r.Content.ProjectId = ptr(projectID)
	r.Content.Title = "Fixture title"
	r.Content.ExistingTicketId = ptr(ticketID)
	r.ExpectedTicketRevision = ptr(rev(88))
	return r
}

func TestAuthoredRPCGuidance(t *testing.T) {
	for _, tc := range []struct {
		reason actionable.Reason
		step   string
	}{
		{actionable.TicketClosed, "reopens the ticket in the Syntra web app"},
		{actionable.TicketNotPublished, "Publish its initial message"},
		{actionable.TicketTitleMismatch, "syntra prepare --template reply or syntra reply"},
		{actionable.ProjectMismatch, "Copy the exact projectId"},
		{actionable.StaleTicketRevision, "Re-read it with syntra ticket"},
		{actionable.StaleDraftRevision, "read it with syntra snapshot"},
		{actionable.NotAuthoredDraft, "create a new authored draft"},
		{actionable.RecipientNotEligible, "Check syntra members"},
		{actionable.RecipientNotLinked, "syntra link"},
		{actionable.InitialDraftAlreadyPublished, "prepare a reply"},
		{actionable.DraftRoutingChange, "may change only the summary and Markdown"},
		{actionable.DraftAlreadyLinked, "prepare a new draft for a different person"},
		{actionable.LinkContentChange, "may change only the draft's destination"},
		{actionable.RequestIDConflict, "use a fresh requestId"},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			public := actionable.New(tc.reason)
			if tc.reason == actionable.TicketTitleMismatch {
				public = actionable.TitleMismatch("Current\nTitle\x1b\u2028")
			}
			got := safeAuthoredRPCError(public.Connect()).Error()
			if got != public.Error() || !strings.HasPrefix(got, "reason="+string(tc.reason)+"; ") || !strings.Contains(got, tc.step) || strings.ContainsAny(got, "\n\r\x1b\u2028") {
				t.Fatalf("unexpected rendering: %q", got)
			}
			if tc.reason == actionable.TicketTitleMismatch && !strings.HasSuffix(got, `"Current\nTitle\x1b\u2028"`) {
				t.Fatalf("title was lost or unsafely rendered: %q", got)
			}
		})
	}
	for _, msg := range []string{
		"private upstream error",
		"reason=TICKET_CLOSED; private upstream error",
		"reason=UNKNOWN_REASON; private upstream error",
		actionable.New(actionable.TicketClosed).Error() + "\nprivate upstream error",
	} {
		if got := safeAuthoredRPCError(connect.NewError(connect.CodeFailedPrecondition, errors.New(msg))).Error(); got != "API request failed (failed_precondition)" {
			t.Fatalf("unexpected disclosure: %q", got)
		}
	}
	if got := safeAuthoredRPCError(connect.NewError(connect.CodeNotFound, actionable.New(actionable.TicketClosed))).Error(); got != "API request failed (not_found)" {
		t.Fatalf("unrelated status accepted: %q", got)
	}
}

func TestPrepareReplyPreflight(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state v1.TicketState
		edit  func(*v1.PrepareContentRequest)
		want  error
	}{
		{"closed", v1.TicketState_TICKET_STATE_CLOSED, nil, actionable.New(actionable.TicketClosed)},
		{"draft", v1.TicketState_TICKET_STATE_DRAFT, nil, actionable.New(actionable.TicketNotPublished)},
		{"title", 0, func(r *v1.PrepareContentRequest) { r.Content.Title = "Respuesta: Fixture title" }, actionable.TitleMismatch("Fixture title")},
		{"project", 0, func(r *v1.PrepareContentRequest) { r.Content.ProjectId = nil }, actionable.New(actionable.ProjectMismatch)},
		{"stale", 0, func(r *v1.PrepareContentRequest) { r.ExpectedTicketRevision = ptr(rev(1)) }, actionable.New(actionable.StaleTicketRevision)},
		{"happy", 0, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f, _, _ := setupContent(t)
			f.mu.Lock()
			f.ticketState = tc.state
			f.mu.Unlock()
			r := fixtureReply()
			if tc.edit != nil {
				tc.edit(r)
			}
			s, err := a.Prepare(context.Background(), r, "")
			f.mu.Lock()
			reads, calls := f.ticketReads, f.calls
			f.mu.Unlock()
			if reads != 1 {
				t.Fatalf("expected one ticket read, got %d", reads)
			}
			if tc.want != nil {
				if err == nil || err.Error() != tc.want.Error() || s != nil || calls != 0 {
					t.Fatalf("prepare reached mutation or returned different guidance: %v (calls %d)", err, calls)
				}
				if _, err := a.store.retained("attempt", r.RequestId); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("local rejection left a retained attempt: %v", err)
				}
				return
			}
			if err != nil || s == nil || !proto.Equal(s.Content, r.Content) || calls != 1 {
				t.Fatalf("prepare did not preserve happy path: %v (calls %d)", err, calls)
			}
			f.mu.Lock()
			f.ticketState = v1.TicketState_TICKET_STATE_CLOSED
			f.mu.Unlock()
			for _, replay := range []func() (*v1.AuthoredContentSnapshot, error){
				func() (*v1.AuthoredContentSnapshot, error) { return a.Retry(context.Background(), r.RequestId) },
				func() (*v1.AuthoredContentSnapshot, error) { return a.Prepare(context.Background(), r, "") },
			} {
				again, err := replay()
				if err != nil || !proto.Equal(s, again) {
					t.Fatalf("retained reply failed to reach the server's retry path: %v", err)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.ticketReads != 1 || f.calls != 3 {
				t.Fatalf("retry sent %d ticket reads and %d mutations", f.ticketReads, f.calls)
			}
		})
	}
}

func TestReplyPreflightKeepsPrepareCapabilityDenial(t *testing.T) {
	a, f, _, _ := setupContent(t)
	readOnly := []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}
	a.profile.Config.Capabilities = readOnly
	f.mu.Lock()
	f.p.Config.Capabilities = readOnly
	f.ticketState = v1.TicketState_TICKET_STATE_CLOSED
	f.mu.Unlock()

	_, err := a.Prepare(context.Background(), fixtureReply(), "")
	var missing *MissingCapability
	if !errors.As(err, &missing) || missing.Required != v1.ToolCapability_TOOL_CAPABILITY_PREPARE {
		t.Fatalf("expected capability denial: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ticketReads != 0 || f.calls != 0 {
		t.Fatalf("denied operation sent %d ticket reads and %d mutations", f.ticketReads, f.calls)
	}
}

func TestCompiledCLIActionableReplyErrors(t *testing.T) {
	a, f, profile, ca := setupContent(t)
	binary := testsupport.BuildCLI(t, "../../cmd/syntra", ca)
	a.store.Close()
	dir := t.TempDir()
	input := filepath.Join(dir, "reply.json")
	ctxPath := filepath.Join(dir, "context.md")
	if err := os.WriteFile(ctxPath, []byte("Details"), 0600); err != nil {
		t.Fatal(err)
	}
	r := fixtureReply()
	r.Content.Title = "Respuesta: Fixture title"
	body, err := protojson.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	check := func(want error, args ...string) {
		t.Helper()
		out, err := cliCall(t, binary, append(args, "--profile-dir", profile)...)
		if err == nil || string(out) != want.Error()+"\n" {
			t.Fatalf("unexpected CLI failure: %v %q", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	}
	check(actionable.TitleMismatch("Fixture title"), "prepare", "--file", input)
	f.mu.Lock()
	f.ticketState = v1.TicketState_TICKET_STATE_CLOSED
	f.mu.Unlock()
	closed := actionable.New(actionable.TicketClosed)
	check(closed, "prepare", "--file", input)
	check(closed, "prepare", "--template", "reply", "--workspace", workspaceID, "--ticket", ticketID)
	check(closed, "reply", "--workspace", workspaceID, "--ticket", ticketID, "--summary", "Update", "--context-file", ctxPath)

	out, err := cliCall(t, binary, "ticket", "--workspace", workspaceID, "--ticket", ticketID, "--profile-dir", profile)
	if err != nil || !bytes.Contains(out, []byte("TICKET_STATE_CLOSED")) || !bytes.Contains(out, []byte("Closed tickets accept no replies")) {
		t.Fatalf("closed ticket did not show state and restriction: %v %q", err, out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 0 || f.ticketReads != 5 {
		t.Fatalf("rejections sent %d mutations and %d ticket reads", f.calls, f.ticketReads)
	}
}
