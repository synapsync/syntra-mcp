package toolclient

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

// ancestryPageSize is the fixed page size the web uses to read inherited history.
const ancestryPageSize = 25

const (
	maxAncestryDepth    = 64
	maxAncestryPages    = 400
	maxAncestryMessages = 10000
)

var errAncestry = errors.New("fork ancestry unavailable or inconsistent; no partial history is returned")

// AncestryThread names one thread of a fork's ancestry and the published message
// where the next thread of the chain was forked from it.
type AncestryThread struct {
	ThreadID        string `json:"threadId"`
	Title           string `json:"title"`
	AnchorMessageID string `json:"anchorMessageId"`
}

// AncestryMessage is one inherited published message in the existing wire
// contract (Message is canonical Protobuf JSON of PublishedMessage), labeled with
// the thread where it was written.
type AncestryMessage struct {
	OriginThreadID    string          `json:"originThreadId"`
	OriginThreadTitle string          `json:"originThreadTitle"`
	Message           json.RawMessage `json:"message"`
}

// Ancestry is the read-only history a fork inherits, oldest to newest, through
// each inclusive anchor. It excludes the fork's own messages (see Messages). A
// thread that is not a fork has empty Threads and Messages.
type Ancestry struct {
	WorkspaceID string            `json:"workspaceId"`
	ThreadID    string            `json:"threadId"`
	Threads     []AncestryThread  `json:"threads"`
	Messages    []AncestryMessage `json:"messages"`
}

// Ancestry composes GetThread and the through-anchor ListMessages read, exactly
// as the web does, but exhausts every page. Authorization stays server side on
// each read; nothing is marked read or reviewed and no source receipt changes.
func (a *App) Ancestry(ctx context.Context, w, t string) (*Ancestry, error) {
	if !validUUID(w) || !validUUID(t) {
		return nil, ciInput()
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	fork, e := a.readThread(ctx, w, t)
	if e != nil {
		return nil, e
	}
	type segment struct {
		thread   AncestryThread
		messages []*v1.PublishedMessage // newest first
	}
	var segments []segment // newest ancestor first
	visited := map[string]bool{t: true}
	total, bytes := 0, 0
	for origin := fork.GetForkedFrom(); origin != nil; {
		if len(segments) >= maxAncestryDepth || !validUUID(origin.ThreadId) || !validUUID(origin.MessageId) || visited[origin.ThreadId] {
			return nil, errAncestry
		}
		visited[origin.ThreadId] = true
		source, e := a.readThread(ctx, w, origin.ThreadId)
		if e != nil {
			return nil, e
		}
		rows, e := a.ancestorMessages(ctx, w, origin)
		if e != nil {
			return nil, e
		}
		for _, m := range rows {
			total++
			bytes += proto.Size(m)
		}
		if total > maxAncestryMessages || bytes > 8*maxResponseBytes {
			return nil, errors.New("fork ancestry exceeds the safe size bound; no partial history is returned")
		}
		segments = append(segments, segment{ancestryThreadOf(source, origin), rows})
		origin = source.GetForkedFrom()
	}
	out := &Ancestry{WorkspaceID: w, ThreadID: t, Threads: []AncestryThread{}, Messages: []AncestryMessage{}}
	for i := len(segments) - 1; i >= 0; i-- {
		s := segments[i]
		out.Threads = append(out.Threads, s.thread)
		for j := len(s.messages) - 1; j >= 0; j-- {
			raw, e := protojson.Marshal(s.messages[j])
			if e != nil {
				return nil, errAncestry
			}
			out.Messages = append(out.Messages, AncestryMessage{OriginThreadID: s.thread.ThreadID, OriginThreadTitle: s.thread.Title, Message: raw})
		}
	}
	return out, nil
}

// ancestryThreadOf labels a source thread with the anchor that continues the chain.
func ancestryThreadOf(source *v1.Thread, origin *v1.ThreadForkOrigin) AncestryThread {
	return AncestryThread{ThreadID: source.Id, Title: source.Title, AnchorMessageID: origin.MessageId}
}

// ancestorMessages reads one ancestor through its anchor, newest first, following
// the opaque cursor with the fixed page size. An expired cursor restarts once.
func (a *App) ancestorMessages(ctx context.Context, w string, origin *v1.ThreadForkOrigin) ([]*v1.PublishedMessage, error) {
	client := syntrav1connect.NewMessageServiceClient(a.client, a.client.origin, RPCOptions()...)
	for restart := 0; restart < 2; restart++ {
		var all []*v1.PublishedMessage
		seen, tokens := map[string]bool{}, map[string]bool{}
		token, retry := "", false
		for n := 0; n < maxAncestryPages; n++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			through := origin.MessageId
			r, e := client.ListMessages(ctx, bearer(connect.NewRequest(&v1.ListMessagesRequest{
				WorkspaceId: w, ThreadId: origin.ThreadId, View: v1.MessageView_MESSAGE_VIEW_PUBLISHED,
				PageSize: ancestryPageSize, PageToken: token, NewestFirst: true, ThroughMessageId: &through,
			}), a.profile.Secret))
			if e != nil {
				if token != "" && restart == 0 && (connect.CodeOf(e) == connect.CodeFailedPrecondition || connect.CodeOf(e) == connect.CodeInvalidArgument) {
					retry = true
					break
				}
				return nil, a.authoredRPCError(e)
			}
			if len(r.Msg.PreparedMessages) != 0 {
				return nil, errAncestry
			}
			for _, m := range r.Msg.Messages {
				if m == nil || m.WorkspaceId != w || m.ThreadId != origin.ThreadId || !validUUID(m.Id) || seen[m.Id] {
					return nil, errAncestry
				}
				seen[m.Id] = true
				all = append(all, m)
			}
			next := r.Msg.NextPageToken
			if next == "" {
				if len(all) == 0 {
					return nil, errAncestry
				}
				return all, nil
			}
			if tokens[next] {
				return nil, errAncestry
			}
			tokens[next], token = true, next
		}
		if !retry {
			return nil, errAncestry
		}
	}
	return nil, errAncestry
}
