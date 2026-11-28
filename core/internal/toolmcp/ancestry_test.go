package toolmcp

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"syntra.local/mcp/internal/testsupport"
)

func TestAncestryArgumentsAreExactAndReadOnly(t *testing.T) {
	const ok = `{"workspaceId":"workspace","threadId":"thread"}`
	a, err := decodeArgs("ancestry", json.RawMessage(ok))
	if err != nil || a.workspace != "workspace" || a.thread != "thread" {
		t.Fatalf("valid arguments rejected: %v", err)
	}
	for name, bad := range map[string]string{
		"missing thread":    `{"workspaceId":"workspace"}`,
		"missing workspace": `{"threadId":"thread"}`,
		"unknown key":       `{"workspaceId":"workspace","threadId":"thread","view":"published"}`,
		"duplicate key":     `{"workspaceId":"workspace","threadId":"thread","threadId":"other"}`,
		"number as text":    `{"workspaceId":"workspace","threadId":7}`,
		"empty text":        `{"workspaceId":"workspace","threadId":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeArgs("ancestry", json.RawMessage(bad)); err == nil {
				t.Fatal("accepted invalid arguments")
			}
		})
	}
	tool := toolDefinition("ancestry")
	if !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
		t.Fatal("ancestry must be a read-only tool")
	}
	raw, _ := json.Marshal(tool.InputSchema)
	for _, want := range []string{`"workspaceId"`, `"threadId"`, `"additionalProperties":false`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("schema misses %s: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"view"`) {
		t.Fatal("ancestry takes no view")
	}
}

func TestAncestryAndThreadAgeCLIAndMCPOutputs(t *testing.T) {
	f := fixture(t)
	p := spawn(t, f)
	p.init(t)
	cli := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(f.binary, append(args, "--profile-dir", f.dir)...)
		cmd.Env = testsupport.FixtureCLIEnv()
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return string(out)
	}
	// A fork reads its direct source through the anchor; each message names its origin thread.
	mcp := string(toolResult(t, p.call(t, "ancestry", map[string]any{"workspaceId": workspaceID, "threadId": forkReadID}), false))
	out := cli("ancestry", "--workspace", workspaceID, "--thread", forkReadID)
	var fromCLI, fromMCP struct {
		ThreadID string `json:"threadId"`
		Threads  []struct{ ThreadID, Title, AnchorMessageID string }
		Messages []struct {
			OriginThreadID, OriginThreadTitle string
			Message                           map[string]any
		}
	}
	if err := json.Unmarshal([]byte(out), &fromCLI); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(mcp), &fromMCP); err != nil {
		t.Fatal(err)
	}
	if len(fromMCP.Messages) != 2 || len(fromMCP.Threads) != 1 || fromMCP.ThreadID != forkReadID {
		t.Fatalf("unexpected MCP ancestry: %s", mcp)
	}
	if fromMCP.Threads[0].ThreadID != threadID || fromMCP.Threads[0].AnchorMessageID != messageID {
		t.Fatalf("ancestor chain lost the anchor: %s", mcp)
	}
	for _, m := range fromMCP.Messages {
		if m.OriginThreadID != threadID || m.OriginThreadTitle != "Visible title" || m.Message["threadId"] != threadID {
			t.Fatalf("message not labeled with its origin thread: %+v", m)
		}
	}
	if !bytes.Equal(bytes.TrimSpace([]byte(out)), bytes.TrimSpace([]byte(mcp))) {
		t.Fatalf("CLI and MCP ancestry differ:\nCLI %s\nMCP %s", out, mcp)
	}
	// A thread that is not a fork has nothing inherited.
	empty := string(toolResult(t, p.call(t, "ancestry", map[string]any{"workspaceId": workspaceID, "threadId": threadID}), false))
	if !strings.Contains(empty, `"messages":[]`) || !strings.Contains(empty, `"threads":[]`) {
		t.Fatalf("non-fork ancestry not empty: %s", empty)
	}
	// Thread age: ISO in CLI JSON and MCP, relative age in CLI text.
	iso := lastMessageAt.Format(time.RFC3339)
	list := toolResult(t, p.call(t, "threads", map[string]any{"workspaceId": workspaceID}), false)
	cliJSON := cli("threads", "--workspace", workspaceID)
	for name, raw := range map[string][]byte{"MCP": list, "CLI": []byte(cliJSON)} {
		if bytes.Count(raw, []byte(`"lastMessagePublishedAt":"`+iso+`"`)) != 1 {
			t.Fatalf("%s thread listing lost lastMessagePublishedAt: %s", name, raw)
		}
	}
	text := cli("threads", "--workspace", workspaceID, "--format", "text")
	if !strings.Contains(text, "3h ago") || !strings.Contains(text, threadID) || !strings.Contains(text, "-") {
		t.Fatalf("text listing: %q", text)
	}
}

func TestLargeAncestryHasAReadAppropriateMCPErrorAndCLIStillSucceeds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		messages    int
		body        int
		mcpSucceeds bool
	}{
		{"about 120 long messages", 120, 9 * 1024, false},
		{"many short messages", 168, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture(t)
			f.api.mu.Lock()
			f.api.ancestryMessages, f.api.ancestryBody = tc.messages, tc.body
			f.api.mu.Unlock()
			p := spawn(t, f)
			p.init(t)
			cmd := exec.Command(f.binary, "ancestry", "--profile-dir", f.dir, "--workspace", workspaceID, "--thread", forkReadID)
			cmd.Env = testsupport.FixtureCLIEnv()
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("CLI: %v", err)
			}
			var cli struct{ Messages []json.RawMessage }
			if err = json.Unmarshal(out, &cli); err != nil || len(cli.Messages) != tc.messages {
				t.Fatalf("CLI returned %d messages (%v)", len(cli.Messages), err)
			}
			args := map[string]any{"workspaceId": workspaceID, "threadId": forkReadID}
			if tc.mcpSucceeds {
				raw := toolResult(t, p.call(t, "ancestry", args), false)
				if bytes.Count(raw, []byte(`"originThreadId"`)) != tc.messages {
					t.Fatal("MCP lost messages")
				}
				return
			}
			text := string(toolResult(t, p.call(t, "ancestry", args), true))
			if !strings.Contains(text, "syntra ancestry") || !strings.Contains(text, "Nothing was changed") {
				t.Fatalf("unhelpful MCP oversize error: %q", text)
			}
			for _, wrong := range []string{"requestId", "rollback", "retry"} {
				if strings.Contains(text, wrong) {
					t.Fatalf("read tool error mentions %q: %q", wrong, text)
				}
			}
		})
	}
}

func TestReadToolFailuresNeverTalkAboutRetainedRequests(t *testing.T) {
	for _, name := range toolNames {
		msg := oversizeMessage(name)
		mentionsWrite := strings.Contains(msg, "requestId") || strings.Contains(msg, "rollback")
		if readOnlyTool(name) == mentionsWrite {
			t.Errorf("%s: oversize text %q does not match its read/write nature", name, msg)
		}
	}
	if strings.Contains(readFallback, "requestId") || strings.Contains(readFallback, "rollback") {
		t.Fatal("read fallback implies a write")
	}
}
