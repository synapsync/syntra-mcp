package main

import (
	"context"
	"strings"
	"testing"
)

func TestAddParticipantCommandAcceptsOnlyItsOptions(t *testing.T) {
	err := run(context.Background(), []string{"add-participant", "--file", "request.json", "--workspace", "44444444-4444-4444-8444-444444444444", "--thread", "77777777-7777-4777-8777-777777777777"})
	if err == nil || !strings.Contains(err.Error(), "option unavailable for this command: --file") {
		t.Fatalf("a request file must not be accepted by add-participant: %v", err)
	}
	err = run(context.Background(), []string{"add-participant", "--workspace", "44444444-4444-4444-8444-444444444444", "--thread", "77777777-7777-4777-8777-777777777777", "--member", "88888888-8888-4888-8888-888888888888", "--request-id", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "--profile-dir", t.TempDir()})
	if err == nil {
		t.Fatal("without a retained profile the command must fail before any request")
	}
}
