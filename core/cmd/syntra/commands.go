package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/toolclient"
)

func isContentCommand(c string) bool {
	switch c {
	case "workspaces", "projects", "teams", "members", "resolve", "prepare", "link", "publish", "retry", "snapshot", "tickets", "ticket", "messages":
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
	file := f.String("file", "", "strict bounded request JSON file, or - for stdin")
	base := f.String("base", "", "retained request ID anchoring exact CAS/preimage")
	request := f.String("request", "", "retained request UUID for retry")
	workspace := f.String("workspace", "", "workspace ID or resolve name")
	project := f.String("project", "", "project ID or resolve name")
	team := f.String("team", "", "team name for resolve")
	recipient := f.String("recipient", "", "recipient name for resolve")
	ticket := f.String("ticket", "", "ticket ID")
	view := f.String("view", "published", "messages: published or own-drafts")
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
	case "projects", "members", "tickets":
		allowed["workspace"] = true
		if args[0] == "tickets" {
			allowed["view"] = true
		}
	case "teams":
		allowed["workspace"] = true
		allowed["project"] = true
	case "ticket", "messages":
		if args[0] == "messages" {
			allowed["view"] = true
		}
		allowed["workspace"] = true
		allowed["ticket"] = true
	case "resolve":
		for _, s := range []string{"workspace", "project", "team", "recipient"} {
			allowed[s] = true
		}
	case "retry":
		allowed["request"] = true
	case "prepare":
		for _, s := range []string{"file", "base", "template", "workspace", "project", "ticket"} {
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
	if *dir == "" {
		root, e := os.UserConfigDir()
		if e != nil {
			return e
		}
		*dir = filepath.Join(root, "syntra")
	}
	store, e := toolclient.OpenStore(ctx, *dir)
	if e != nil {
		return e
	}
	defer store.Close()
	p, e := store.Load()
	if e != nil {
		return e
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
	if args[0] == "prepare" && *template != "" {
		if *file != "" || *base != "" {
			return errors.New("--template cannot be combined with --file or --base")
		}
		if *template == "reply" {
			t, err := app.Ticket(ctx, *workspace, *ticket)
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
	case "tickets":
		v := v1.TicketVisibility_TICKET_VISIBILITY_PUBLISHED
		if *view == "own-drafts" {
			v = v1.TicketVisibility_TICKET_VISIBILITY_OWN_DRAFTS
		} else if *view != "published" {
			return errors.New("invalid explicit ticket view")
		}
		rows, err := app.Tickets(ctx, *workspace, v)
		e = err
		if e == nil {
			message = &v1.ListTicketsResponse{Tickets: rows}
		}
	case "messages":
		v := v1.MessageView_MESSAGE_VIEW_PUBLISHED
		if *view == "own-drafts" {
			v = v1.MessageView_MESSAGE_VIEW_OWN_DRAFTS
		} else if *view != "published" {
			return errors.New("invalid explicit message view")
		}
		message, e = app.Messages(ctx, *workspace, *ticket, v)
	case "ticket":
		message, e = app.Ticket(ctx, *workspace, *ticket)
	case "resolve":
		result, e = app.Resolve(ctx, toolclient.Selection{Workspace: *workspace, Project: *project, Team: *team, Recipient: *recipient})
	case "retry":
		message, e = app.Retry(ctx, *request)
	default:
		var target proto.Message
		switch args[0] {
		case "prepare":
			target = &v1.PrepareContentRequest{}
		case "link":
			target = &v1.LinkContentRequest{}
		case "publish":
			target = &v1.PublishContentRequest{}
		case "snapshot":
			target = &v1.GetContentSnapshotRequest{}
		}
		if *file == "" {
			return errors.New("explicit --file required; use - for bounded stdin")
		}
		var r io.Reader = os.Stdin
		if *file != "-" {
			input, err := openRequestFile(*file)
			if err != nil {
				return errors.New("request file unavailable")
			}
			defer input.Close()
			stat, err := input.Stat()
			if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1048576 {
				return errors.New("request file must be a bounded regular file")
			}
			r = input
		}
		if e = readContentWithContext(ctx, r, target); e != nil {
			return e
		}
		switch q := target.(type) {
		case *v1.PrepareContentRequest:
			message, e = app.Prepare(ctx, q, *base)
		case *v1.LinkContentRequest:
			message, e = app.Link(ctx, q, *base)
		case *v1.PublishContentRequest:
			message, e = app.Publish(ctx, q, *base)
		case *v1.GetContentSnapshotRequest:
			var s *toolclient.VerifiedSnapshot
			s, e = app.Snapshot(ctx, q, *base)
			if e == nil {
				var raw []byte
				if s.Snapshot != nil {
					raw, e = protojson.Marshal(s.Snapshot)
				} else {
					raw, e = protojson.Marshal(s.Legacy)
				}
				if e == nil {
					result = struct {
						Verification string          `json:"verification"`
						Record       json.RawMessage `json:"record"`
					}{s.Verification, raw}
				}
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
	done := make(chan error, 1)
	go func() {
		done <- toolclient.ReadContentInputWithCorrections(r, target, func(s string) { fmt.Fprintln(os.Stderr, "syntra:", s) })
	}()
	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		return errors.New("request input cancelled or deadline exceeded")
	}
}
