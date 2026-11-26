package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/toolclient"
)

func TestReplyTemplate(t *testing.T) {
	const w = "22222222-2222-4222-8222-222222222222"
	const id = "44444444-4444-4444-8444-444444444444"
	const author = "11111111-1111-4111-8111-111111111111"
	const creator = "33333333-3333-4333-8333-333333333333"
	const revision = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = write
	err = printTemplate("reply", w, "", author, &v1.Thread{Id: id, WorkspaceId: w, CreatorUserId: creator, State: v1.ThreadState_THREAD_STATE_OPEN, Title: "Title", Revision: revision}, "")
	write.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(read)
	read.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("template: %s", data)
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["expectedThreadRevision"] != revision {
		t.Fatal("revision not top level")
	}
	var q v1.PrepareContentRequest
	if err = toolclient.ReadContentInput(strings.NewReader(string(data)), &q); err == nil {
		t.Fatal("placeholders accepted")
	}
	data = []byte(strings.ReplaceAll(string(data), "TODO: SYNTRA ", "Finished "))
	if err = toolclient.ReadContentInput(strings.NewReader(string(data)), &q); err != nil {
		t.Fatal(err)
	}
	if q.Content.GetExistingThreadId() != id || !q.Content.GetDestination().GetReplyAll() {
		t.Fatal("wrong thread or destination")
	}
}
