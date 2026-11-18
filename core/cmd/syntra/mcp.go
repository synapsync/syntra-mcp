package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/term"
	"syntra.local/mcp/internal/toolmcp"
)

func runMCP(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("profile-dir", "", "shared private profile directory")
	api := flags.String("api", "", "exact retained API origin")
	timeout := flags.Duration("call-timeout", time.Minute, "per-call deadline, not session lifetime")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *timeout <= 0 || *timeout > 10*time.Minute {
		return errors.New("invalid local MCP options")
	}
	if term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("MCP requires piped stdin and stdout; human login is a separate command")
	}
	if *dir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return errors.New("explicit private profile directory required")
		}
		*dir = filepath.Join(root, "syntra")
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
		ProfileDir: *dir, APIOrigin: *api, CallTimeout: *timeout,
		WriteTimeout: 2 * time.Second,
	}, input, output)
}
