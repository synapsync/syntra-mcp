package toolmcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"syntra.local/mcp/internal/toolclient"
)

// frameReader validates one bounded original line before the SDK can decode it.
// Nothing malformed is echoed into a JSON-RPC error or SDK logger.
type frameReader struct {
	source  io.ReadCloser
	r       *bufio.Reader
	pending []byte
}

func newFrameReader(r io.ReadCloser) *frameReader {
	return &frameReader{source: r, r: bufio.NewReaderSize(r, 4096)}
}
func (f *frameReader) Read(p []byte) (int, error) {
	if len(f.pending) == 0 {
		var line []byte
		for {
			part, e := f.r.ReadSlice('\n')
			if len(line)+len(part) > MaxFrameBytes {
				return 0, errors.New("MCP frame limit")
			}
			line = append(line, part...)
			if e == bufio.ErrBufferFull {
				continue
			}
			if e == io.EOF && len(line) == 0 {
				return 0, io.EOF
			}
			if e != nil && e != io.EOF {
				return 0, errors.New("MCP input failed")
			}
			break
		}
		raw := bytes.TrimSpace(line)
		if toolclient.ValidateRawObject(raw, MaxFrameBytes) != nil {
			return 0, errors.New("invalid MCP frame")
		}
		f.pending = append(raw, '\n')
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}
func (f *frameReader) Close() error { return f.source.Close() }

type boundedTransport struct {
	reader       io.ReadCloser
	writer       io.WriteCloser
	writeTimeout time.Duration
}

func (t *boundedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	base, e := (&mcp.IOTransport{Reader: newFrameReader(t.reader), Writer: t.writer, MaxLineLength: MaxFrameBytes}).Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &boundedConnection{Connection: base, writeTimeout: t.writeTimeout}, nil
}

type boundedConnection struct {
	mcp.Connection
	writeTimeout time.Duration
	once         sync.Once
}

func (c *boundedConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	raw, e := jsonrpc.EncodeMessage(msg)
	if e != nil || len(raw)+1 > MaxFrameBytes {
		return errors.New("MCP output frame limit")
	}
	done := make(chan error, 1)
	go func() { done <- c.Connection.Write(ctx, msg) }()
	timer := time.NewTimer(c.writeTimeout)
	defer timer.Stop()
	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		c.Close()
		return errors.New("MCP output cancelled")
	case <-timer.C:
		c.Close()
		return errors.New("MCP output deadline")
	}
}
func (c *boundedConnection) Close() error {
	var e error
	c.once.Do(func() { e = c.Connection.Close() })
	return e
}
