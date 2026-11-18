package toolclient

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"syntra.local/mcp/fixtures"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

const testBrowserOrigin = "https://syntra.example.test"

type browserHarness struct {
	fixture *fixtures.DeviceFixture
	server  *httptest.Server
	client  *Client
	config  BrowserConfig
	now     atomic.Int64
}

func newBrowserHarness(t *testing.T, options fixtures.TestHarnessOptions, handler func(*fixtures.DeviceFixture) http.Handler) *browserHarness {
	t.Helper()
	initial := time.Now().UTC().Truncate(time.Second)
	h := &browserHarness{}
	h.now.Store(initial.UnixNano())
	fixture, err := fixtures.NewDeviceFixtureForTestHarness(func() time.Time {
		return time.Unix(0, h.now.Load()).UTC()
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	h.fixture = fixture
	var endpoint http.Handler = fixture.Handler()
	if handler != nil {
		endpoint = handler(fixture)
	}
	h.server = httptest.NewTLSServer(endpoint)
	t.Cleanup(h.server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(h.server.Certificate())
	h.client, err = NewClient(h.server.URL, roots)
	if err != nil {
		t.Fatal(err)
	}
	h.config = BrowserConfig{
		APIOrigin: h.server.URL, BrowserOrigin: testBrowserOrigin, Label: "Synthetic browser client",
		Capabilities: []v1.ToolCapability{
			v1.ToolCapability_TOOL_CAPABILITY_READ,
			v1.ToolCapability_TOOL_CAPABILITY_PREPARE,
			v1.ToolCapability_TOOL_CAPABILITY_PUBLISH,
		},
		LifetimeSeconds: 600,
	}
	return h
}

func (h *browserHarness) advance(delay time.Duration) error {
	h.now.Add(int64(delay))
	return nil
}

func (h *browserHarness) approve(t *testing.T, caps []v1.ToolCapability, lifetime int32) {
	t.Helper()
	login := syntrav1connect.NewToolLoginServiceClient(h.client, h.client.origin, RPCOptions()...)
	get := connect.NewRequest(&v1.GetLoginRequestRequest{UserCode: fixtures.UserCode})
	get.Header().Set("Authorization", fixtures.HumanAuthorization)
	preview, err := login.GetLoginRequest(context.Background(), get)
	if err != nil {
		t.Fatal("synthetic human could not read preview", SafeRPCError(err))
	}
	approve := connect.NewRequest(&v1.ApproveLoginRequest{
		UserCode: fixtures.UserCode, ExpectedFingerprint: append([]byte(nil), preview.Msg.Request.Fingerprint...),
		Capabilities: append([]v1.ToolCapability(nil), caps...), ExpiresInSeconds: lifetime,
	})
	approve.Header().Set("Authorization", fixtures.HumanAuthorization)
	if _, err = login.ApproveLogin(context.Background(), approve); err != nil {
		t.Fatal("synthetic human could not approve", SafeRPCError(err))
	}
}

func (h *browserHarness) deny(t *testing.T) {
	t.Helper()
	login := syntrav1connect.NewToolLoginServiceClient(h.client, h.client.origin, RPCOptions()...)
	get := connect.NewRequest(&v1.GetLoginRequestRequest{UserCode: fixtures.UserCode})
	get.Header().Set("Authorization", fixtures.HumanAuthorization)
	preview, err := login.GetLoginRequest(context.Background(), get)
	if err != nil {
		t.Fatal("synthetic human could not read preview", SafeRPCError(err))
	}
	request := connect.NewRequest(&v1.DenyLoginRequest{UserCode: fixtures.UserCode, ExpectedFingerprint: append([]byte(nil), preview.Msg.Request.Fingerprint...)})
	request.Header().Set("Authorization", fixtures.HumanAuthorization)
	if _, err = login.DenyLogin(context.Background(), request); err != nil {
		t.Fatal("synthetic human could not deny", SafeRPCError(err))
	}
}

func readPendingBrowser(t *testing.T, dir string) (*PendingBrowserAttempt, []byte) {
	t.Helper()
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	attempt, raw, err := store.LoadPendingBrowserAttemptSnapshot()
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return attempt, raw
}

func TestBrowserLoginUsesTrustedTLSAndPersistsReducedGrant(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	dir := filepath.Join(t.TempDir(), "profile")
	var output bytes.Buffer
	var opened string
	var waits []time.Duration
	profile, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		Output: &output,
		OpenURL: func(value string) error {
			opened = value
			h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
			return nil
		},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return h.advance(delay)
		},
	})
	if err != nil {
		t.Fatal("browser login failed", err)
	}
	if opened != testBrowserOrigin+"/auth/device" || len(waits) != 1 || waits[0] != 5*time.Second {
		t.Fatalf("trusted URL or initial poll interval was not honored: URL=%q waits=%v", opened, waits)
	}
	if !strings.Contains(output.String(), "Code: "+fixtures.UserCode) || !strings.Contains(output.String(), "Verification URL: "+opened) {
		t.Fatal("human instructions omitted the exact code or verification URL")
	}
	if profile.Version != browserProfileVersion || profile.Method != browserLoginMethod || profile.Config.LifetimeSeconds != 300 || !equalCaps(profile.Config.Capabilities, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}) || profile.Config.ProviderOrigin != "" || profile.Config.PublishableKey != "" {
		t.Fatal("profile did not retain the validated reduced browser grant")
	}
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded == nil || loaded.RequestID != profile.RequestID || loaded.Config.LifetimeSeconds != 300 {
		t.Fatal("active browser profile did not load", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = h.client.Identity(context.Background(), loaded); err != nil {
		t.Fatal("active browser credential failed GetIdentity", err)
	}
	if _, err = os.Stat(filepath.Join(dir, browserAttemptFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed browser intent was not removed", err)
	}
}

func TestBrowserLoginNoBrowserLeavesCrossDeviceInstructions(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	dir := filepath.Join(t.TempDir(), "profile")
	var output bytes.Buffer
	opened := false
	profile, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		NoBrowser: true, Output: &output,
		OpenURL: func(string) error { opened = true; return nil },
		Sleep: func(ctx context.Context, delay time.Duration) error {
			h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
			return h.advance(delay)
		},
	})
	if err != nil || opened || profile == nil {
		t.Fatal("--no-browser did not complete without an opener", err)
	}
	if strings.Contains(output.String(), "localhost") || !strings.Contains(output.String(), "Verification URL: "+testBrowserOrigin+"/auth/device") || !strings.Contains(output.String(), "Code: "+fixtures.UserCode) {
		t.Fatal("cross-device instructions were missing or used a local callback")
	}
}

type browserTransport struct {
	base             http.RoundTripper
	dropBegin        atomic.Int32
	dropPoll         atomic.Int32
	wrongPollAuth    atomic.Bool
	beginCalls       atomic.Int32
	retryAfterHeader string
}

func (r *browserTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/BeginLogin") {
		r.beginCalls.Add(1)
	}
	if strings.HasSuffix(request.URL.Path, "/PollLogin") && r.wrongPollAuth.Load() {
		request.Header.Set("Authorization", "SyntraLogin intentionally-wrong-proof")
	}
	response, err := r.base.RoundTrip(request)
	if err != nil {
		return response, err
	}
	if strings.HasSuffix(request.URL.Path, "/BeginLogin") && r.dropBegin.Load() > 0 && r.dropBegin.Add(-1) >= 0 {
		_ = response.Body.Close()
		return nil, errors.New("synthetic dropped Begin response")
	}
	if strings.HasSuffix(request.URL.Path, "/PollLogin") && r.dropPoll.Load() > 0 && r.dropPoll.Add(-1) >= 0 {
		_ = response.Body.Close()
		return nil, errors.New("synthetic dropped Poll response")
	}
	if strings.HasSuffix(request.URL.Path, "/PollLogin") && response.StatusCode == http.StatusTooManyRequests && r.retryAfterHeader != "" {
		response.Header.Set("Retry-After", r.retryAfterHeader)
	}
	return response, nil
}

func useBrowserTransport(t *testing.T, h *browserHarness) *browserTransport {
	t.Helper()
	transport := &browserTransport{base: h.client.http.Transport}
	h.client.http.Transport = transport
	return transport
}

func TestBrowserLoginLostBeginResponseResumesExactIntent(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	transport := useBrowserTransport(t, h)
	transport.dropBegin.Store(1)
	dir := filepath.Join(t.TempDir(), "profile")
	options := BrowserLoginOptions{NoBrowser: true, Output: ioDiscard{}, Sleep: func(ctx context.Context, delay time.Duration) error { return h.advance(delay) }}
	if _, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, options); err == nil {
		t.Fatal("lost Begin response was treated as success")
	}
	first, firstRaw := readPendingBrowser(t, dir)
	secondOptions := options
	secondOptions.OpenURL = func(string) error {
		h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
		return nil
	}
	secondOptions.NoBrowser = false
	if _, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, secondOptions); err != nil {
		t.Fatal("exact Begin retry failed to recover consent", err)
	}
	if transport.beginCalls.Load() != 2 {
		t.Fatal("restart did not replay the exact Begin request")
	}
	if _, err := os.Stat(filepath.Join(dir, browserAttemptFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovered intent remained after activation", err)
	}
	if len(firstRaw) == 0 || first.Validate() != nil {
		t.Fatal("first durable intent was invalid")
	}
}

func TestBrowserLoginLostPollResponseResumesApprovedGrant(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	h.config.LifetimeSeconds = 7200
	transport := useBrowserTransport(t, h)
	transport.dropPoll.Store(1)
	dir := filepath.Join(t.TempDir(), "profile")
	options := BrowserLoginOptions{
		Output: ioDiscard{},
		OpenURL: func(string) error {
			h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 3600)
			return nil
		},
		Sleep: func(ctx context.Context, delay time.Duration) error { return h.advance(delay) },
	}
	firstCtx, cancel := context.WithCancel(context.Background())
	waits := 0
	options.Sleep = func(ctx context.Context, delay time.Duration) error {
		waits++
		if waits == 2 {
			cancel()
			return ctx.Err()
		}
		return h.advance(delay)
	}
	if _, err := h.client.RunBrowserLogin(firstCtx, dir, h.config, options); err == nil || !strings.Contains(err.Error(), "interrupted") {
		cancel()
		t.Fatal("interrupted Poll retry did not preserve the approved attempt", err)
	}
	cancel()
	first, _ := readPendingBrowser(t, dir)
	h.now.Add(int64(11 * time.Minute))
	options.Sleep = func(ctx context.Context, delay time.Duration) error { return h.advance(delay) }
	profile, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, options)
	if err != nil || profile == nil {
		t.Fatal("approved grant was not recovered after restart", err)
	}
	if profile.RequestID != first.RequestID || transport.beginCalls.Load() != 2 {
		t.Fatal("approved recovery changed the retained request")
	}
}

func TestBrowserLoginHonorsRetryAfterAndMinimumInitialInterval(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	dir := filepath.Join(t.TempDir(), "profile")
	var waits []time.Duration
	approved := false
	profile, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		NoBrowser: true, Output: ioDiscard{},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			if len(waits) == 2 && !approved {
				h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
				approved = true
			}
			step := delay
			if len(waits) < 3 {
				step = time.Second
			}
			return h.advance(step)
		},
	})
	if err != nil || profile == nil {
		t.Fatal("throttled browser polling did not recover", err)
	}
	if len(waits) != 3 || waits[0] != 5*time.Second || waits[1] != 5*time.Second || waits[2] != 10*time.Second {
		t.Fatalf("initial interval or Retry-After was ignored: %v", waits)
	}
}

func TestBrowserLoginShortRetryAfterDoesNotReduceBackoff(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	transport := useBrowserTransport(t, h)
	transport.retryAfterHeader = "1"
	dir := filepath.Join(t.TempDir(), "profile")
	var waits []time.Duration
	approved := false
	profile, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		NoBrowser: true, Output: ioDiscard{},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			if len(waits) == 2 && !approved {
				h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
				approved = true
			}
			step := delay
			if len(waits) < 4 {
				step = time.Second
			}
			return h.advance(step)
		},
	})
	if err != nil || profile == nil {
		t.Fatal("browser polling did not recover from a short Retry-After", err)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("unexpected number of poll waits: got %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("short Retry-After reduced polling backoff: got %v, want %v", waits, want)
		}
	}
}

func TestRetryAfterBackoffNeverShortensCurrentDelay(t *testing.T) {
	tests := []struct {
		name    string
		current time.Duration
		header  string
		want    time.Duration
	}{
		{name: "small header backs off", current: 10 * time.Second, header: "1", want: 15 * time.Second},
		{name: "equal header still backs off", current: 10 * time.Second, header: "10", want: 15 * time.Second},
		{name: "long header is a lower bound", current: 10 * time.Second, header: "30", want: 30 * time.Second},
		{name: "missing header backs off", current: 10 * time.Second, want: 15 * time.Second},
		{name: "invalid header backs off", current: 10 * time.Second, header: "soon", want: 15 * time.Second},
		{name: "local backoff caps at one minute", current: 55 * time.Second, header: "1", want: 60 * time.Second},
		{name: "local cap remains stable", current: 60 * time.Second, want: 60 * time.Second},
		{name: "maximum supported header is accepted", current: 60 * time.Second, header: "86400", want: 24 * time.Hour},
		{name: "header above supported bound uses fallback", current: 10 * time.Second, header: "86401", want: 15 * time.Second},
		{name: "short header preserves prior long wait", current: 120 * time.Second, header: "1", want: 120 * time.Second},
		{name: "missing header preserves prior long wait", current: 120 * time.Second, want: 120 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := connect.NewError(connect.CodeResourceExhausted, errors.New("synthetic throttling"))
			if tt.header != "" {
				err.Meta().Set("Retry-After", tt.header)
			}
			if got := retryAfter(err, tt.current); got != tt.want {
				t.Fatalf("retryAfter(%q, %s) = %s, want %s", tt.header, tt.current, got, tt.want)
			}
		})
	}
}

func TestBrowserLoginRejectsUntrustedVerificationOrigin(t *testing.T) {
	const evil = "https://evil.example.test/auth/device"
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: evil}, nil)
	dir := filepath.Join(t.TempDir(), "profile")
	var output bytes.Buffer
	opened := false
	_, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		Output: &output, OpenURL: func(string) error { opened = true; return nil },
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err == nil || opened || strings.Contains(output.String(), evil) {
		t.Fatal("untrusted verification origin was shown or opened")
	}
	readPendingBrowser(t, dir)
}

type browserRedirectTransport struct {
	base       http.RoundTripper
	location   string
	redirected atomic.Bool
}

func (r *browserRedirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/PollLogin") && r.redirected.CompareAndSwap(false, true) {
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Status:     "307 Temporary Redirect",
			Header:     http.Header{"Location": []string{r.location}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	}
	return r.base.RoundTrip(request)
}

func TestBrowserPollRedirectDoesNotForwardProofToOtherOrigin(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("poll proof was forwarded across redirect")
		}
	}))
	t.Cleanup(target.Close)
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	h.client.http.Transport = &browserRedirectTransport{base: h.client.http.Transport, location: target.URL + "/collect"}
	dir := filepath.Join(t.TempDir(), "profile")
	profile, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		Output: ioDiscard{}, OpenURL: func(string) error {
			h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
			return nil
		},
		Sleep: func(ctx context.Context, delay time.Duration) error { return h.advance(delay) },
	})
	if err != nil || profile == nil || targetRequests.Load() != 0 {
		t.Fatal("Poll redirect was followed or blocked safe same-origin retry", err)
	}
}

func TestBrowserLoginRetainsDeniedAndExpiredAttempts(t *testing.T) {
	for name, interaction := range map[string]func(*testing.T, *browserHarness){
		"denied":  func(t *testing.T, h *browserHarness) { h.deny(t) },
		"expired": func(_ *testing.T, h *browserHarness) { h.now.Add(int64(11 * time.Minute)) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
			dir := filepath.Join(t.TempDir(), "profile")
			_, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
				Output: ioDiscard{}, OpenURL: func(string) error { interaction(t, h); return nil },
				Sleep: func(ctx context.Context, delay time.Duration) error { return h.advance(delay) },
			})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), name) {
				t.Fatal("terminal browser state was not reported", err)
			}
			readPendingBrowser(t, dir)
		})
	}
}

func TestBrowserLoginDisabledRouteSuggestsOTPAndRetainsIntent(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(server.URL, roots)
	if err != nil {
		t.Fatal(err)
	}
	config := BrowserConfig{APIOrigin: server.URL, BrowserOrigin: testBrowserOrigin, Label: "Disabled route", Capabilities: []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, LifetimeSeconds: 600}
	dir := filepath.Join(t.TempDir(), "profile")
	_, err = client.RunBrowserLogin(context.Background(), dir, config, BrowserLoginOptions{NoBrowser: true, Output: ioDiscard{}, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "--method otp") {
		t.Fatal("disabled ToolLogin route did not direct the human to explicit OTP login", err)
	}
	readPendingBrowser(t, dir)
}

func TestBrowserLoginUnsafePendingPathSendsNoBegin(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	transport := useBrowserTransport(t, h)
	dir := filepath.Join(t.TempDir(), "profile")
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, browserAttemptFilename)); err != nil {
		if symlinkPermissionUnavailable(err) {
			t.Skipf("creating a Windows symlink requires permission: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{Output: ioDiscard{}}); err == nil {
		t.Fatal("symlink pending intent was accepted")
	}
	if transport.beginCalls.Load() != 0 {
		t.Fatal("Begin was sent before intent persistence was proven safe")
	}
}

func TestBrowserBeginRequiresExactServerCodeAlphabet(t *testing.T) {
	config := browserConfig("https://api.example", []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 600)
	config.BrowserOrigin = testBrowserOrigin
	attempt, err := NewPendingBrowserAttempt(config)
	if err != nil {
		t.Fatal(err)
	}
	request, err := attempt.BeginRequest()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := attempt.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"BCDFGHJK", "ABCD0FGH", "ABCDEFGI", "abcdef12", "ABCDEFGH9"} {
		response := &v1.BeginLoginResponse{
			Request: &v1.ToolLoginRequest{
				RequestId: request.RequestId, UserCode: code, Label: request.Label,
				Capabilities: request.Capabilities, ExpiresInSeconds: request.ExpiresInSeconds,
				Fingerprint: fingerprint, State: v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING,
				ExpiresAt: timestamppb.New(time.Now().Add(10 * time.Minute)),
			},
			IntervalSeconds: 5,
		}
		gotErr := validateBeginResponse(attempt, response)
		if (gotErr == nil) != (code == "BCDFGHJK") {
			t.Fatalf("unexpected code validation for %q: %v", code, gotErr)
		}
	}
}

func saveExistingActiveProfile(t *testing.T, h *browserHarness, dir, owner string, caps []v1.ToolCapability, lifetime int32) []byte {
	t.Helper()
	config := Config{
		APIOrigin: h.client.origin, ProviderOrigin: "https://provider.example",
		PublishableKey: "sb_publishable_fixtureOnlyPublicKey", Label: h.config.Label,
		Capabilities: append([]v1.ToolCapability(nil), caps...), LifetimeSeconds: lifetime,
	}
	profile, err := NewPending(config, owner)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, h.now.Load()).UTC().Truncate(time.Second)
	profile.State = "active"
	profile.ConnectionID = "44444444-4444-4444-8444-444444444444"
	profile.CreatedAt = now.Format(time.RFC3339Nano)
	profile.ExpiresAt = now.Add(time.Duration(lifetime) * time.Second).Format(time.RFC3339Nano)
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(profile); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBrowserReauthorizationExpandsScopeOnlyWithConsent(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	transport := useBrowserTransport(t, h)
	dir := filepath.Join(t.TempDir(), "profile")
	before := saveExistingActiveProfile(t, h, dir, "33333333-3333-4333-8333-333333333333", []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, h.config.LifetimeSeconds)
	var output bytes.Buffer
	got, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		Reauthorize: true, Output: &output,
		OpenURL: func(string) error {
			if !bytes.Equal(before, mustReadFile(t, filepath.Join(dir, "profile.json"))) {
				t.Fatal("active profile changed before browser consent")
			}
			h.approve(t, h.config.Capabilities, h.config.LifetimeSeconds)
			return nil
		},
		Sleep: func(_ context.Context, delay time.Duration) error { return h.advance(delay) },
	})
	if err != nil || !equalCaps(got.Config.Capabilities, h.config.Capabilities) || transport.beginCalls.Load() != 1 {
		t.Fatal("approved permission expansion was not activated", err)
	}
	if !strings.Contains(output.String(), "Requested permissions: read, prepare") {
		t.Fatal("missing requested permission preview")
	}
	if _, err = os.Stat(filepath.Join(dir, browserAttemptFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed consent left a pending attempt", err)
	}
}
func TestBrowserReauthorizationPreservesProfileOnIdentitySubstitution(t *testing.T) {
	mutations := map[string]func(*v1.ToolConnection){
		"owner": func(connection *v1.ToolConnection) { connection.OwnerUserId = "55555555-5555-4555-8555-555555555555" },
		"revoked": func(connection *v1.ToolConnection) {
			connection.State = v1.ToolConnectionState_TOOL_CONNECTION_STATE_REVOKED
			connection.RevokedAt = timestamppb.Now()
		},
		"label": func(connection *v1.ToolConnection) { connection.Label = "substituted label" },
		"capabilities": func(connection *v1.ToolConnection) {
			connection.Capabilities = []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			options := fixtures.TestHarnessOptions{
				AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device",
				IdentityMetadataTransform: func(connection *v1.ToolConnection) *v1.ToolConnection { mutate(connection); return connection },
			}
			h := newBrowserHarness(t, options, nil)
			dir := filepath.Join(t.TempDir(), "profile")
			before := saveExistingActiveProfile(t, h, dir, "33333333-3333-4333-8333-333333333333", h.config.Capabilities, h.config.LifetimeSeconds)
			_, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
				Reauthorize: true, Output: ioDiscard{},
				OpenURL: func(string) error { h.approve(t, h.config.Capabilities, h.config.LifetimeSeconds); return nil },
				Sleep:   func(ctx context.Context, delay time.Duration) error { return h.advance(delay) },
			})
			if err == nil {
				t.Fatal("substituted GetIdentity metadata was accepted")
			}
			if !bytes.Equal(before, mustReadFile(t, filepath.Join(dir, "profile.json"))) {
				t.Fatal("identity mismatch changed the old active profile")
			}
			readPendingBrowser(t, dir)
		})
	}
}

func TestBrowserReauthorizationCannotChangeExistingOwner(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	dir := filepath.Join(t.TempDir(), "profile")
	before := saveExistingActiveProfile(t, h, dir, "11111111-1111-4111-8111-111111111111", h.config.Capabilities, h.config.LifetimeSeconds)
	_, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		Reauthorize: true, Output: ioDiscard{},
		OpenURL: func(string) error { h.approve(t, h.config.Capabilities, h.config.LifetimeSeconds); return nil },
		Sleep:   func(ctx context.Context, delay time.Duration) error { return h.advance(delay) },
	})
	if err == nil || !strings.Contains(err.Error(), "change account") {
		t.Fatal("reauthorization changed active account", err)
	}
	if !bytes.Equal(before, mustReadFile(t, filepath.Join(dir, "profile.json"))) {
		t.Fatal("account mismatch changed the old active profile")
	}
	readPendingBrowser(t, dir)
}

type expandingPollFixture struct{ *fixtures.DeviceFixture }

func (f *expandingPollFixture) PollLogin(ctx context.Context, request *connect.Request[v1.PollLoginRequest]) (*connect.Response[v1.PollLoginResponse], error) {
	response, err := f.DeviceFixture.PollLogin(ctx, request)
	if err == nil && response.Msg.State == v1.ToolLoginState_TOOL_LOGIN_STATE_APPROVED && response.Msg.Connection != nil {
		response.Msg.Connection.Capabilities = []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ, v1.ToolCapability_TOOL_CAPABILITY_MARK_READ}
	}
	return response, err
}

func TestBrowserLoginRejectsExpandedPollGrantBeforeIdentity(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, func(fixture *fixtures.DeviceFixture) http.Handler {
		loginPath, loginHandler := syntrav1connect.NewToolLoginServiceHandler(&expandingPollFixture{DeviceFixture: fixture})
		connectionPath, connectionHandler := syntrav1connect.NewToolConnectionServiceHandler(fixture)
		mux := http.NewServeMux()
		mux.Handle(loginPath, loginHandler)
		mux.Handle(connectionPath, connectionHandler)
		return mux
	})
	dir := filepath.Join(t.TempDir(), "profile")
	before := saveExistingActiveProfile(t, h, dir, "33333333-3333-4333-8333-333333333333", h.config.Capabilities, h.config.LifetimeSeconds)
	_, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		Reauthorize: true, Output: ioDiscard{},
		OpenURL: func(string) error {
			h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
			return nil
		},
		Sleep: func(ctx context.Context, delay time.Duration) error { return h.advance(delay) },
	})
	if err == nil || !strings.Contains(err.Error(), "expanded requested scope") {
		t.Fatal("expanded Poll grant was accepted", err)
	}
	if !bytes.Equal(before, mustReadFile(t, filepath.Join(dir, "profile.json"))) {
		t.Fatal("expanded Poll grant changed the existing profile")
	}
	readPendingBrowser(t, dir)
}

func TestBrowserPollRejectsWrongProofWithoutLeakingCredentials(t *testing.T) {
	h := newBrowserHarness(t, fixtures.TestHarnessOptions{AcceptFirstValidRequestID: true, VerificationURI: testBrowserOrigin + "/auth/device"}, nil)
	transport := useBrowserTransport(t, h)
	transport.wrongPollAuth.Store(true)
	dir := filepath.Join(t.TempDir(), "profile")
	_, err := h.client.RunBrowserLogin(context.Background(), dir, h.config, BrowserLoginOptions{
		Output: ioDiscard{},
		OpenURL: func(string) error {
			h.approve(t, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
			return nil
		},
		Sleep: func(ctx context.Context, delay time.Duration) error { return h.advance(delay) },
	})
	if err == nil || strings.Contains(err.Error(), "intentionally-wrong-proof") {
		t.Fatal("wrong Poll proof was accepted or reflected in the error", err)
	}
	attempt, _ := readPendingBrowser(t, dir)
	if strings.Contains(err.Error(), attempt.ToolSecret) || strings.Contains(err.Error(), attempt.PollProof) {
		t.Fatal("browser error exposed a retained credential")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
