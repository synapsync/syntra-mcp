package toolclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	v1 "syntra.local/mcp/gen/syntra/v1"
)

// ancestryChain serves a fork chain: root <- middle <- leaf. Each thread holds
// published messages in order; a fork reads its source through its anchor.
type ancestryChain struct {
	mu        sync.Mutex
	threads   map[string]*v1.Thread
	messages  map[string][]string // thread -> message IDs, oldest first
	denied    map[string]bool
	requests  []*v1.ListMessagesRequest
	expireOne bool
	tamper    func(thread string, m *v1.PublishedMessage)
	empty     map[string]bool
}

func uid(n int) string { return fmt.Sprintf("%08x-0000-4000-8000-%012x", n, n) }

// newChain builds three levels: root has 30 messages, the middle thread is forked from root
// message 28 and has 3 own messages, the leaf is forked from middle message 2 and has 1 own message.
func newChain() (*ancestryChain, string, string, string) {
	c := &ancestryChain{threads: map[string]*v1.Thread{}, messages: map[string][]string{}, denied: map[string]bool{}, empty: map[string]bool{}}
	root, middle, leaf := uid(1), uid(2), uid(3)
	add := func(thread string, base, n int) {
		for i := 0; i < n; i++ {
			c.messages[thread] = append(c.messages[thread], uid(base+i))
		}
	}
	add(root, 1000, 30)
	add(middle, 2000, 3)
	add(leaf, 3000, 1)
	c.threads[root] = &v1.Thread{Id: root, WorkspaceId: workspaceID, Title: "Root"}
	c.threads[middle] = &v1.Thread{Id: middle, WorkspaceId: workspaceID, Title: "Middle", ForkedFrom: &v1.ThreadForkOrigin{ThreadId: root, MessageId: c.messages[root][27], Title: "Root"}}
	c.threads[leaf] = &v1.Thread{Id: leaf, WorkspaceId: workspaceID, Title: "Leaf", ForkedFrom: &v1.ThreadForkOrigin{ThreadId: middle, MessageId: c.messages[middle][1], Title: "Middle"}}
	return c, root, middle, leaf
}

func (c *ancestryChain) getThread(q *v1.GetThreadRequest) (*connect.Response[v1.GetThreadResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.threads[q.ThreadId]
	if t == nil || c.denied[q.ThreadId] {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("DO-NOT-LEAK-REMOTE"))
	}
	return connect.NewResponse(&v1.GetThreadResponse{Thread: t}), nil
}

// listMessages implements newest-first, inclusive-through-anchor paging with an opaque cursor "after:N".
func (c *ancestryChain) listMessages(q *v1.ListMessagesRequest) (*connect.Response[v1.ListMessagesResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, q)
	if c.denied[q.ThreadId] {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("DO-NOT-LEAK-REMOTE"))
	}
	if c.expireOne && q.PageToken != "" {
		c.expireOne = false
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("expired"))
	}
	ids := c.messages[q.ThreadId]
	end := len(ids)
	if q.ThroughMessageId != nil {
		end = -1
		for i, id := range ids {
			if id == *q.ThroughMessageId {
				end = i + 1
			}
		}
		if end < 0 {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("anchor"))
		}
	}
	if c.empty[q.ThreadId] {
		end = 0
	}
	start := 0
	if q.PageToken != "" {
		if _, e := fmt.Sscanf(q.PageToken, "after:%d", &start); e != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("token"))
		}
	}
	size := int(q.PageSize)
	if size == 0 {
		size = 25
	}
	out := &v1.ListMessagesResponse{}
	i := start
	for ; i < end && len(out.Messages) < size; i++ {
		m := &v1.PublishedMessage{Id: ids[end-1-i], WorkspaceId: workspaceID, ThreadId: q.ThreadId, ContextMarkdown: "body " + ids[end-1-i][:8]}
		if c.tamper != nil {
			c.tamper(q.ThreadId, m)
		}
		out.Messages = append(out.Messages, m)
	}
	if i < end {
		out.NextPageToken = fmt.Sprintf("after:%d", i)
	}
	return connect.NewResponse(out), nil
}

func decodeAncestry(t *testing.T, a *Ancestry) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Messages []map[string]any `json:"messages"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Messages
}

func TestAncestryChainedForkReturnsLabeledHistoryOldestFirst(t *testing.T) {
	app, f, _, _ := setupContent(t)
	chain, root, middle, leaf := newChain()
	f.chain = chain
	got, err := app.Ancestry(context.Background(), workspaceID, leaf)
	if err != nil {
		t.Fatal(err)
	}
	// root through message 28 (28 messages, two pages of 25 and 3) then middle through message 2 (2 messages).
	if len(got.Messages) != 30 || len(got.Threads) != 2 {
		t.Fatalf("messages=%d threads=%d", len(got.Messages), len(got.Threads))
	}
	if got.Threads[0].ThreadID != root || got.Threads[0].Title != "Root" || got.Threads[0].AnchorMessageID != chain.messages[root][27] ||
		got.Threads[1].ThreadID != middle || got.Threads[1].Title != "Middle" || got.Threads[1].AnchorMessageID != chain.messages[middle][1] {
		t.Fatalf("unexpected ancestry threads %+v", got.Threads)
	}
	want := append(append([]string{}, chain.messages[root][:28]...), chain.messages[middle][:2]...)
	for i, m := range got.Messages {
		var decoded v1.PublishedMessage
		if err = protojson.Unmarshal(m.Message, &decoded); err != nil || decoded.Id != want[i] {
			t.Fatalf("message %d = %s, want %s (%v)", i, decoded.Id, want[i], err)
		}
		origin := root
		if i >= 28 {
			origin = middle
		}
		if m.OriginThreadID != origin || decoded.ThreadId != origin || m.OriginThreadTitle == "" {
			t.Fatalf("message %d is not labeled with its origin thread: %+v", i, m)
		}
	}
	if got.Messages[28].OriginThreadTitle != "Middle" || got.Messages[0].OriginThreadTitle != "Root" {
		t.Fatal("origin titles not carried")
	}
	pages := 0
	for _, q := range chain.requests {
		if q.PageSize != 25 || !q.NewestFirst || q.ThroughMessageId == nil || q.View != v1.MessageView_MESSAGE_VIEW_PUBLISHED {
			t.Fatalf("ancestor read left the fixed contract: %v", q)
		}
		pages++
	}
	if pages != 3 {
		t.Fatalf("expected 3 ancestor pages (2 for root, 1 for middle), got %d", pages)
	}
	for _, q := range chain.requests {
		if q.ThreadId == leaf {
			t.Fatal("ancestry read the fork's own messages")
		}
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "summary") {
		t.Fatal("legacy summary leaked")
	}
	_ = decodeAncestry(t, got)
}

func TestAncestryOfNonForkIsEmptyAndRestartsExpiredCursor(t *testing.T) {
	app, f, _, _ := setupContent(t)
	chain, root, middle, _ := newChain()
	f.chain = chain
	got, err := app.Ancestry(context.Background(), workspaceID, root)
	if err != nil || len(got.Messages) != 0 || len(got.Threads) != 0 || len(chain.requests) != 0 {
		t.Fatalf("root ancestry = %+v %v", got, err)
	}
	chain.expireOne = true
	got, err = app.Ancestry(context.Background(), workspaceID, middle)
	if err != nil || len(got.Messages) != 28 {
		t.Fatalf("expired cursor was not restarted once: %v", err)
	}
}

func TestAncestryFailsClosedWithoutPartialHistory(t *testing.T) {
	for name, mutate := range map[string]func(c *ancestryChain, root, middle, leaf string){
		"ancestor refused on thread read": func(c *ancestryChain, root, _, _ string) { c.denied[root] = true },
		"unknown ancestor": func(c *ancestryChain, _, middle, _ string) {
			c.threads[middle].ForkedFrom.ThreadId = uid(9)
		},
		"empty segment": func(c *ancestryChain, root, _, _ string) { c.empty[root] = true },
		"cycle": func(c *ancestryChain, root, _, leaf string) {
			c.threads[root].ForkedFrom = &v1.ThreadForkOrigin{ThreadId: leaf, MessageId: c.messages[leaf][0]}
		},
		"message from another thread": func(c *ancestryChain, root, _, _ string) {
			c.tamper = func(thread string, m *v1.PublishedMessage) {
				if thread == root {
					m.ThreadId = uid(77)
				}
			}
		},
		"message from another workspace": func(c *ancestryChain, _, middle, _ string) {
			c.tamper = func(thread string, m *v1.PublishedMessage) {
				if thread == middle {
					m.WorkspaceId = uid(78)
				}
			}
		},
		"duplicate message": func(c *ancestryChain, root, _, _ string) {
			c.messages[root][5] = c.messages[root][4]
		},
		"ancestor in another workspace": func(c *ancestryChain, root, _, _ string) { c.threads[root].WorkspaceId = uid(79) },
	} {
		t.Run(name, func(t *testing.T) {
			app, f, _, _ := setupContent(t)
			chain, root, middle, leaf := newChain()
			mutate(chain, root, middle, leaf)
			f.chain = chain
			got, err := app.Ancestry(context.Background(), workspaceID, leaf)
			if err == nil || got != nil {
				t.Fatalf("expected a fail-closed error, got %+v", got)
			}
			if strings.Contains(err.Error(), "DO-NOT-LEAK") {
				t.Fatal("upstream text leaked")
			}
		})
	}
}

func TestAncestryRequiresReadAndValidIDs(t *testing.T) {
	app, f, _, _ := setupContent(t)
	chain, _, _, leaf := newChain()
	f.chain = chain
	if _, err := app.Ancestry(context.Background(), "bad", leaf); err == nil {
		t.Fatal("accepted a malformed workspace")
	}
	if _, err := app.Ancestry(context.Background(), workspaceID, "bad"); err == nil {
		t.Fatal("accepted a malformed thread")
	}
	app.profile.Config.Capabilities = []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_PREPARE}
	if _, err := app.Ancestry(context.Background(), workspaceID, leaf); err == nil || len(chain.requests) != 0 {
		t.Fatal("a connection without READ reached the API")
	}
}
