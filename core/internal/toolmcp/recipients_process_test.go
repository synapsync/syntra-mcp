package toolmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

// Exercises the compiled stdio MCP process end to end against the fixture API: preparing to two
// people and to an explicit team, publishing exactly, and reading the recipient set back.
func TestMCPActualProcessRecipientSets(t *testing.T) {
	f := fixture(t)
	p := spawn(t, f)
	p.init(t)
	second := "55555555-0000-4000-8000-000000000002"
	third := "55555555-0000-4000-8000-000000000003"
	team := "66666666-0000-4000-8000-000000000001"

	prepare := func(destination *v1.ContentDestination) (*v1.PrepareContentRequest, *v1.AuthoredContentSnapshot) {
		q := newRequest()
		q.Content.Destination = destination
		raw := toolResult(t, p.call(t, "prepare", rpcArgs(t, q, "")), false)
		snapshot := &v1.AuthoredContentSnapshot{}
		if protojson.Unmarshal(raw, snapshot) != nil || !proto.Equal(snapshot.Content, q.Content) {
			t.Fatalf("exact destination changed: %s", raw)
		}
		return q, snapshot
	}
	people := &v1.ContentDestination{Target: &v1.ContentDestination_LinkedMemberSet{LinkedMemberSet: &v1.LinkedMemberSet{MemberIds: []string{recipientID, second}}}}
	q, prepared := prepare(people)
	if len(prepared.Content.Destination.GetLinkedMemberSet().MemberIds) != 2 {
		t.Fatal("recipient set lost")
	}
	// Publication binds the exact reviewed content, then the snapshot lists every recipient in order.
	pub := &v1.PublishContentRequest{WorkspaceId: workspaceID, ExpectedAuthorUserId: ownerID, Current: expect(prepared), RequestId: id()}
	raw := toolResult(t, p.call(t, "publish", rpcArgs(t, pub, q.RequestId)), false)
	var published v1.AuthoredContentSnapshot
	if protojson.Unmarshal(raw, &published) != nil || published.GetPublishedMessageId() != messageID {
		t.Fatalf("publish failed: %s", raw)
	}
	snapshot := &v1.GetContentSnapshotRequest{WorkspaceId: workspaceID, ThreadId: threadID, Record: &v1.GetContentSnapshotRequest_PublishedMessageId{PublishedMessageId: messageID}}
	raw = toolResult(t, p.call(t, "snapshot", rpcArgs(t, snapshot, pub.RequestId)), false)
	var read struct {
		Recipients []struct {
			UserId string `json:"userId"`
		} `json:"recipients"`
	}
	if json.Unmarshal(raw, &read) != nil || len(read.Recipients) != 2 || read.Recipients[0].UserId != recipientID || read.Recipients[1].UserId != second {
		t.Fatalf("snapshot did not list all recipients: %s", raw)
	}

	// An explicit team destination is accepted unchanged (the server expands it at send time).
	_, byTeam := prepare(&v1.ContentDestination{Target: &v1.ContentDestination_TeamId{TeamId: team}})
	if byTeam.Content.Destination.GetTeamId() != team {
		t.Fatal("team destination lost")
	}

	// Invalid sets are rejected locally before any mutation reaches the API.
	f.api.mu.Lock()
	before := f.api.mutations
	f.api.mu.Unlock()
	for name, destination := range map[string]*v1.ContentDestination{
		"duplicate": {Target: &v1.ContentDestination_LinkedMemberSet{LinkedMemberSet: &v1.LinkedMemberSet{MemberIds: []string{recipientID, recipientID}}}},
		"single":    {Target: &v1.ContentDestination_LinkedMemberSet{LinkedMemberSet: &v1.LinkedMemberSet{MemberIds: []string{recipientID}}}},
		"eleven": {Target: &v1.ContentDestination_LinkedMemberSet{LinkedMemberSet: &v1.LinkedMemberSet{MemberIds: []string{
			recipientID, second, third, teamMembers[0], teamMembers[1], teamMembers[2], teamMembers[3], "77777777-0000-4000-8000-000000000001", "77777777-0000-4000-8000-000000000002", "77777777-0000-4000-8000-000000000003", "77777777-0000-4000-8000-000000000004"}}}},
	} {
		bad := newRequest()
		bad.Content.Destination = destination
		out := toolResult(t, p.call(t, "prepare", rpcArgs(t, bad, "")), true)
		if len(out) == 0 {
			t.Fatalf("%s set was not rejected with a diagnostic", name)
		}
	}
	f.api.mu.Lock()
	after := f.api.mutations
	f.api.mu.Unlock()
	if after != before {
		t.Fatal("an invalid recipient set reached the API")
	}

	// resolve returns the exact destination for several people, with unambiguous identifiers.
	raw = toolResult(t, p.call(t, "resolve", map[string]any{"workspace": workspaceID, "recipients": []string{"Pedro"}}), false)
	if !strings.Contains(string(raw), `"linkedMemberId"`) || !strings.Contains(string(raw), recipientID) {
		t.Fatalf("resolve destination: %s", raw)
	}
	toolResult(t, p.call(t, "resolve", map[string]any{"workspace": workspaceID, "recipients": []string{"Pedro", "Pedro"}}), true)
	toolResult(t, p.call(t, "resolve", map[string]any{"workspace": workspaceID, "recipient": "Pedro", "recipients": []string{"Pedro"}}), true)
	toolResult(t, p.call(t, "resolve", map[string]any{"workspace": workspaceID, "asTeam": true}), true)
}
