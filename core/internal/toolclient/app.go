package toolclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
	ci "syntra.local/mcp/internal/contentintegrity"
)

// App lives only for one operation while Store's lock is held. CLI and MCP use
// the same typed methods, not arbitrary procedure names or executable content.
type App struct {
	client  *Client
	store   *Store
	profile *Profile
}

func NewApp(ctx context.Context, c *Client, s *Store) (*App, error) {
	if c == nil || s == nil {
		return nil, ErrProfile
	}
	p, e := s.Load()
	if e != nil {
		return nil, ProfileError(s.dir, e)
	}
	if _, e = c.Identity(ctx, p); e != nil {
		return nil, ConnectionError(s.dir, p, e)
	}
	return &App{c, s, p}, nil
}

func (a *App) authorize(ctx context.Context, cap v1.ToolCapability) error {
	if _, e := a.Identity(ctx); e != nil {
		return e
	}
	for _, c := range a.profile.Config.Capabilities {
		if c == cap {
			return nil
		}
	}
	return &MissingCapability{Required: cap, granted: a.profile.Config.Capabilities, dir: a.store.dir}
}
func (a *App) content() syntrav1connect.AuthoredContentServiceClient {
	return syntrav1connect.NewAuthoredContentServiceClient(a.client, a.client.origin, RPCOptions()...)
}
func ReadContentInput(r io.Reader, target proto.Message) error {
	b, e := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if e != nil || len(b) > maxResponseBytes {
		return ci.ErrWire
	}
	return decodeClientContent(b, target, nil)
}
func digest(c *v1.AuthoredContent) ([]byte, error) {
	x, e := ci.FromProto(c)
	if e != nil {
		return nil, e
	}
	d, e := ci.Digest(x)
	return d[:], e
}
func fillDigest(c *v1.AuthoredContent, d *[]byte) error {
	h, e := digest(c)
	if e != nil {
		return e
	}
	if len(*d) == 0 {
		*d = h
	}
	if !bytes.Equal(*d, h) {
		return ci.ErrIntegrity
	}
	return nil
}
func jsonProto(m proto.Message) ([]byte, error) {
	raw, e := protojson.MarshalOptions{}.Marshal(m)
	if e != nil {
		return nil, e
	}
	var compact bytes.Buffer
	if e = json.Compact(&compact, raw); e != nil {
		return nil, e
	}
	return compact.Bytes(), nil
}
func strictRecord(raw []byte, keys []string, out any) error {
	if !utf8.Valid(raw) || !json.Valid(raw) || !validEscapedUnicode(raw) {
		return ErrProfile
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(d) != nil {
		return ErrProfile
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || !exactKeys(m, keys, nil) {
		return ErrProfile
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrProfile
	}
	return nil
}

// Identity returns only current verified metadata, never the profile credential.
func (a *App) Identity(ctx context.Context) (*v1.ToolConnection, error) {
	identity, err := a.client.Identity(ctx, a.profile)
	return identity, ConnectionError(a.store.dir, a.profile, err)
}

// Status describes the retained profile without returning its credential.
func (a *App) Status(ctx context.Context) ConnectionStatus {
	return DescribeProfile(ctx, a.store.dir, a.profile)
}
