//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
	"syntra.local/mcp/internal/toolclient"
)

const fixtureEmail = "human-fixture@example.invalid"
const fixtureCode = "739126"
const fixtureJWT = "fixture-human-access-secret"
const fixtureRefresh = "fixture-human-refresh-secret"
const fixtureAccount = "11111111-1111-4111-8111-111111111111"

type cliFixture struct {
	syntrav1connect.UnimplementedToolConnectionServiceHandler
	syntrav1connect.UnimplementedUserServiceHandler
	mu         sync.Mutex
	t          *testing.T
	dir        string
	owner      string
	otpStatus  int
	otpCalls   atomic.Int32
	issueCalls atomic.Int32
	request    *v1.HumanCreateRequest
	connection *v1.ToolConnection
	holdFirst  bool
	committed  chan struct{}
	mutate     string
	redirect   string
	server     *httptest.Server
	ca         string
}

func newFixture(t *testing.T) *cliFixture {
	t.Helper()
	root := t.TempDir()
	f := &cliFixture{t: t, dir: root + "/profile", owner: fixtureAccount, committed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(syntrav1connect.NewToolConnectionServiceHandler(f))
	mux.Handle(syntrav1connect.NewUserServiceHandler(f))
	mux.HandleFunc("/auth/v1/otp", func(w http.ResponseWriter, r *http.Request) {
		f.otpCalls.Add(1)
		var p map[string]any
		if json.NewDecoder(r.Body).Decode(&p) != nil || len(p) != 2 || p["email"] != fixtureEmail || p["create_user"] != false || r.Header.Get("apikey") != "sb_publishable_fixtureOnlyPublicKey" {
			t.Error("invalid managed OTP/no-signup payload")
			w.WriteHeader(400)
			return
		}
		if f.redirect != "" {
			http.Redirect(w, r, f.redirect, 307)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	})
	mux.HandleFunc("/auth/v1/verify", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if json.NewDecoder(r.Body).Decode(&p) != nil || len(p) != 3 || p["email"] != fixtureEmail || p["token"] != fixtureCode || p["type"] != "email" {
			t.Error("invalid managed OTP verify payload")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if f.otpStatus != 0 {
			w.WriteHeader(f.otpStatus)
			fmt.Fprint(w, "provider-body-private "+fixtureJWT+" "+fixtureRefresh)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"token_type":"bearer","user":{"id":"22222222-2222-4222-8222-222222222222"}}`, fixtureJWT, fixtureRefresh)
	})
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	f.ca = root + "/ca.pem"
	if e := os.WriteFile(f.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *cliFixture) GetMe(context.Context, *connect.Request[v1.GetMeRequest]) (*connect.Response[v1.GetMeResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&v1.GetMeResponse{Profile: &v1.UserProfile{Id: f.owner}, Identity: &v1.SelfIdentity{ProviderSubject: "22222222-2222-4222-8222-222222222222"}}), nil
}
func (f *cliFixture) HumanCreate(ctx context.Context, r *connect.Request[v1.HumanCreateRequest]) (*connect.Response[v1.HumanCreateResponse], error) {
	count := f.issueCalls.Add(1)
	raw, e := os.ReadFile(f.dir + "/profile.json")
	if e != nil {
		f.t.Error("pending absent before HTTP issuance")
		return nil, connect.NewError(connect.CodeInternal, errors.New("pending absent"))
	}
	var p toolclient.Profile
	if json.Unmarshal(raw, &p) != nil || p.State != "pending" || p.Validate() != nil {
		f.t.Error("invalid durable pending")
		return nil, connect.NewError(connect.CodeInternal, errors.New("invalid pending"))
	}
	digest := sha256.Sum256([]byte(p.Secret))
	if !bytes.Equal(digest[:], r.Msg.SecretDigest) || r.Msg.RequestId != p.RequestID || r.Header().Get("Authorization") != "Bearer "+fixtureJWT {
		f.t.Error("not exact digest-only human issuance")
	}
	f.mu.Lock()
	if f.request == nil {
		f.request = proto.Clone(r.Msg).(*v1.HumanCreateRequest)
		now := time.Now().UTC()
		f.connection = &v1.ToolConnection{Id: "33333333-3333-4333-8333-333333333333", OwnerUserId: f.owner, Label: r.Msg.Label, Capabilities: r.Msg.Capabilities, CreatedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(time.Duration(r.Msg.ExpiresInSeconds) * time.Second)), State: 1}
	} else if !proto.Equal(f.request, r.Msg) {
		f.t.Error("restart changed exact issuance payload")
	}
	out := proto.Clone(f.connection).(*v1.ToolConnection)
	f.mu.Unlock()
	if f.mutate == "owner" {
		out.OwnerUserId = "22222222-2222-4222-8222-222222222222"
	}
	if f.mutate == "caps" {
		out.Capabilities = append(out.Capabilities, 2)
	}
	if count == 1 {
		close(f.committed)
	}
	if f.holdFirst && count == 1 {
		<-ctx.Done()
		return nil, connect.NewError(connect.CodeCanceled, errors.New("lost reply"))
	}
	return connect.NewResponse(&v1.HumanCreateResponse{Connection: out}), nil
}
func (f *cliFixture) GetIdentity(_ context.Context, r *connect.Request[v1.GetIdentityRequest]) (*connect.Response[v1.GetIdentityResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connection == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("fixture"))
	}
	raw, _ := os.ReadFile(f.dir + "/profile.json")
	var p toolclient.Profile
	json.Unmarshal(raw, &p)
	if r.Header().Get("Authorization") != "Bearer "+p.Secret {
		f.t.Error("identity did not use retained tool secret")
	}
	return connect.NewResponse(&v1.GetIdentityResponse{Connection: proto.Clone(f.connection).(*v1.ToolConnection)}), nil
}
func (f *cliFixture) Disconnect(_ context.Context, _ *connect.Request[v1.DisconnectRequest]) (*connect.Response[v1.DisconnectResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := proto.Clone(f.connection).(*v1.ToolConnection)
	q.State = 2
	q.RevokedAt = timestamppb.Now()
	return connect.NewResponse(&v1.DisconnectResponse{Connection: q}), nil
}

func buildCLI(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bin := root + "/syntra"
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(fixtureEnvironment(), "XDG_CONFIG_HOME="+root)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("CLI build failed: %v %s", e, out)
	}
	return bin
}
func (f *cliFixture) args() []string {
	return []string{"login", "--method", "otp", "--profile-dir", f.dir, "--api", f.server.URL, "--provider", f.server.URL, "--publishable-key", "sb_publishable_fixtureOnlyPublicKey", "--lifetime-seconds", "300", "--timeout", "20s"}
}
func (f *cliFixture) env() []string {
	root := filepath.Dir(f.dir)
	return append(fixtureEnvironment(), "XDG_CONFIG_HOME="+root, "SSL_CERT_FILE="+f.ca, "SSL_CERT_DIR="+root+"/no-cert-dir")
}

type processPTY struct {
	cmd           *exec.Cmd
	master, slave *os.File
	chunks        chan []byte
	done          chan error
	output        []byte
	readerDone    chan struct{}
	processDone   chan struct{}
	readerErr     error
	echoState     *unix.Termios
	echoErr       error
}

func startPTY(t *testing.T, bin string, env, args []string) *processPTY {
	return startPTYWithDeliveryGate(t, bin, env, args, nil)
}

// An optional per-process delivery gate lets the fault probe force output
// delivery after Wait, without changing the child's real terminal writes.
func startPTYWithDeliveryGate(t *testing.T, bin string, env, args []string, gate <-chan struct{}) *processPTY {
	t.Helper()
	fd, e := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if e != nil {
		t.Fatal(e)
	}
	master := os.NewFile(uintptr(fd), "fixture PTY master")
	if e = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); e != nil {
		t.Fatal(e)
	}
	n, e := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if e != nil {
		t.Fatal(e)
	}
	slave, e := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	p := &processPTY{cmd: cmd, master: master, slave: slave, chunks: make(chan []byte, 128), done: make(chan error, 1), readerDone: make(chan struct{}), processDone: make(chan struct{})}
	go func() {
		defer close(p.readerDone)
		defer close(p.chunks)
		buf := make([]byte, 4096)
		for {
			n, e := master.Read(buf)
			if n > 0 {
				if gate != nil {
					<-gate
				}
				p.chunks <- append([]byte(nil), buf[:n]...)
			}
			if e != nil {
				p.readerErr = e
				return
			}
		}
	}()
	go func() { p.done <- cmd.Wait(); close(p.processDone) }()
	t.Cleanup(func() {

		select {
		case <-p.processDone:
		default:
			cmd.Process.Kill()
			select {
			case <-p.processDone:
			case <-time.After(time.Second):
			}
		}

		master.Close()
		slave.Close()
	})
	return p
}
func (p *processPTY) waitText(t *testing.T, text string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !bytes.Contains(p.output, []byte(text)) {
		select {
		case b, ok := <-p.chunks:
			if !ok {
				t.Fatal("PTY closed before expected prompt")
			}
			p.output = append(p.output, b...)
		case <-deadline:
			t.Fatal("PTY prompt deadline", text)
		}
	}
}
func (p *processPTY) enter(t *testing.T, text string) {
	t.Helper()
	if _, e := p.master.Write([]byte(text + "\n")); e != nil {
		t.Fatal(e)
	}
}
func (p *processPTY) login(t *testing.T) {
	p.waitText(t, "Email: ")
	p.enter(t, fixtureEmail)
	p.waitText(t, "Email OTP (hidden): ")
	p.enter(t, fixtureCode)
}

func (p *processPTY) finish(t *testing.T, wantSuccess bool) {
	t.Helper()
	var exitErr error
	select {
	case exitErr = <-p.done:
	case <-time.After(12 * time.Second):
		t.Fatal("CLI exit deadline")
	}
	// The parent slave would prevent EOF/HUP forever. Capture its state first for
	// later echo assertions, then close it so the master can reach complete EOF/EIO.
	p.echoState, p.echoErr = unix.IoctlGetTermios(int(p.slave.Fd()), unix.TCGETS)
	p.slave.Close()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case chunk, ok := <-p.chunks:
			if !ok {
				goto drained
			}
			p.output = append(p.output, chunk...)
			if len(p.output) > 2*1048576 {
				t.Fatal("PTY output bound exceeded")
			}
		case <-deadline:
			t.Fatal("PTY complete output deadline")
		}
	}
drained:
	select {
	case <-p.readerDone:
	case <-deadline:
		t.Fatal("PTY reader completion deadline")
	}
	if !errors.Is(p.readerErr, io.EOF) && !errors.Is(p.readerErr, unix.EIO) {
		t.Fatal("unexpected PTY reader failure")
	}
	if (exitErr == nil) != wantSuccess {
		t.Fatalf("unexpected CLI exit success=%v", exitErr == nil)
	}
}
func (p *processPTY) assertEcho(t *testing.T) {
	t.Helper()
	if p.echoErr != nil || p.echoState == nil || p.echoState.Lflag&unix.ECHO == 0 {
		t.Fatal("terminal echo not restored")
	}
}
func assertRedacted(t *testing.T, out []byte, p *toolclient.Profile) {
	t.Helper()
	for _, s := range []string{fixtureCode, fixtureJWT, fixtureRefresh, "provider-body-private"} {
		if bytes.Contains(out, []byte(s)) {
			t.Fatal("sensitive synthetic value leaked to terminal")
		}
	}
	if p != nil && bytes.Contains(out, []byte(p.Secret)) {
		t.Fatal("tool credential leaked")
	}
}
func readProfile(t *testing.T, dir string) *toolclient.Profile {
	t.Helper()
	raw, e := os.ReadFile(dir + "/profile.json")
	if e != nil {
		t.Fatal(e)
	}
	var p toolclient.Profile
	if json.Unmarshal(raw, &p) != nil || p.Validate() != nil {
		t.Fatal("invalid persisted profile")
	}
	for _, secret := range []string{fixtureCode, fixtureJWT, fixtureRefresh, fixtureEmail, "access_token", "refresh_token", "provider_subject"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("human session/OTP persisted")
		}
	}
	return &p
}

func TestCompiledCLIHumanTTYAndProcessRecovery(t *testing.T) {
	bin := buildCLI(t)
	t.Run("valid_login_identity_status_disconnect", func(t *testing.T) {
		f := newFixture(t)
		p := startPTY(t, bin, f.env(), f.args())
		p.login(t)
		p.finish(t, true)
		p.assertEcho(t)
		profile := readProfile(t, f.dir)
		if profile.State != "active" || profile.AccountID != fixtureAccount || len(profile.Config.Capabilities) != 1 {
			t.Fatal("bad active profile")
		}
		assertRedacted(t, p.output, profile)
		for _, command := range []string{"status", "identity", "disconnect"} {
			cmd := exec.Command(bin, command, "--profile-dir", f.dir)
			cmd.Env = f.env()
			out, e := cmd.CombinedOutput()
			if e != nil {
				t.Fatal("connection command failed", command)
			}
			assertRedacted(t, out, profile)
		}
		if _, e := os.Stat(f.dir + "/profile.json"); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("disconnect retained profile")
		}
	})
	t.Run("default_browser_login_reuses_matching_v1_profile", func(t *testing.T) {
		f := newFixture(t)
		p := startPTY(t, bin, f.env(), f.args())
		p.login(t)
		p.finish(t, true)
		before, err := os.ReadFile(filepath.Join(f.dir, "profile.json"))
		if err != nil {
			t.Fatal(err)
		}
		const journalID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		journal := []byte(`{"fixture":"retained mutation"}`)
		journalDigest := sha256.Sum256(journal)
		journalPath := filepath.Join(f.dir, "attempt-"+journalID+".json")
		anchorPath := filepath.Join(f.dir, "attempt-"+journalID+".sha256")
		if err = os.WriteFile(journalPath, journal, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(anchorPath, journalDigest[:], 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "login", "--profile-dir", f.dir, "--api", f.server.URL)
		cmd.Env = f.env()
		output, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("Connected account")) {
			t.Fatalf("matching legacy profile was not verified and reused: %v %s", err, output)
		}
		after, err := os.ReadFile(filepath.Join(f.dir, "profile.json"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("browser-default login rewrote an equivalent active v1 profile", err)
		}
		journalAfter, journalErr := os.ReadFile(journalPath)
		anchorAfter, anchorErr := os.ReadFile(anchorPath)
		if journalErr != nil || anchorErr != nil || !bytes.Equal(journal, journalAfter) || !bytes.Equal(journalDigest[:], anchorAfter) {
			t.Fatal("browser-default login changed an existing mutation journal", journalErr, anchorErr)
		}
		if f.otpCalls.Load() != 1 || f.issueCalls.Load() != 1 {
			t.Fatal("matching profile reuse triggered a new login or issuance")
		}
	})
	t.Run("default_profile_is_shared", func(t *testing.T) {
		f := newFixture(t)
		args := f.args()
		args = append(args[:3], args[5:]...)
		f.dir = filepath.Dir(f.dir) + "/syntra"
		p := startPTY(t, bin, f.env(), args)
		p.login(t)
		p.finish(t, true)
		profile := readProfile(t, f.dir)
		cmd := exec.Command(bin, "status")
		cmd.Env = f.env()
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal("default profile not reusable")
		}
		assertRedacted(t, out, profile)
	})
	for _, status := range []int{400, 403} {
		t.Run(fmt.Sprintf("invalid_or_expired_OTP_%d", status), func(t *testing.T) {
			f := newFixture(t)
			f.otpStatus = status
			p := startPTY(t, bin, f.env(), f.args())
			p.login(t)
			p.finish(t, false)
			p.assertEcho(t)
			assertRedacted(t, p.output, nil)
			if f.issueCalls.Load() != 0 {
				t.Fatal("issued after rejected OTP")
			}
			if _, e := os.Stat(f.dir + "/profile.json"); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("persisted rejected session")
			}
		})
	}
	t.Run("no_TTY_no_OTP", func(t *testing.T) {
		f := newFixture(t)
		cmd := exec.Command(bin, f.args()...)
		cmd.Env = f.env()
		cmd.Stdin = strings.NewReader(fixtureCode + "\n")
		out, e := cmd.CombinedOutput()
		if e == nil || f.otpCalls.Load() != 0 || f.issueCalls.Load() != 0 {
			t.Fatal("noninteractive login reached network")
		}
		assertRedacted(t, out, nil)
	})
	t.Run("crash_after_issuance_recovers_same_pending", func(t *testing.T) {
		f := newFixture(t)
		f.holdFirst = true
		p := startPTY(t, bin, f.env(), f.args())
		p.login(t)
		select {
		case <-f.committed:
		case <-time.After(10 * time.Second):
			t.Fatal("no issuance")
		}
		before := readProfile(t, f.dir)
		if before.State != "pending" {
			t.Fatal("pending not durable")
		}
		p.cmd.Process.Kill()
		p.finish(t, false)
		retry := startPTY(t, bin, f.env(), f.args())
		retry.login(t)
		retry.finish(t, true)
		after := readProfile(t, f.dir)
		if after.Secret != before.Secret || after.RequestID != before.RequestID || after.State != "active" || f.issueCalls.Load() != 2 {
			t.Fatal("crash replaced issuance")
		}
		assertRedacted(t, retry.output, after)
	})
	t.Run("cross_process_lock_deadline_and_release", func(t *testing.T) {
		f := newFixture(t)
		first := startPTY(t, bin, f.env(), f.args())
		first.waitText(t, "Email: ")
		cmd := exec.Command(bin, "status", "--profile-dir", f.dir, "--timeout", "100ms")
		cmd.Env = f.env()
		if out, e := cmd.CombinedOutput(); e == nil || !bytes.Contains(out, []byte("profile lock deadline")) {
			t.Fatal("second process bypassed lock")
		}
		first.cmd.Process.Signal(syscall.SIGTERM)
		first.finish(t, false)
		first.assertEcho(t)
		second := startPTY(t, bin, f.env(), f.args())
		second.login(t)
		second.finish(t, true)
	})
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(fmt.Sprintf("signal_%d_restores_OTP_echo", signal), func(t *testing.T) {
			f := newFixture(t)
			p := startPTY(t, bin, f.env(), f.args())
			p.waitText(t, "Email: ")
			p.enter(t, fixtureEmail)
			p.waitText(t, "Email OTP (hidden): ")
			p.cmd.Process.Signal(signal)
			p.finish(t, false)
			p.assertEcho(t)
			if f.issueCalls.Load() != 0 {
				t.Fatal("issued after cancellation")
			}
		})
	}
	t.Run("OTP_deadline_restores_echo", func(t *testing.T) {
		f := newFixture(t)
		args := f.args()
		args[len(args)-1] = "700ms"
		p := startPTY(t, bin, f.env(), args)
		p.waitText(t, "Email: ")
		p.enter(t, fixtureEmail)
		p.waitText(t, "Email OTP (hidden): ")
		p.finish(t, false)
		p.assertEcho(t)
	})
	for _, field := range []string{"owner", "caps"} {
		t.Run("metadata_"+field+"_rejects_keeps_pending", func(t *testing.T) {
			f := newFixture(t)
			f.mutate = field
			p := startPTY(t, bin, f.env(), f.args())
			p.login(t)
			p.finish(t, false)
			profile := readProfile(t, f.dir)
			if profile.State != "pending" {
				t.Fatal("activated mismatched metadata")
			}
			assertRedacted(t, p.output, profile)
		})
	}
	t.Run("pending_other_account_and_config_cannot_replace", func(t *testing.T) {
		f := newFixture(t)
		f.holdFirst = true
		p := startPTY(t, bin, f.env(), f.args())
		p.login(t)
		select {
		case <-f.committed:
		case <-time.After(10 * time.Second):
			t.Fatal("no issuance")
		}
		before := readProfile(t, f.dir)
		p.cmd.Process.Kill()
		p.finish(t, false)
		f.mu.Lock()
		f.owner = "22222222-2222-4222-8222-222222222222"
		f.mu.Unlock()
		retry := startPTY(t, bin, f.env(), f.args())
		retry.login(t)
		retry.finish(t, false)
		after := readProfile(t, f.dir)
		if after.Secret != before.Secret || f.issueCalls.Load() != 1 {
			t.Fatal("other account replaced pending")
		}
		for _, extra := range [][]string{{"--prepare"}, {"--provider", "https://other.example"}, {"--api", "https://other.example"}, {"--label", "changed"}, {"--lifetime-seconds", "600"}} {
			count := f.otpCalls.Load()
			q := startPTY(t, bin, f.env(), append(f.args(), extra...))
			q.finish(t, false)
			if f.otpCalls.Load() != count {
				t.Fatal("config mismatch reached OTP")
			}
			after = readProfile(t, f.dir)
			if after.Secret != before.Secret {
				t.Fatal("config mismatch replaced pending")
			}
		}
	})

	t.Run("Ctrl_C_key_restores_hidden_terminal", func(t *testing.T) {
		f := newFixture(t)
		p := startPTY(t, bin, f.env(), f.args())
		p.waitText(t, "Email: ")
		p.enter(t, fixtureEmail)
		p.waitText(t, "Email OTP (hidden): ")
		p.master.Write([]byte{3})
		p.finish(t, false)
		p.assertEcho(t)
		if f.issueCalls.Load() != 0 {
			t.Fatal("issued after Ctrl-C")
		}
	})
	t.Run("unsafe_files_reject_in_actual_process", func(t *testing.T) {
		for _, kind := range []string{"symlink", "fifo", "hardlink", "unsafe-mode", "unsafe-dir", "symlink-lock"} {
			t.Run(kind, func(t *testing.T) {
				f := newFixture(t)
				os.Mkdir(f.dir, 0700)
				target := f.dir + "/profile.json"
				cfg := toolclient.Config{APIOrigin: f.server.URL, ProviderOrigin: f.server.URL, PublishableKey: "sb_publishable_fixtureOnlyPublicKey", Label: "fixture", Capabilities: []v1.ToolCapability{1}, LifetimeSeconds: 300}
				profile, e := toolclient.NewPending(cfg, fixtureAccount)
				if e != nil {
					t.Fatal(e)
				}
				raw, _ := json.Marshal(profile)
				switch kind {
				case "symlink":
					os.WriteFile(filepath.Dir(f.dir)+"/target", raw, 0600)
					os.Symlink(filepath.Dir(f.dir)+"/target", target)
				case "fifo":
					unix.Mkfifo(target, 0600)
				case "hardlink":
					os.WriteFile(filepath.Dir(f.dir)+"/target", raw, 0600)
					os.Link(filepath.Dir(f.dir)+"/target", target)
				case "unsafe-mode":
					os.WriteFile(target, raw, 0644)
					// WriteFile honors umask; set and verify the unsafe fixture explicitly.
					if err := os.Chmod(target, 0644); err != nil {
						t.Fatal(err)
					}
					if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0644 {
						t.Fatal("unsafe fixture mode was not established")
					}
				case "unsafe-dir":
					os.Chmod(f.dir, 0755)
				case "symlink-lock":
					os.WriteFile(filepath.Dir(f.dir)+"/target", raw, 0600)
					os.Symlink(filepath.Dir(f.dir)+"/target", f.dir+"/.lock")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bin, "status", "--profile-dir", f.dir)
				cmd.Env = f.env()
				out, e := cmd.CombinedOutput()
				if e == nil || ctx.Err() != nil {
					t.Fatal("unsafe file accepted or blocked process")
				}
				assertRedacted(t, out, profile)
			})
		}
	})
	t.Run("invalid_capabilities_origins_and_OTP_flags_no_network", func(t *testing.T) {
		for _, extra := range [][]string{{"--publish"}, {"--api", "http://127.0.0.1"}, {"--provider", "https://user:password@example.invalid"}, {"--otp", fixtureCode}} {
			f := newFixture(t)
			p := startPTY(t, bin, f.env(), append(f.args(), extra...))
			p.finish(t, false)
			assertRedacted(t, p.output, nil)
			if f.otpCalls.Load() != 0 || f.issueCalls.Load() != 0 {
				t.Fatal("invalid configuration reached network")
			}
		}
	})
	t.Run("API_override_and_redirect_never_forward", func(t *testing.T) {
		f := newFixture(t)
		p := startPTY(t, bin, f.env(), f.args())
		p.login(t)
		p.finish(t, true)
		var calls atomic.Int32
		target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer target.Close()
		cmd := exec.Command(bin, "identity", "--profile-dir", f.dir, "--api", target.URL)
		cmd.Env = f.env()
		out, e := cmd.CombinedOutput()
		if e == nil || calls.Load() != 0 {
			t.Fatal("API override forwarded stored bearer")
		}
		assertRedacted(t, out, readProfile(t, f.dir))
		g := newFixture(t)
		g.redirect = target.URL
		q := startPTY(t, bin, g.env(), g.args())
		q.waitText(t, "Email: ")
		q.enter(t, fixtureEmail)
		q.finish(t, false)
		if calls.Load() != 0 {
			t.Fatal("redirect forwarded provider credential")
		}
		assertRedacted(t, q.output, nil)
	})
}

// Omit inherited HOME rather than reassigning a system variable. All app
// profiles and Go caches/configuration are explicitly owned fixture paths.
func fixtureEnvironment() []string {
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "HOME=") && !strings.HasPrefix(value, "XDG_CONFIG_HOME=") {
			env = append(env, value)
		}
	}
	return env
}

// This is an expected-failure harness probe, not a fabricated output slice. A
// real subprocess writes the fixture credential immediately before actual exit.
// Its PTY reader is deliberately held until AFTER process Wait has completed.
// The nested detector must fail through the same assertRedacted used by CLI tests.
func TestPTYLateSecretOutputFault(t *testing.T) {
	const modeKey = "SYNTRA_PTY_OUTPUT_FAULT"
	switch os.Getenv(modeKey) {
	case "writer":
		fmt.Fprint(os.Stderr, fixtureJWT)
		os.Exit(0)
	case "detector":
		gate := make(chan struct{})
		env := append(fixtureEnvironment(), modeKey+"=writer", "XDG_CONFIG_HOME="+t.TempDir())
		p := startPTYWithDeliveryGate(t, os.Args[0], env, []string{"-test.run=^TestPTYLateSecretOutputFault$"}, gate)
		select {
		case <-p.processDone:
		case <-time.After(5 * time.Second):
			close(gate)
			t.Fatal("fault writer did not exit")
		}
		go func() { time.Sleep(50 * time.Millisecond); close(gate) }()
		p.finish(t, true)
		assertRedacted(t, p.output, nil)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPTYLateSecretOutputFault$")
	cmd.Env = append(fixtureEnvironment(), modeKey+"=detector", "XDG_CONFIG_HOME="+t.TempDir())
	out, e := cmd.CombinedOutput()
	if e == nil || !bytes.Contains(out, []byte("sensitive synthetic value leaked to terminal")) {
		t.Fatal("late-output redaction fault was not detected for the intended reason")
	}
}

func TestAllCommandHelp(t *testing.T) {
	for _, name := range helpOrder {
		for _, args := range [][]string{{"help", name}, {name, "--help"}, {name, "-h"}} {
			if err := run(context.Background(), args); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
		}
	}
	if err := run(context.Background(), []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if err := unknownCommand("prepar"); !strings.Contains(err.Error(), `Did you mean "prepare"`) {
		t.Fatal(err)
	}
}
