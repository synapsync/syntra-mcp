package toolclient

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

const accountA = "11111111-1111-4111-8111-111111111111"
const accountB = "22222222-2222-4222-8222-222222222222"

type fixture struct {
	syntrav1connect.UnimplementedToolConnectionServiceHandler
	syntrav1connect.UnimplementedUserServiceHandler
	t          *testing.T
	store      *Store
	owner      string
	retained   *v1.HumanCreateRequest
	connection *v1.ToolConnection
	calls      int
	mutate     func(*v1.ToolConnection)
}

func (f *fixture) GetMe(context.Context, *connect.Request[v1.GetMeRequest]) (*connect.Response[v1.GetMeResponse], error) {
	return connect.NewResponse(&v1.GetMeResponse{Identity: &v1.SelfIdentity{ProviderSubject: accountB}, Profile: &v1.UserProfile{Id: f.owner}}), nil
}
func (f *fixture) HumanCreate(ctx context.Context, r *connect.Request[v1.HumanCreateRequest]) (*connect.Response[v1.HumanCreateResponse], error) {
	f.calls++
	if r.Header().Get("Authorization") != "Bearer human-session-only" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("private upstream body human-session-only"))
	}
	p, e := f.store.Load()
	if e != nil || p.State != "pending" {
		f.t.Fatal("pending was not durable before issuance")
	}
	sum := sha256.Sum256([]byte(p.Secret))
	if !reflect.DeepEqual(sum[:], r.Msg.SecretDigest) || r.Msg.RequestId != p.RequestID || r.Msg.Label != p.Config.Label || !equalCaps(r.Msg.Capabilities, p.Config.Capabilities) || r.Msg.ExpiresInSeconds != p.Config.LifetimeSeconds {
		f.t.Fatal("issuance mismatch")
	}
	if f.retained == nil {
		f.retained = proto.Clone(r.Msg).(*v1.HumanCreateRequest)
		now := time.Now().UTC()
		f.connection = &v1.ToolConnection{Id: "33333333-3333-4333-8333-333333333333", OwnerUserId: f.owner, Label: r.Msg.Label, Capabilities: r.Msg.Capabilities, CreatedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(time.Duration(r.Msg.ExpiresInSeconds) * time.Second)), State: 1}
	} else if !proto.Equal(f.retained, r.Msg) {
		f.t.Fatal("retry changed issuance request")
	}
	out := proto.Clone(f.connection).(*v1.ToolConnection)
	if f.mutate != nil {
		f.mutate(out)
	}
	return connect.NewResponse(&v1.HumanCreateResponse{Connection: out}), nil
}
func (f *fixture) GetIdentity(context.Context, *connect.Request[v1.GetIdentityRequest]) (*connect.Response[v1.GetIdentityResponse], error) {
	return connect.NewResponse(&v1.GetIdentityResponse{Connection: f.connection}), nil
}
func setupClient(t *testing.T) (*Client, *Store, *fixture, Config) {
	t.Helper()
	s, e := OpenStore(context.Background(), filepath.Join(t.TempDir(), "profile"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	f := &fixture{t: t, store: s, owner: accountA}
	mux := http.NewServeMux()
	mux.Handle(syntrav1connect.NewToolConnectionServiceHandler(f))
	mux.Handle(syntrav1connect.NewUserServiceHandler(f))
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c, e := NewClient(srv.URL, pool)
	if e != nil {
		t.Fatal(e)
	}
	cfg := config()
	cfg.APIOrigin = srv.URL
	return c, s, f, cfg
}

type lostTransport struct {
	base http.RoundTripper
	lost bool
}

func (l *lostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, e := l.base.RoundTrip(r)
	if e == nil && !l.lost && strings.HasSuffix(r.URL.Path, "/HumanCreate") {
		l.lost = true
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return nil, errors.New("response lost")
	}
	return res, e
}
func TestDigestOnlyDurableIssuanceAndExactRecovery(t *testing.T) {
	c, s, f, cfg := setupClient(t)
	c.http.Transport = &lostTransport{base: c.http.Transport}
	if _, e := c.CompleteLogin(context.Background(), s, cfg, "human-session-only"); e == nil {
		t.Fatal("lost response succeeded")
	}
	p, e := s.Load()
	if e != nil || p.State != "pending" {
		t.Fatal(e)
	}
	secret, request := p.Secret, p.RequestID
	p, e = c.CompleteLogin(context.Background(), s, cfg, "human-session-only")
	if e != nil {
		t.Fatal(e)
	}
	if p.State != "active" || p.Secret != secret || p.RequestID != request || f.calls != 2 {
		t.Fatal("recovery replaced pending credential")
	}
	if p.AccountID != accountA {
		t.Fatal("used provider subject")
	}
	if _, e = c.Identity(context.Background(), p); e != nil {
		t.Fatal(e)
	}
}
func TestPendingMismatchAndMetadataGuard(t *testing.T) {
	for _, kind := range []string{"owner", "caps", "label", "lifetime", "expired", "bad-id", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			c, s, f, cfg := setupClient(t)
			f.mutate = func(q *v1.ToolConnection) {
				switch kind {
				case "owner":
					q.OwnerUserId = accountB
				case "caps":
					q.Capabilities = []v1.ToolCapability{1, 2}
				case "label":
					q.Label += "changed"
				case "lifetime":
					q.ExpiresAt.Seconds++
				case "expired":
					q.CreatedAt = timestamppb.New(time.Now().Add(-time.Hour))
					q.ExpiresAt = timestamppb.New(q.CreatedAt.AsTime().Add(300 * time.Second))
				case "bad-id":
					q.Id = "bad"
				case "revoked":
					q.State = 2
				}
			}
			if _, e := c.CompleteLogin(context.Background(), s, cfg, "human-session-only"); e == nil {
				t.Fatal("accepted mismatched metadata")
			}
			p, e := s.Load()
			if e != nil || p.State != "pending" {
				t.Fatal("pending not retained", e)
			}
			before := p.Secret
			f.owner = accountB
			if _, e = c.CompleteLogin(context.Background(), s, cfg, "human-session-only"); e == nil {
				t.Fatal("different account accepted")
			}
			p, _ = s.Load()
			if p.Secret != before || f.calls != 1 {
				t.Fatal("account mismatch mutated pending")
			}
			cfg.Label += "different"
			if _, e = c.CompleteLogin(context.Background(), s, cfg, "human-session-only"); e == nil {
				t.Fatal("different config accepted")
			}
		})
	}
}
func TestTransportOriginRedirectBoundsAndRedaction(t *testing.T) {
	var forwarded atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
			return
		}
		if r.URL.Path == "/error" {
			w.WriteHeader(400)
			w.Write([]byte("human-session-only syntra_tool_private provider-secret-body"))
			return
		}
		http.Redirect(w, r, other.URL, 307)
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c, _ := NewClient(srv.URL, pool)
	r, _ := http.NewRequest("POST", srv.URL+"/redirect", nil)
	r.Header.Set("Authorization", "Bearer private")
	if _, e := c.Do(r); e == nil || strings.Contains(e.Error(), "private") {
		t.Fatal("unsafe redirect error")
	}
	if forwarded.Load() != 0 {
		t.Fatal("forwarded credential")
	}
	r, _ = http.NewRequest("GET", other.URL, nil)
	if _, e := c.Do(r); e == nil {
		t.Fatal("origin mismatch accepted")
	}
	r, _ = http.NewRequest("GET", srv.URL+"/large", nil)
	res, e := c.Do(r)
	if e == nil {
		_, e = io.ReadAll(res.Body)
		res.Body.Close()
	}
	if e == nil {
		t.Fatal("unbounded response")
	}
	p := &OTPProvider{client: c, key: "sb_publishable_fixtureOnlyPublicKey"}
	if _, e = p.post(context.Background(), "/error", map[string]string{}); e == nil || strings.Contains(e.Error(), "provider-secret-body") {
		t.Fatal("upstream body disclosure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ = http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	if _, e = c.Do(r); e == nil {
		t.Fatal("cancellation ignored")
	}
	untrusted, _ := NewClient(srv.URL, nil)
	r, _ = http.NewRequest("GET", srv.URL, nil)
	if _, e = untrusted.Do(r); e == nil {
		t.Fatal("untrusted fixture TLS accepted")
	}
}
func TestPersistedFieldAudit(t *testing.T) {
	c, s, _, cfg := setupClient(t)
	p, e := c.CompleteLogin(context.Background(), s, cfg, "human-session-only")
	if e != nil {
		t.Fatal(e)
	}
	if p.Validate() != nil {
		t.Fatal("invalid profile")
	}
	f, e := s.openProfile()
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	raw, e := io.ReadAll(f)
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 9 {
		t.Fatal("unexpected persisted fields")
	}
	for _, name := range []string{"version", "config", "accountId", "requestId", "secret", "state", "connectionId", "createdAt", "expiresAt"} {
		if _, ok := fields[name]; !ok {
			t.Fatal("missing field", name)
		}
	}
	for _, forbidden := range []string{"human-session-only", "refresh_token", "access_token", "provider_subject", "otp", "fixture@example"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("persisted forbidden session field")
		}
	}

	if p.Config.PublishableKey == "" {
		t.Fatal("missing public configuration")
	}
}
