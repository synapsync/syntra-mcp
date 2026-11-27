package toolmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/contentintegrity/actionable"
	"syntra.local/mcp/internal/testsupport"
)

func TestForkArgumentsAreBoundedAndExact(t *testing.T) {
	const fields = `"workspaceId":"workspace","sourceThreadId":"thread","anchorMessageId":"message","title":"Follow-up","requestId":"request"`
	for _, tc := range []struct {
		name, suffix string
		present      bool
		ids          []string
	}{
		{name: "omitted"},
		{name: "explicit", suffix: `,"sourceMemberIds":["one","two"]`, present: true, ids: []string{"one", "two"}},
		{name: "explicit empty", suffix: `,"sourceMemberIds":[]`, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := decodeArgs("fork", json.RawMessage("{"+fields+tc.suffix+"}"))
			if err != nil {
				t.Fatal(err)
			}
			if (a.fork.SourceParticipants != nil) != tc.present || strings.Join(a.fork.SourceParticipants.GetUserIds(), ",") != strings.Join(tc.ids, ",") {
				t.Fatalf("wrong source selection: %v", a.fork)
			}
		})
	}
	for name, bad := range map[string]string{
		"unknown key":             fields + `,"markRead":true`,
		"duplicate key":           fields + `,"title":"second"`,
		"number as text":          strings.Replace(fields, `"message"`, "7", 1),
		"null as text":            strings.Replace(fields, `"message"`, "null", 1),
		"empty required text":     strings.Replace(fields, `"message"`, `""`, 1),
		"oversized text":          strings.Replace(fields, "Follow-up", strings.Repeat("a", 2049), 1),
		"number as array":         fields + `,"sourceMemberIds":7`,
		"null extra member list":  fields + `,"extraMemberIds":null`,
		"null source member list": fields + `,"sourceMemberIds":null`,
		"non-string members":      fields + `,"sourceMemberIds":[7]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeArgs("fork", json.RawMessage("{"+bad+"}")); err == nil {
				t.Fatal("accepted invalid fork arguments")
			}
		})
	}
}

func TestForkToolIsAWriteWithTitleLimit(t *testing.T) {
	tool := toolDefinition("fork")
	if tool.Annotations.ReadOnlyHint {
		t.Fatal("fork creates a thread and must be advertised as a write")
	}
	schema := tool.InputSchema.(map[string]any)
	title := schema["properties"].(map[string]any)["title"].(map[string]any)
	if title["maxLength"] != 240 {
		t.Fatalf("wrong fork title limit: %v", title)
	}
}

func forkArgs(requestID string) map[string]any {
	return map[string]any{"workspaceId": workspaceID, "sourceThreadId": threadID, "anchorMessageId": messageID, "title": "Follow-up", "requestId": requestID}
}

func runForkCLI(f *setup, requestID string, flags ...string) ([]byte, error) {
	args := []string{"fork", "--profile-dir", f.dir, "--workspace", workspaceID, "--thread", threadID,
		"--message", messageID, "--title", "Follow-up", "--request-id", requestID}
	cmd := exec.Command(f.binary, append(args, flags...)...)
	cmd.Env = testsupport.FixtureCLIEnv()
	return cmd.CombinedOutput()
}

func TestForkRejectionsCLIAndMCP(t *testing.T) {
	f := fixture(t)
	p := spawn(t, f)
	p.init(t)
	for _, reason := range []actionable.Reason{
		actionable.ForkParticipantLimit, actionable.RequestIDConflict, actionable.ForkSourcePending,
		actionable.AnchorUnavailable, actionable.ForkSourceMemberInactive, actionable.ForkWorkspaceUnavailable,
	} {
		t.Run(string(reason), func(t *testing.T) {
			public := actionable.New(reason)
			f.api.mu.Lock()
			f.api.mutationError = public.Connect()
			f.api.mu.Unlock()
			requestID := id()
			want := public.Error()
			if reason == actionable.RequestIDConflict {
				want = "reason=REQUEST_ID_CONFLICT; This requestId is already bound to a different fork. Resend the original fork arguments and requestId to recover its result, or use a fresh requestId for changed input."
			}
			if got := string(toolResult(t, p.call(t, "fork", forkArgs(requestID)), true)); got != want {
				t.Fatalf("MCP rejection = %q, want %q", got, want)
			}
			if out, err := runForkCLI(f, requestID); err == nil || string(out) != want+"\n" {
				t.Fatalf("CLI rejection = %q, %v, want %q", out, err, want)
			}
		})
	}
	t.Run("noncanonical reason is redacted", func(t *testing.T) {
		public := actionable.New(actionable.AnchorUnavailable)
		f.api.mu.Lock()
		f.api.mutationError = connect.NewError(connect.CodeNotFound, errors.New(public.Error()+" fixture-private-data"))
		f.api.mu.Unlock()
		requestID := id()
		const want = "syntra: API request failed (not_found)"
		const mcpFallback = "operation rejected or response could not be verified; no rollback is implied; reuse the same requestId for exact retry"
		if got := string(toolResult(t, p.call(t, "fork", forkArgs(requestID)), true)); got != mcpFallback {
			t.Fatalf("MCP rejection = %q, want %q", got, mcpFallback)
		}
		if out, err := runForkCLI(f, requestID); err == nil || string(out) != want+"\n" {
			t.Fatalf("CLI rejection = %q, %v, want %q", out, err, want)
		}
	})
}

func TestForkCLIRejectsMalformedMemberFlagsBeforeRPC(t *testing.T) {
	f := fixture(t)
	for _, flag := range []string{"--member", "--source-member"} {
		if out, err := runForkCLI(f, id(), flag, "bad-id"); err == nil || !bytes.Contains(out, []byte("member ID")) {
			t.Fatalf("%s: expected local member ID error, got %q, %v", flag, out, err)
		}
	}
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	if len(f.api.forks) != 0 {
		t.Fatal("invalid member flags reached the fork RPC")
	}
}

func TestAnchoredReplyRejectionsCLIAndMCP(t *testing.T) {
	f := fixture(t)
	p := spawn(t, f)
	p.init(t)
	for _, reason := range []actionable.Reason{actionable.AnchorUnavailable, actionable.ReplyParticipationRequired, actionable.ThreadClosed} {
		t.Run(string(reason), func(t *testing.T) {
			public := actionable.New(reason)
			f.api.mu.Lock()
			f.api.mutationError = public.Connect()
			f.api.mu.Unlock()
			r := newRequest()
			r.Content.FormatVersion = 3
			r.Content.ExistingThreadId = str(threadID)
			r.Content.ReplyToMessageId = str(messageID)
			r.Content.Title = "Visible title"
			r.Content.Destination = &v1.ContentDestination{Target: &v1.ContentDestination_ReplyAll{ReplyAll: true}}
			r.ExpectedThreadRevision = str(revision(1))
			want := public.Error() + " (retained request " + r.RequestId + ")"
			if got := string(toolResult(t, p.call(t, "prepare", rpcArgs(t, r, "")), true)); got != want {
				t.Fatalf("MCP prepare = %q, want %q", got, want)
			}
			cmd := exec.Command(f.binary, "reply", "--profile-dir", f.dir, "--workspace", workspaceID, "--thread", threadID,
				"--message", messageID, "--context-file", "-")
			cmd.Env = testsupport.FixtureCLIEnv()
			cmd.Stdin = strings.NewReader("Reply context")
			if out, err := cmd.CombinedOutput(); err == nil || !bytes.HasPrefix(out, []byte(public.Error()+" (retained request ")) {
				t.Fatalf("CLI reply = %q, %v, want %q", out, err, public)
			}
		})
	}
}
