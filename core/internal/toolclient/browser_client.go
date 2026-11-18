package toolclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

const DefaultBrowserAPIOrigin = "https://syntra.synapsynclabs.com"
const DefaultBrowserVerificationOrigin = "https://syntra.synapsynclabs.com"
const verificationPath = "/auth/device"

// BrowserLoginOptions contains local interaction hooks. OpenURL receives only
// a URL that has passed the retained browser-origin/path check.
type BrowserLoginOptions struct {
	NoBrowser   bool
	Reauthorize bool
	Output      io.Writer
	OpenURL     func(string) error
	Sleep       func(context.Context, time.Duration) error
}

// RunBrowserLogin coordinates short durable state operations with network work
// performed after releasing the profile lock. The pending intent is saved and
// directory-synced before BeginLogin is sent.
func (c *Client) RunBrowserLogin(ctx context.Context, storePath string, config BrowserConfig, options BrowserLoginOptions) (*Profile, error) {
	if c == nil || ctx == nil || config.Validate() != nil || config.APIOrigin != c.origin || storePath == "" {
		return nil, ErrProfile
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}

	store, err := OpenStore(ctx, storePath)
	if err != nil {
		return nil, err
	}
	previous, previousRaw, existingAttempt, err := store.BrowserSnapshot(ctx)
	if err != nil {
		store.Close()
		return nil, err
	}
	previousExists := previous != nil
	if previousExists && previous.State != "active" {
		store.Close()
		return nil, errors.New("an unresolved profile must be recovered before browser authorization")
	}
	if previousExists && previous.Config.APIOrigin != config.APIOrigin {
		store.Close()
		return nil, errors.New("browser API origin differs from the active profile; retained without replacement")
	}

	var attempt *PendingBrowserAttempt
	resumed := existingAttempt != nil
	if existingAttempt != nil {
		attempt = existingAttempt
		if !sameBrowserConfig(attempt.Config, config) {
			store.Close()
			return nil, ErrBrowserAttemptExists
		}
		if previousExists && activeProfileMatchesAttempt(previous, attempt) {
			if err = store.Close(); err != nil {
				return nil, err
			}
			if _, err = c.Identity(ctx, previous); err != nil {
				return nil, err
			}
			store, err = OpenStore(ctx, storePath)
			if err != nil {
				return nil, err
			}
			err = store.ActivateBrowserProfile(attempt, previous, previousRaw, true)
			if closeErr := store.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return nil, err
			}
			return previous, nil
		}
	} else if previousExists && !options.Reauthorize {
		if err = store.Close(); err != nil {
			return nil, err
		}
		if _, err = c.Identity(ctx, previous); err != nil {
			return nil, err
		}
		return previous, nil
	}

	if existingAttempt == nil {
		attempt, err = NewPendingBrowserAttempt(config)
		if err != nil {
			store.Close()
			return nil, err
		}
	}
	previous, previousRaw, err = store.SaveBrowserAttemptBeforeBegin(attempt)
	if err != nil {
		store.Close()
		return nil, err
	}
	previousExists = previous != nil
	if err = store.Close(); err != nil {
		return nil, err
	}

	login := syntrav1connect.NewToolLoginServiceClient(c, c.origin, RPCOptions()...)
	begin, err := attempt.BeginRequest()
	if err != nil {
		return nil, err
	}
	started, err := login.BeginLogin(ctx, connect.NewRequest(begin))
	if err != nil {
		return nil, browserBeginError(err)
	}
	if err = validateBeginResponse(attempt, started.Msg); err != nil {
		return nil, err
	}
	if started.Msg.Request.State == v1.ToolLoginState_TOOL_LOGIN_STATE_DENIED || started.Msg.Request.State == v1.ToolLoginState_TOOL_LOGIN_STATE_EXPIRED {
		if !resumed {
			return nil, terminalLoginError(started.Msg.Request.State)
		}
		next, createErr := NewPendingBrowserAttempt(config)
		if createErr != nil {
			return nil, createErr
		}
		store, err = OpenStore(ctx, storePath)
		if err != nil {
			return nil, err
		}
		err = store.ReplaceTerminalBrowserAttempt(attempt, next, previousRaw, previousExists)
		if closeErr := store.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		attempt = next
		resumed = false
		begin, err = attempt.BeginRequest()
		if err != nil {
			return nil, err
		}
		started, err = login.BeginLogin(ctx, connect.NewRequest(begin))
		if err != nil {
			return nil, browserBeginError(err)
		}
		if err = validateBeginResponse(attempt, started.Msg); err != nil {
			return nil, err
		}
		if started.Msg.Request.State != v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING {
			return nil, terminalLoginError(started.Msg.Request.State)
		}
	}

	verificationURL := ""
	if started.Msg.Request.State == v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING {
		verificationURL, err = validateVerificationURL(started.Msg.VerificationUri, attempt.Config.BrowserOrigin)
		if err != nil {
			return nil, err
		}
		if err = writeBrowserInstructions(options.Output, verificationURL, started.Msg.Request.UserCode); err != nil {
			return nil, err
		}
		if !options.NoBrowser && options.OpenURL != nil {
			if err = options.OpenURL(verificationURL); err != nil {
				_, _ = fmt.Fprintln(options.Output, "The browser could not be opened; use the URL above on this or another device.")
			}
		}
	}

	interval := time.Duration(started.Msg.IntervalSeconds) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	networkFailures := 0
	for {
		if ctx.Err() != nil {
			return nil, errors.New("browser authorization interrupted; pending intent retained")
		}
		if options.Sleep(ctx, interval) != nil {
			return nil, errors.New("browser authorization interrupted; pending intent retained")
		}
		pollAuth, authErr := attempt.PollAuthorization()
		if authErr != nil {
			return nil, authErr
		}
		pollRequest := connect.NewRequest(&v1.PollLoginRequest{RequestId: attempt.RequestID})
		pollRequest.Header().Set("Authorization", pollAuth)
		polled, pollErr := login.PollLogin(ctx, pollRequest)
		if pollErr != nil {
			code := connect.CodeOf(pollErr)
			if code == connect.CodeResourceExhausted {
				interval = retryAfter(pollErr, interval)
				continue
			}
			if code != connect.CodeUnavailable && code != connect.CodeDeadlineExceeded && code != connect.CodeUnknown {
				return nil, SafeRPCError(pollErr)
			}
			networkFailures++
			if networkFailures > 8 {
				return nil, SafeRPCError(pollErr)
			}
			interval = nextNetworkBackoff(interval)
			continue
		}
		networkFailures = 0
		switch polled.Msg.State {
		case v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING:
			if polled.Msg.IntervalSeconds > 0 {
				interval = time.Duration(polled.Msg.IntervalSeconds) * time.Second
			}
			if interval < 5*time.Second {
				interval = 5 * time.Second
			}
		case v1.ToolLoginState_TOOL_LOGIN_STATE_DENIED, v1.ToolLoginState_TOOL_LOGIN_STATE_EXPIRED:
			return nil, terminalLoginError(polled.Msg.State)
		case v1.ToolLoginState_TOOL_LOGIN_STATE_APPROVED:
			if polled.Msg.Connection == nil {
				return nil, errors.New("approved browser request omitted connection metadata")
			}
			active, profileErr := attempt.ActiveProfile(polled.Msg.Connection)
			if profileErr != nil {
				return nil, profileErr
			}
			identity, identityErr := c.Identity(ctx, active)
			if identityErr != nil {
				return nil, identityErr
			}
			if !proto.Equal(identity, polled.Msg.Connection) {
				return nil, errors.New("verified identity differs from approved connection metadata; pending intent retained")
			}
			store, err = OpenStore(ctx, storePath)
			if err != nil {
				return nil, err
			}
			err = store.ActivateBrowserProfile(attempt, active, previousRaw, previousExists)
			if closeErr := store.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return nil, err
			}
			return active, nil
		default:
			return nil, errors.New("browser authorization returned an unknown state; pending intent retained")
		}
	}
}

func sameBrowserConfig(a, b BrowserConfig) bool {
	return reflect.DeepEqual(a, b)
}

func activeProfileMatchesAttempt(profile *Profile, attempt *PendingBrowserAttempt) bool {
	return profile != nil && profile.Version == browserProfileVersion && profile.Method == browserLoginMethod && profile.State == "active" && profile.RequestID == attempt.RequestID && profile.Secret == attempt.ToolSecret && profile.Config.APIOrigin == attempt.Config.APIOrigin && profile.BrowserOrigin == attempt.Config.BrowserOrigin && profile.Config.Label == attempt.Config.Label && capabilitySubset(profile.Config.Capabilities, attempt.Config.Capabilities) && profile.Config.LifetimeSeconds <= attempt.Config.LifetimeSeconds
}

func validateBeginResponse(attempt *PendingBrowserAttempt, response *v1.BeginLoginResponse) error {
	if attempt.Validate() != nil || response == nil || response.Request == nil || response.Request.ExpiresAt == nil || response.Request.ExpiresAt.CheckValid() != nil || response.IntervalSeconds <= 0 || response.IntervalSeconds > 86400 {
		return errors.New("invalid browser authorization preview; pending intent retained")
	}
	request, err := attempt.BeginRequest()
	if err != nil {
		return err
	}
	fingerprint, err := loginFingerprint(request)
	if err != nil {
		return err
	}
	preview := response.Request
	if preview.RequestId != request.RequestId || preview.Label != request.Label || !equalCaps(preview.Capabilities, request.Capabilities) || preview.ExpiresInSeconds != request.ExpiresInSeconds || len(preview.Fingerprint) != len(fingerprint) || !bytes.Equal(preview.Fingerprint, fingerprint) {
		return errors.New("browser authorization preview differs from retained intent; pending intent retained")
	}
	switch preview.State {
	case v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING,
		v1.ToolLoginState_TOOL_LOGIN_STATE_APPROVED,
		v1.ToolLoginState_TOOL_LOGIN_STATE_DENIED,
		v1.ToolLoginState_TOOL_LOGIN_STATE_EXPIRED:
	default:
		return errors.New("browser authorization preview has an unknown state; pending intent retained")
	}
	if preview.State == v1.ToolLoginState_TOOL_LOGIN_STATE_PENDING && !validUserCode(preview.UserCode) {
		return errors.New("browser authorization preview has an invalid code; pending intent retained")
	}
	return nil
}

func validUserCode(code string) bool {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	if len(code) != 8 {
		return false
	}
	for _, r := range code {
		if !strings.ContainsRune(alphabet, r) {
			return false
		}
	}
	return true
}

func browserBeginError(err error) error {
	code := connect.CodeOf(err)
	if code == connect.CodeNotFound || code == connect.CodeUnimplemented {
		return errors.New("browser authorization is unavailable on this API; use syntra login --method otp")
	}
	return SafeRPCError(err)
}

func validateVerificationURL(raw, trustedOrigin string) (string, error) {
	if ValidateOrigin(trustedOrigin) != nil {
		return "", errors.New("invalid trusted browser origin")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.RawPath != "" || u.Path != verificationPath || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.String() != raw || u.Scheme+"://"+u.Host != trustedOrigin {
		return "", errors.New("untrusted verification URL rejected; pending intent retained")
	}
	return raw, nil
}

func writeBrowserInstructions(out io.Writer, verificationURL, code string) error {
	if !validUserCode(code) {
		return errors.New("invalid browser authorization code; pending intent retained")
	}
	if _, err := fmt.Fprintf(out, "Open this URL on this or another device and enter the code:\nVerification URL: %s\nCode: %s\n", verificationURL, code); err != nil {
		return errors.New("could not display browser authorization instructions; pending intent retained")
	}
	return nil
}

func terminalLoginError(state v1.ToolLoginState) error {
	switch state {
	case v1.ToolLoginState_TOOL_LOGIN_STATE_DENIED:
		return errors.New("browser authorization was denied; pending intent retained")
	case v1.ToolLoginState_TOOL_LOGIN_STATE_EXPIRED:
		return errors.New("browser authorization expired; pending intent retained")
	default:
		return errors.New("browser authorization ended without approval; pending intent retained")
	}
}

func retryAfter(err error, current time.Duration) time.Duration {
	const (
		minimumPollInterval    = 5 * time.Second
		backoffIncrement       = 5 * time.Second
		backoffCap             = 60 * time.Second
		maximumRetryAfterValue = 86400
	)

	localCurrent := current
	if localCurrent < minimumPollInterval {
		localCurrent = minimumPollInterval
	}
	next := localCurrent + backoffIncrement
	if next > backoffCap {
		next = backoffCap
	}
	// Keep any previously accepted server-directed wait even when it exceeds
	// the local backoff cap.
	if next < current {
		next = current
	}

	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if seconds, parseErr := strconv.ParseInt(strings.TrimSpace(connectErr.Meta().Get("Retry-After")), 10, 64); parseErr == nil && seconds > 0 && seconds <= maximumRetryAfterValue {
			serverDelay := time.Duration(seconds) * time.Second
			if serverDelay > next {
				next = serverDelay
			}
		}
	}
	return next
}

func nextNetworkBackoff(current time.Duration) time.Duration {
	if current < 5*time.Second {
		current = 5 * time.Second
	}
	next := current * 2
	if next > 60*time.Second {
		next = 60 * time.Second
	}
	return next
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
