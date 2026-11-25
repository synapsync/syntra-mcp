package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/toolclient"
)

func isContentCommand(c string) bool {
	switch c {
	case "workspaces", "projects", "teams", "members", "resolve", "prepare", "reply", "link", "publish", "discard", "retry", "snapshot", "threads", "thread", "messages", "add-participant":
		return true
	}
	return false
}
func runContent(parent context.Context, args []string) error {
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	dir := f.String("profile-dir", "", "private shared profile directory")
	api := f.String("api", "", "exact retained API origin")
	timeout := f.Duration("timeout", time.Minute, "operation deadline")
	template := f.String("template", "", "new or reply template")
	summary := f.String("summary", "", "reply summary")
	contextFile := f.String("context-file", "", "Markdown file or - for stdin")
	file := f.String("file", "", "strict bounded request JSON file, or - for stdin")
	base := f.String("base", "", "retained request ID anchoring exact CAS/preimage")
	request := f.String("request", "", "retained request UUID for retry")
	workspace := f.String("workspace", "", "workspace ID or resolve name")
	project := f.String("project", "", "project ID or resolve name")
	team := f.String("team", "", "project team name or ID for resolve")
	recipient := f.String("recipient", "", "recipient for resolve: a person by name, @username or ID, or a team of the project")
	recipients := f.String("recipients", "", "comma-separated people (names, @usernames or IDs; at most 10) for resolve")
	asTeam := f.Bool("as-team", false, "resolve: address the whole selected --team")
	thread := f.String("thread", "", "thread ID")
	view := f.String("view", "published", "messages: published or own-prepared")
	member := f.String("member", "", "add-participant: active workspace member UUID")
	requestID := f.String("request-id", "", "add-participant: fresh canonical UUID; resend it to retry")
	if err := f.Parse(args[1:]); err != nil {
		return optionError(args[0], err)
	}
	if f.NArg() != 0 || *timeout <= 0 || *timeout > 10*time.Minute {
		return optionError(args[0], nil)
	}
	allowed := map[string]bool{"profile-dir": true, "api": true, "timeout": true}
	switch args[0] {
	case "workspaces":
	// Account-wide listing accepts only common profile/origin/deadline options.
	case "projects", "members", "threads":
		allowed["workspace"] = true
		if args[0] == "threads" {
			allowed["view"] = true
		}
	case "teams":
		allowed["workspace"] = true
		allowed["project"] = true
	case "thread", "messages":
		if args[0] == "messages" {
			allowed["view"] = true
		}
		allowed["workspace"] = true
		allowed["thread"] = true
	case "resolve":
		for _, s := range []string{"workspace", "project", "team", "recipient", "recipients", "as-team"} {
			allowed[s] = true
		}
	case "retry":
		allowed["request"] = true
	case "add-participant":
		for _, s := range []string{"workspace", "thread", "member", "request-id"} {
			allowed[s] = true
		}
	case "reply":
		for _, s := range []string{"workspace", "thread", "summary", "context-file"} {
			allowed[s] = true
		}
	case "prepare":
		for _, s := range []string{"file", "base", "template", "workspace", "project", "thread"} {
			allowed[s] = true
		}
	default:
		allowed["file"] = true
		allowed["base"] = true
	}
	invalid := ""
	f.Visit(func(x *flag.Flag) {
		if !allowed[x.Name] {
			invalid = x.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("option unavailable for this command: --%s. %s", invalid, usage(args[0]))
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	var target proto.Message
	switch args[0] {
	case "prepare":
		if *template == "" {
			target = &v1.PrepareContentRequest{}
		} else if *file != "" || *base != "" {
			return errors.New("--template cannot be combined with --file or --base")
		} else if err := toolclient.ValidateTemplateInput(*template, *workspace, *project, *thread); err != nil {
			return err
		}
	case "link":
		target = &v1.LinkContentRequest{}
	case "publish":
		target = &v1.PublishContentRequest{}
	case "discard":
		target = &v1.DiscardContentRequest{}
	case "snapshot":
		target = &v1.GetContentSnapshotRequest{}
	}
	if target != nil {
		if *file == "" {
			return errors.New("explicit --file required; use - for bounded stdin")
		}
		input, closeInput, err := openBoundedInput(*file, 1048576, "request")
		if err != nil {
			return err
		}
		defer closeInput()
		if err = readContentWithContext(ctx, input, target); err != nil {
			return err
		}
		if err = toolclient.ValidateLocalContentRequest(target, *base); err != nil {
			return err
		}
	}
	replyOpts := replyOptions{Workspace: *workspace, ThreadID: *thread, Summary: *summary}
	if args[0] == "reply" {
		if *contextFile == "" {
			return errors.New("reply requires --workspace, --thread, --summary and --context-file")
		}
		if err := toolclient.ValidateReplyInput(*workspace, *thread, *summary, ""); err != nil {
			return err
		}
		data, err := readReplyContext(ctx, *contextFile)
		if err != nil {
			return err
		}
		replyOpts.ContextMarkdown = string(data)
		if err = toolclient.ValidateReplyInput(*workspace, *thread, *summary, replyOpts.ContextMarkdown); err != nil {
			return err
		}
	}
	if *dir == "" {
		resolved, e := toolclient.DefaultProfileDir()
		if e != nil {
			return e
		}
		*dir = resolved
	}
	store, e := toolclient.OpenStore(ctx, *dir)
	if e != nil {
		return toolclient.ProfileError(*dir, e)
	}
	defer store.Close()
	p, e := store.Load()
	if e != nil {
		return toolclient.ProfileError(*dir, e)
	}
	if *api != "" && *api != p.Config.APIOrigin {
		return errors.New("API override does not match retained profile origin")
	}
	client, e := toolclient.NewClient(p.Config.APIOrigin, nil)
	if e != nil {
		return e
	}
	app, e := toolclient.NewApp(ctx, client, store)
	if e != nil {
		return e
	}
	if args[0] == "reply" {
		replyOpts.Account = p.AccountID
		replyOpts.ProfileDir = *dir
		return reply(ctx, app, replyOpts)
	}
	if args[0] == "prepare" && *template != "" {
		if *file != "" || *base != "" {
			return errors.New("--template cannot be combined with --file or --base")
		}
		if *template == "reply" {
			t, err := app.Thread(ctx, *workspace, *thread)
			if err != nil {
				return err
			}
			return printTemplate(*template, *workspace, *project, p.AccountID, t)
		}
		return printTemplate(*template, *workspace, *project, p.AccountID, nil)
	}
	var result any
	var message proto.Message
	switch args[0] {
	case "workspaces":
		rows, err := app.Workspaces(ctx)
		e = err
		if e == nil {
			message = &v1.ListAccessibleWorkspacesResponse{Workspaces: rows}
		}
	case "projects":
		rows, err := app.Projects(ctx, *workspace)
		e = err
		if e == nil {
			message = &v1.ListProjectsResponse{Projects: rows}
		}
	case "teams":
		rows, err := app.ProjectTeams(ctx, *workspace, *project)
		e = err
		if e == nil {
			message = &v1.ListProjectTeamsResponse{ProjectTeams: rows}
		}
	case "members":
		rows, err := app.Members(ctx, *workspace)
		e = err
		if e == nil {
			message = &v1.ListMembersResponse{Members: rows}
		}
	case "threads":
		v := v1.ThreadVisibility_THREAD_VISIBILITY_PUBLISHED
		if *view == "own-prepared" {
			v = v1.ThreadVisibility_THREAD_VISIBILITY_OWN_PREPARED
		} else if *view != "published" {
			return errors.New("invalid explicit thread view")
		}
		rows, err := app.Threads(ctx, *workspace, v)
		e = err
		if e == nil {
			message = &v1.ListThreadsResponse{Threads: rows}
		}
	case "messages":
		v := v1.MessageView_MESSAGE_VIEW_PUBLISHED
		if *view == "own-prepared" {
			v = v1.MessageView_MESSAGE_VIEW_OWN_PREPARED
		} else if *view != "published" {
			return errors.New("invalid explicit message view")
		}
		message, e = app.Messages(ctx, *workspace, *thread, v)
	case "thread":
		var t *v1.Thread
		t, e = app.Thread(ctx, *workspace, *thread)
		if e == nil && t.State == v1.ThreadState_THREAD_STATE_CLOSED {
			fmt.Fprintln(os.Stderr, "Closed threads accept no replies until a person reopens the thread in the Syntra web app.")
		}
		message = t
	case "resolve":
		result, e = app.Resolve(ctx, toolclient.Selection{Workspace: *workspace, Project: *project, Team: *team, Recipient: *recipient, Recipients: splitRecipients(*recipients), AsTeam: *asTeam})
	case "add-participant":
		message, e = app.AddParticipant(ctx, *workspace, *thread, *member, *requestID)
	case "retry":
		message, e = app.Retry(ctx, *request)
	default:

		switch q := target.(type) {
		case *v1.PrepareContentRequest:
			message, e = app.Prepare(ctx, q, *base)
		case *v1.LinkContentRequest:
			message, e = app.Link(ctx, q, *base)
		case *v1.PublishContentRequest:
			message, e = app.Publish(ctx, q, *base)
		case *v1.DiscardContentRequest:
			message, e = app.Discard(ctx, q)
		case *v1.GetContentSnapshotRequest:
			var s *toolclient.VerifiedSnapshot
			s, e = app.Snapshot(ctx, q, *base)
			if e == nil {
				result, e = s.Output()
			}
		}
	}
	if e != nil {
		var clarification *toolclient.Clarification
		if errors.As(e, &clarification) {
			json.NewEncoder(os.Stdout).Encode(clarification)
		}
		return e
	}
	if message != nil {
		raw, err := protojson.Marshal(message)
		if err != nil {
			return errors.New("response encoding failed")
		}
		_, e = fmt.Fprintln(os.Stdout, string(raw))
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

// A stalled stdin pipe cannot outlive the operation deadline. The command exits
// after cancellation; no blocked input reader is reused for another operation.
func readContentWithContext(ctx context.Context, r io.Reader, target proto.Message) error {
	return runInputWithContext(ctx, func() error {
		return toolclient.ReadContentInputWithCorrections(r, target, func(s string) { fmt.Fprintln(os.Stderr, "syntra:", s) })
	})
}
func runInputWithContext(ctx context.Context, read func() error) error {
	done := make(chan error, 1)
	go func() { done <- read() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errors.New("request input cancelled or deadline exceeded")
	}
}

// openBoundedInput preserves the existing no-follow and regular-file checks.
func openBoundedInput(path string, max int64, label string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	file, err := openRequestFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("%s file unavailable", label)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > max {
		file.Close()
		return nil, nil, fmt.Errorf("%s file must be a bounded regular file", label)
	}
	return file, func() { file.Close() }, nil
}
func readReplyContext(ctx context.Context, path string) ([]byte, error) {
	reader, closeInput, err := openBoundedInput(path, 131072, "context")
	if err != nil {
		return nil, err
	}
	defer closeInput()
	// Regular files cannot block on a producer; stdin may be a stalled pipe and
	// retains the operation deadline via the same cancellation wrapper as JSON.
	var data []byte
	read := func() error {
		var e error
		data, e = io.ReadAll(io.LimitReader(reader, 131073))
		if e != nil || len(data) > 131072 {
			return errors.New("context file exceeds limit or cannot be read")
		}
		return nil
	}
	if path != "-" {
		err = read()
	} else {
		err = runInputWithContext(ctx, read)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// splitRecipients parses the comma-separated --recipients value; empty items are kept so validation rejects them.
func splitRecipients(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
