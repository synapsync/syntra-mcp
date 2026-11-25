package toolmcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAddParticipantArgumentsAreBoundedAndExact(t *testing.T) {
	valid := `{"workspaceId":"44444444-4444-4444-8444-444444444444","threadId":"77777777-7777-4777-8777-777777777777","userId":"88888888-8888-4888-8888-888888888888","requestId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}`
	a, e := decodeArgs("add-participant", json.RawMessage(valid))
	if e != nil {
		t.Fatal(e)
	}
	if a.workspace != "44444444-4444-4444-8444-444444444444" || a.thread != "77777777-7777-4777-8777-777777777777" || a.member != "88888888-8888-4888-8888-888888888888" || a.requestID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("arguments must reach the handler unchanged: %+v", a)
	}
	for name, raw := range map[string]string{
		"missing request id": `{"workspaceId":"44444444-4444-4444-8444-444444444444","threadId":"77777777-7777-4777-8777-777777777777","userId":"88888888-8888-4888-8888-888888888888"}`,
		"missing member":     `{"workspaceId":"44444444-4444-4444-8444-444444444444","threadId":"77777777-7777-4777-8777-777777777777","requestId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}`,
		"unknown key":        `{"workspaceId":"44444444-4444-4444-8444-444444444444","threadId":"77777777-7777-4777-8777-777777777777","userId":"88888888-8888-4888-8888-888888888888","requestId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","markRead":true}`,
		"member as a number": `{"workspaceId":"44444444-4444-4444-8444-444444444444","threadId":"77777777-7777-4777-8777-777777777777","userId":7,"requestId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}`,
		"oversized member":   `{"workspaceId":"44444444-4444-4444-8444-444444444444","threadId":"77777777-7777-4777-8777-777777777777","userId":"` + strings.Repeat("a", 2049) + `","requestId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}`,
		"empty thread":       `{"workspaceId":"44444444-4444-4444-8444-444444444444","threadId":"","userId":"88888888-8888-4888-8888-888888888888","requestId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}`,
	} {
		if _, e := decodeArgs("add-participant", json.RawMessage(raw)); e == nil {
			t.Fatalf("%s must be rejected before any profile is opened", name)
		}
	}
}

func TestAddParticipantToolIsAWriteWithExplicitSchema(t *testing.T) {
	tool := toolDefinition("add-participant")
	if tool.Annotations.ReadOnlyHint {
		t.Fatal("add-participant changes thread membership and must not be advertised as read-only")
	}
	schema, _ := json.Marshal(tool.InputSchema)
	for _, field := range []string{"workspaceId", "threadId", "userId", "requestId"} {
		if !strings.Contains(string(schema), `"`+field+`"`) {
			t.Fatalf("schema lacks %s", field)
		}
	}
	if !strings.Contains(tool.Description, "publishing capability") || !strings.Contains(tool.Description, "without sending a message") {
		t.Fatalf("description must state the capability and that no message is sent: %q", tool.Description)
	}
}
