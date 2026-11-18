// Syntra's Linux pilot CLI shares a private account connection with local tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/term"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/toolclient"
)

var version = "0.0.0-handoff"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "syntra:", e)
		os.Exit(1)
	}
}

func run(parent context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: syntra login|status|identity|disconnect|forget [options]")
	}
	command := args[0]
	if command == "--help" || command == "-h" || command == "help" && len(args) == 1 {
		printHelp("")
		return nil
	}
	if command == "help" && len(args) == 2 {
		if _, ok := helpEntries[args[1]]; !ok {
			return unknownCommand(args[1])
		}
		printHelp(args[1])
		return nil
	}
	if _, ok := helpEntries[command]; ok && len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		printHelp(command)
		return nil
	}
	if (command == "version" || command == "--version") && len(args) == 1 {
		fmt.Println(version)
		return nil
	}
	if command == "mcp" {
		return runMCP(parent, args[1:])
	}
	if isContentCommand(command) {
		return runContent(parent, args)
	}
	if command != "login" && command != "status" && command != "identity" && command != "disconnect" && command != "forget" {
		return unknownCommand(command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("profile-dir", "", "absolute private profile directory; existing parent required")
	api := flags.String("api", "", "exact HTTPS API origin")
	method := flags.String("method", "browser", "login method: browser (default) or otp")
	browserOrigin := flags.String("browser-origin", "", "exact HTTPS origin trusted for browser consent")
	noBrowser := flags.Bool("no-browser", false, "print the verification URL and code without opening a browser")
	reauthorize := flags.Bool("reauthorize", false, "deliberately replace a compatible active connection through browser consent")
	provider := flags.String("provider", "", "exact managed Supabase HTTPS origin (login --method otp)")
	key := flags.String("publishable-key", "", "public Supabase publishable key (login --method otp)")
	label := flags.String("label", "Syntra local tools", "connection label (login)")
	lifetime := flags.Int("lifetime-seconds", 2592000, "absolute lifetime 300..7776000 (login)")
	prepare := flags.Bool("prepare", false, "enable preparation (login)")
	publish := flags.Bool("publish", false, "enable publication, requires --prepare (login)")
	markRead := flags.Bool("mark-read", false, "enable explicit mark-read (login)")
	timeout := flags.Duration("timeout", 5*time.Minute, "operation/lock deadline")
	if err := flags.Parse(args[1:]); err != nil {
		return optionError(command, err)
	}
	if flags.NArg() != 0 {
		return optionError(command, nil)
	}
	if *timeout <= 0 || *timeout > 10*time.Minute {
		return errors.New("timeout must be positive and at most 10m")
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	if command != "login" {
		bad := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name != "api" && f.Name != "profile-dir" && f.Name != "timeout" {
				bad = true
			}
		})
		if bad {
			return errors.New("login configuration flags are unavailable for this command")
		}
	}
	if *dir == "" {
		base, e := os.UserConfigDir()
		if e != nil {
			return errors.New("set --profile-dir to a private absolute directory")
		}
		*dir = filepath.Join(base, "syntra")
	}
	if command == "login" {
		if *method != "browser" && *method != "otp" {
			return errors.New("login method must be browser or otp")
		}
		if *method == "browser" {
			if flagWasSet(flags, "provider") || flagWasSet(flags, "publishable-key") {
				return errors.New("provider credentials are accepted only with --method otp")
			}
			return runBrowserLoginCommand(ctx, *dir, *api, *browserOrigin, *label, *lifetime, *prepare, *publish, *markRead, *noBrowser, *reauthorize, flags)
		}
		if *noBrowser || *reauthorize || flagWasSet(flags, "browser-origin") {
			return errors.New("--no-browser, --reauthorize and --browser-origin are available only with browser login")
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
			return errors.New("OTP login requires a human terminal on stdin and stderr; use the human CLI outside MCP")
		}
	}
	store, e := toolclient.OpenStore(ctx, *dir)
	if e != nil {
		return e
	}
	defer store.Close()
	if command == "login" {
		if *lifetime < 300 || *lifetime > 7776000 {
			return toolclient.ErrProfile
		}
		caps := []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}
		if *prepare {
			caps = append(caps, 2)
		}
		if *publish {
			caps = append(caps, 3)
		}
		if *markRead {
			caps = append(caps, 4)
		}
		config := toolclient.Config{APIOrigin: *api, ProviderOrigin: *provider, PublishableKey: *key, Label: *label, Capabilities: caps, LifetimeSeconds: int32(*lifetime)}
		if config.Validate() != nil {
			return errors.New("invalid login configuration or capability selection")
		}
		previous, e := store.Load()
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if previous != nil && previous.Method == "browser" {
			return errors.New("browser profile cannot be replaced through OTP login")
		}
		if previous != nil && !toolclient.SameConfig(previous.Config, config) {
			return errors.New("existing profile configuration differs; retained without replacement")
		}
		p, e := toolclient.NewOTPProvider(*provider, *key, nil)
		if e != nil {
			return e
		}
		client, e := toolclient.NewClient(*api, nil)
		if e != nil {
			return e
		}
		email, e := readHuman(ctx, "Email: ")
		if e != nil {
			return e
		}
		defer clear(email)
		if e = p.Request(ctx, string(email)); e != nil {
			return e
		}
		code, e := readHuman(ctx, "Email OTP (hidden): ")
		if e != nil {
			return e
		}
		defer clear(code)
		jwt, e := p.Verify(ctx, string(email), code)
		clear(code)
		if e != nil {
			return e
		}
		profile, e := client.CompleteLogin(ctx, store, config, jwt)
		jwt = ""
		if e != nil {
			return e
		}
		fmt.Fprintf(os.Stdout, "Connected account %s; connection %s; expires %s.\n", profile.AccountID, profile.ConnectionID, profile.ExpiresAt)
		return nil
	}
	if command == "forget" {
		if e = store.Remove(); errors.Is(e, os.ErrNotExist) {
			return errors.New("no local profile or pending browser attempt")
		}
		if e != nil {
			return e
		}
		fmt.Fprintln(os.Stdout, "Local profile and pending browser attempt removed. This does not revoke any server connection.")
		return nil
	}
	p, e := store.Load()
	if e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return errors.New("no local connection; run human interactive login")
		}
		return e
	}
	if *api != "" && *api != p.Config.APIOrigin {
		return errors.New("API override does not match retained profile origin")
	}
	switch command {
	case "status":
		fmt.Fprintf(os.Stdout, "Profile %s; account %s; API %s; capabilities %v", p.State, p.AccountID, p.Config.APIOrigin, p.Config.Capabilities)
		if p.State == "active" {
			fmt.Fprintf(os.Stdout, "; connection %s; expires %s", p.ConnectionID, p.ExpiresAt)
		}
		fmt.Fprintln(os.Stdout)
		return nil
	}
	if p.State != "active" {
		return errors.New("unresolved issuance; repeat the exact human login to recover")
	}
	client, e := toolclient.NewClient(p.Config.APIOrigin, nil)
	if e != nil {
		return e
	}
	switch command {
	case "identity":
		q, e := client.Identity(ctx, p)
		if e != nil {
			return e
		}
		fmt.Fprintf(os.Stdout, "Account %s; connection %s; expires %s.\n", q.OwnerUserId, q.Id, p.ExpiresAt)
	case "disconnect":
		if e = client.Disconnect(ctx, p); e != nil {
			return e
		}
		if e = store.Remove(); e != nil {
			return e
		}
		fmt.Fprintln(os.Stdout, "Connection revoked; local profile removed.")
	}
	return nil
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func runBrowserLoginCommand(ctx context.Context, profileDir, apiOrigin, browserOrigin, label string, lifetime int, prepare, publish, markRead, noBrowser, reauthorize bool, flags *flag.FlagSet) error {
	store, err := toolclient.OpenStore(ctx, profileDir)
	if err != nil {
		return err
	}
	previous, _, pending, err := store.BrowserSnapshot(ctx)
	if err != nil {
		store.Close()
		return err
	}
	if err = store.Close(); err != nil {
		return err
	}

	apiWasSet := flagWasSet(flags, "api")
	browserOriginWasSet := flagWasSet(flags, "browser-origin")
	labelWasSet := flagWasSet(flags, "label")
	lifetimeWasSet := flagWasSet(flags, "lifetime-seconds")
	capabilityFlagsSet := flagWasSet(flags, "prepare") || flagWasSet(flags, "publish") || flagWasSet(flags, "mark-read")
	explicitConfig := apiWasSet || browserOriginWasSet || labelWasSet || lifetimeWasSet || capabilityFlagsSet
	if (apiWasSet && apiOrigin == "") || (browserOriginWasSet && browserOrigin == "") || (labelWasSet && label == "") || (lifetimeWasSet && lifetime == 0) {
		return errors.New("explicit browser configuration values cannot be empty or zero")
	}

	if pending != nil {
		if !apiWasSet {
			apiOrigin = pending.Config.APIOrigin
		}
		if !browserOriginWasSet {
			browserOrigin = pending.Config.BrowserOrigin
		}
		if !labelWasSet {
			label = pending.Config.Label
		}
		if !lifetimeWasSet {
			lifetime = int(pending.Config.LifetimeSeconds)
		}
	} else if previous != nil {
		if !apiWasSet {
			apiOrigin = previous.Config.APIOrigin
		}
		if !browserOriginWasSet && previous.Version == 2 {
			browserOrigin = previous.BrowserOrigin
		}
		if !labelWasSet {
			label = previous.Config.Label
		}
		if !lifetimeWasSet {
			lifetime = int(previous.Config.LifetimeSeconds)
		}
	}
	if previous != nil && pending == nil && !reauthorize {
		if !toolclient.BrowserConfigMatchesProfile(previous, toolclient.BrowserConfig{
			APIOrigin: apiOrigin, BrowserOrigin: browserOrigin, Label: label,
			Capabilities:    browserCapabilities(prepare, publish, markRead, capabilityFlagsSet, previous),
			LifetimeSeconds: int32(lifetime),
		}) {
			if explicitConfig {
				return errors.New("requested browser configuration differs from the active connection; use --reauthorize for a compatible replacement")
			}
			return errors.New("could not resolve the installed profile's browser configuration")
		}
		client, clientErr := toolclient.NewClient(previous.Config.APIOrigin, nil)
		if clientErr != nil {
			return clientErr
		}
		if _, clientErr = client.Identity(ctx, previous); clientErr != nil {
			return clientErr
		}
		fmt.Fprintf(os.Stdout, "Connected account %s; connection %s; expires %s.\n", previous.AccountID, previous.ConnectionID, previous.ExpiresAt)
		return nil
	}

	if apiOrigin == "" {
		switch {
		case pending != nil:
			apiOrigin = pending.Config.APIOrigin
		case previous != nil:
			apiOrigin = previous.Config.APIOrigin
		default:
			apiOrigin = toolclient.DefaultBrowserAPIOrigin
		}
	}
	if browserOrigin == "" {
		switch {
		case pending != nil:
			browserOrigin = pending.Config.BrowserOrigin
		case previous != nil && previous.Version == 2:
			browserOrigin = previous.BrowserOrigin
		case apiOrigin == toolclient.DefaultBrowserAPIOrigin:
			browserOrigin = toolclient.DefaultBrowserVerificationOrigin
		default:
			return errors.New("a self-hosted API requires an explicit --browser-origin trust")
		}
	}
	if apiOrigin != toolclient.DefaultBrowserAPIOrigin && !browserOriginWasSet && pending == nil && (previous == nil || previous.Version != 2) {
		return errors.New("a self-hosted API requires an explicit --browser-origin trust")
	}
	if label == "" {
		switch {
		case pending != nil:
			label = pending.Config.Label
		case previous != nil:
			label = previous.Config.Label
		default:
			label = "Syntra local tools"
		}
	}
	if lifetime == 0 {
		switch {
		case pending != nil:
			lifetime = int(pending.Config.LifetimeSeconds)
		case previous != nil:
			lifetime = int(previous.Config.LifetimeSeconds)
		default:
			lifetime = 2592000
		}
	}
	if lifetime < 300 || lifetime > 7776000 {
		return toolclient.ErrProfile
	}
	var capabilities []v1.ToolCapability
	switch {
	case capabilityFlagsSet:
		capabilities = []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}
		if prepare {
			capabilities = append(capabilities, v1.ToolCapability_TOOL_CAPABILITY_PREPARE)
		}
		if publish {
			capabilities = append(capabilities, v1.ToolCapability_TOOL_CAPABILITY_PUBLISH)
		}
		if markRead {
			capabilities = append(capabilities, v1.ToolCapability_TOOL_CAPABILITY_MARK_READ)
		}
	case pending != nil:
		capabilities = append([]v1.ToolCapability(nil), pending.Config.Capabilities...)
	case previous != nil:
		capabilities = append([]v1.ToolCapability(nil), previous.Config.Capabilities...)
	default:
		capabilities = []v1.ToolCapability{
			v1.ToolCapability_TOOL_CAPABILITY_READ,
			v1.ToolCapability_TOOL_CAPABILITY_PREPARE,
			v1.ToolCapability_TOOL_CAPABILITY_PUBLISH,
		}
	}
	config := toolclient.BrowserConfig{
		APIOrigin: apiOrigin, BrowserOrigin: browserOrigin, Label: label,
		Capabilities: capabilities, LifetimeSeconds: int32(lifetime),
	}
	if config.Validate() != nil {
		return errors.New("invalid browser login configuration or capability selection")
	}
	if previous != nil && pending == nil {
		if config.APIOrigin != previous.Config.APIOrigin || (previous.Version == 2 && config.BrowserOrigin != previous.BrowserOrigin) || !toolclient.BrowserPermissionsSubset(config.Capabilities, previous.Config.Capabilities) || config.LifetimeSeconds > previous.Config.LifetimeSeconds {
			return errors.New("reauthorization cannot change origin, expand permissions or extend the active lifetime")
		}
	}
	client, err := toolclient.NewClient(config.APIOrigin, nil)
	if err != nil {
		return err
	}
	profile, err := client.RunBrowserLogin(ctx, profileDir, config, toolclient.BrowserLoginOptions{
		NoBrowser: noBrowser, Reauthorize: reauthorize, Output: os.Stdout,
		OpenURL: openVerificationPage,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Connected account %s; connection %s; expires %s.\n", profile.AccountID, profile.ConnectionID, profile.ExpiresAt)
	return nil
}

func browserCapabilities(prepare, publish, markRead, capabilityFlagsSet bool, previous *toolclient.Profile) []v1.ToolCapability {
	if !capabilityFlagsSet && previous != nil {
		return append([]v1.ToolCapability(nil), previous.Config.Capabilities...)
	}
	caps := []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}
	if prepare {
		caps = append(caps, v1.ToolCapability_TOOL_CAPABILITY_PREPARE)
	}
	if publish {
		caps = append(caps, v1.ToolCapability_TOOL_CAPABILITY_PUBLISH)
	}
	if markRead {
		caps = append(caps, v1.ToolCapability_TOOL_CAPABILITY_MARK_READ)
	}
	return caps
}

// MakeRaw and Terminal.ReadPassword are official terminal implementations.
// Disabling echo synchronously before the prompt prevents fast-input echo races.
// Restore the captured
// state on cancellation/SIGINT/SIGTERM as well as ordinary return. SIGKILL cannot
// execute cleanup. No stdin pipe, OTP flag, env lookup or model OTP prompt exists.
func readHuman(ctx context.Context, prompt string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("human terminal required")
	}
	state, e := term.MakeRaw(fd)
	if e != nil {
		return nil, errors.New("terminal unavailable")
	}
	defer term.Restore(fd, state)
	terminal := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{io.LimitReader(os.Stdin, 255), os.Stderr}, "")
	type result struct {
		value []byte
		err   error
	}
	done := make(chan result, 1)
	go func() { line, e := terminal.ReadPassword(prompt); done <- result{[]byte(line), e} }()
	select {
	case <-ctx.Done():
		term.Restore(fd, state)
		fmt.Fprint(os.Stderr, "\r\n")
		return nil, errors.New("login cancelled or deadline exceeded")
	case r := <-done:
		fmt.Fprintln(os.Stderr)
		if r.err != nil {
			clear(r.value)
			return nil, errors.New("terminal input failed")
		}
		if len(r.value) > 254 {
			clear(r.value)
			return nil, errors.New("terminal input too long")
		}
		return r.value, nil
	}
}
