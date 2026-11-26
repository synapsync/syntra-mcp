package main

import (
	"context"
	"strings"
	"testing"
)

func TestForkCommandOptions(t *testing.T) {
	for _, missing := range []string{"--title", "--message", "--request-id"} {
		t.Run(missing, func(t *testing.T) {
			args := []string{"fork", "--profile-dir", t.TempDir()}
			for _, flag := range []string{"--title", "--message", "--request-id"} {
				if flag != missing {
					args = append(args, flag, "present")
				}
			}
			assertForkOptionError(t, args, "invalid or missing option")
		})
	}
	assertForkOptionError(t, []string{"fork", "--title", "Title", "--message", "anchor", "--request-id", "request", "--source-member", "member", "--no-source-members"}, "invalid or missing option")
	for _, flag := range []string{"--message", "--source-member", "--no-source-members"} {
		args := []string{"thread", flag}
		if flag != "--no-source-members" {
			args = append(args, "present")
		}
		assertForkOptionError(t, args, "option unavailable for this command: "+flag)
	}
}

func assertForkOptionError(t *testing.T, args []string, want string) {
	t.Helper()
	if err := run(context.Background(), args); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %q, got %v", want, err)
	}
}
