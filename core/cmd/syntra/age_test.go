package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "syntra.local/mcp/gen/syntra/v1"
)

func TestRelativeAge(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{-time.Hour, "just now"}, {30 * time.Second, "just now"}, {time.Minute, "1m ago"}, {59 * time.Minute, "59m ago"},
		{time.Hour, "1h ago"}, {23 * time.Hour, "23h ago"}, {24 * time.Hour, "1d ago"}, {13 * 24 * time.Hour, "13d ago"},
		{14 * 24 * time.Hour, "2w ago"}, {59 * 24 * time.Hour, "8w ago"}, {60 * 24 * time.Hour, "2mo ago"},
		{359 * 24 * time.Hour, "11mo ago"}, {360 * 24 * time.Hour, "1y ago"}, {364 * 24 * time.Hour, "1y ago"}, {366 * 24 * time.Hour, "1y ago"}, {2 * 365 * 24 * time.Hour, "2y ago"},
	} {
		if got := relativeAge(now, now.Add(-tc.ago)); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.ago, got, tc.want)
		}
	}
}

func TestThreadsTextShowsAgeAndStaysOneLinePerThread(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	rows := []*v1.Thread{
		{Id: "t1", State: v1.ThreadState_THREAD_STATE_OPEN, Title: "Recent", LastMessagePublishedAt: timestamppb.New(now.Add(-3 * time.Hour))},
		{Id: "t2", State: v1.ThreadState_THREAD_STATE_PENDING, Title: "Draft\nsecond line\x1b[31m"},
		{Id: "t3", State: v1.ThreadState_THREAD_STATE_CLOSED, Title: "Old", LastMessagePublishedAt: timestamppb.New(now.Add(-20 * 24 * time.Hour))},
	}
	var out bytes.Buffer
	if err := writeThreadsText(&out, rows, now); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "AGE") {
		t.Fatalf("unexpected output %q", out.String())
	}
	for i, want := range [][]string{{"3h ago", "open", "t1", "Recent"}, {"-", "pending", "t2", "Draft"}, {"2w ago", "closed", "t3", "Old"}} {
		for _, part := range want {
			if !strings.Contains(lines[i+1], part) {
				t.Errorf("line %d %q misses %q", i+1, lines[i+1], part)
			}
		}
	}
	if strings.ContainsRune(out.String(), '\x1b') {
		t.Fatal("control characters were printed")
	}
}

func TestThreadsFormatAndAncestryOptions(t *testing.T) {
	for _, args := range [][]string{
		{"threads", "--format", "xml"},
		{"thread", "--format", "text"},
		{"messages", "--format", "text"},
		{"ancestry", "--format", "text"},
		{"ancestry", "--view", "published"},
		{"ancestry", "--message", "00000000-0000-4000-8000-000000000001"},
	} {
		if err := run(context.Background(), append(args, "--profile-dir", t.TempDir())); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}
