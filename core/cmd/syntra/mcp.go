package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"
	"syntra.local/mcp/internal/toolclient"
	"syntra.local/mcp/internal/toolmcp"
)

func runMCP(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("profile-dir", "", "shared private profile directory")
	api := flags.String("api", "", "exact retained API origin")
	timeout := flags.Duration("call-timeout", time.Minute, "per-call deadline, not session lifetime")
	if err := flags.Parse(args); err != nil {
		return optionError("mcp", err)
	}
	if flags.NArg() != 0 {
		return optionError("mcp", nil)
	}
	if *timeout <= 0 || *timeout > 10*time.Minute {
		return fmt.Errorf("--call-timeout must be positive and at most 10m. %s", usage("mcp"))
	}
	if term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("MCP requires piped stdin and stdout; human login is a separate command")
	}
	if *dir == "" {
		resolved, err := toolclient.DefaultProfileDir()
		if err != nil {
			return err
		}
		*dir = resolved
	}

	input, err := duplicateMCPStdin()
	if err != nil {
		return errors.New("MCP stdin unavailable")
	}
	output, err := duplicateMCPStdout()
	if err != nil {
		_ = input.Close()
		return errors.New("MCP stdout unavailable")
	}
	defer input.Close()
	defer output.Close()
	return toolmcp.Run(ctx, toolmcp.Config{
		Version: version, ProfileDir: *dir, APIOrigin: *api, CallTimeout: *timeout,
		WriteTimeout: 2 * time.Second,
	}, input, output)
}
