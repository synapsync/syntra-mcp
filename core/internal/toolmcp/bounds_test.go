package toolmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestRawArgumentGateBeforeLossyDecode(t *testing.T) {
	for _, raw := range []string{`{"workspaceId":"one","workspaceId":"two"}`, `{"workspaceId":null}`, `{"workspace_id":"one"}`, `{"workspaceId":"\ud800"}`, `{"workspaceId":1}`, `{"workspaceId":"one","extra":true}`} {
		if _, e := decodeArgs("projects", []byte(raw)); e == nil {
			t.Fatal("raw ambiguous arguments accepted")
		}
	}
	for _, name := range []string{"identity", "workspaces"} {
		if _, e := decodeArgs(name, nil); e != nil {
			t.Fatal("standard omitted empty arguments rejected")
		}
	}
	if _, e := decodeArgs("prepare", []byte(`{"file":"/forbidden","request":{}}`)); e == nil {
		t.Fatal("tool accepted file input")
	}
}
func TestProjectTeamsArguments(t *testing.T) {
	if _, err := decodeArgs("teams", []byte(`{"workspaceId":"w","projectId":"p"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeArgs("teams", []byte(`{"workspaceId":"w"}`)); err == nil {
		t.Fatal("project required")
	}
	a, err := decodeArgs("resolve", []byte(`{"workspace":"w","project":"p","team":"t"}`))
	if err != nil || a.team != "t" {
		t.Fatal("team selector rejected", err)
	}
	if _, err := decodeArgs("resolve", []byte(`{"workspace":"w","area":"old"}`)); err == nil {
		t.Fatal("area selector accepted")
	}
}

func TestResultBoundIsNotPartialSuccess(t *testing.T) {
	out := result(map[string]string{"content": strings.Repeat("x", MaxResultBytes)}, false)
	if !out.IsError {
		t.Fatal("oversized tool result reported success")
	}
	raw, _ := json.Marshal(out)
	if len(raw) > MaxResultBytes {
		t.Fatal("error result itself unbounded")
	}
	if !strings.Contains(string(raw), "no rollback") {
		t.Fatal("size error implies rollback")
	}
}

type fakeConnection struct {
	writes atomic.Int32
	closed chan struct{}
}

func (f *fakeConnection) Read(context.Context) (jsonrpc.Message, error) { return nil, io.EOF }
func (f *fakeConnection) Write(context.Context, jsonrpc.Message) error {
	f.writes.Add(1)
	if f.closed != nil {
		<-f.closed
		return errors.New("closed")
	}
	return nil
}
func (f *fakeConnection) Close() error {
	if f.closed != nil {
		select {
		case <-f.closed:
		default:
			close(f.closed)
		}
	}
	return nil
}
func (f *fakeConnection) SessionID() string { return "fixture" }
func TestActualEncodedFrameBoundBeforeWrite(t *testing.T) {
	msg, e := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":{"value":"` + strings.Repeat("x", MaxFrameBytes) + `"}}`))
	if e != nil {
		t.Fatal(e)
	}
	base := &fakeConnection{}
	c := &boundedConnection{Connection: base, writeTimeout: time.Second}
	if e = c.Write(context.Background(), msg); e == nil || base.writes.Load() != 0 {
		t.Fatal("oversized encoded frame reached writer")
	}
}
func TestWriterDeadlineClosesBlockedConnection(t *testing.T) {
	msg, _ := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	base := &fakeConnection{closed: make(chan struct{})}
	c := &boundedConnection{Connection: base, writeTimeout: 30 * time.Millisecond}
	start := time.Now()
	if e := c.Write(context.Background(), msg); e == nil || time.Since(start) > time.Second {
		t.Fatal("writer deadline ineffective")
	}
	select {
	case <-base.closed:
	default:
		t.Fatal("blocked writer not closed")
	}
}
func TestOriginalFrameGateRejectsDuplicatesAndInvalidUTF8(t *testing.T) {
	for _, raw := range []string{`{"jsonrpc":"2.0","id":1,"method":"ping","method":"exec"}` + "\n", `{"jsonrpc":"2.0","id":1,"method":"` + string([]byte{255}) + `"}` + "\n", `{"jsonrpc":"2.0","id":1,"method":"\ud800"}` + "\n"} {
		f := newFrameReader(io.NopCloser(strings.NewReader(raw)))
		if b, e := io.ReadAll(f); e == nil || len(b) != 0 {
			t.Fatal("invalid original frame reached SDK decoder")
		}
	}
}
