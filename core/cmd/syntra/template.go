package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/toolclient"
)

func printTemplate(kind, workspace, project, account string, ticket *v1.Ticket) error {
	if workspace == "" {
		return errors.New("--workspace is required for a template")
	}
	uuid, e := toolclient.NewRequestID()
	if e != nil {
		return e
	}
	content := map[string]any{"formatVersion": 1, "expectedAuthorUserId": account, "workspaceId": workspace, "title": "TODO: SYNTRA title", "originalDestinationIntent": "TODO: SYNTRA recipient", "destination": map[string]any{"unlinkedLabel": "TODO: SYNTRA recipient"}, "summary": "TODO: SYNTRA summary", "contextMarkdown": "TODO: SYNTRA context"}
	request := map[string]any{"requestId": uuid, "content": content}
	if project != "" {
		content["projectId"] = project
	}
	if kind == "reply" {
		if ticket == nil {
			return errors.New("--ticket is required for a reply template")
		}
		content["existingTicketId"] = ticket.Id
		content["title"] = ticket.Title
		request["expectedTicketRevision"] = ticket.Revision
		if ticket.ProjectId != "" {
			content["projectId"] = ticket.ProjectId
		} else {
			delete(content, "projectId")
		}
		if ticket.CreatorUserId != account && ticket.CreatorUserId != "" {
			content["destination"] = map[string]any{"linkedMemberId": ticket.CreatorUserId}
			content["originalDestinationIntent"] = "TODO: SYNTRA recipient name"
		}
	} else if kind != "new" {
		return errors.New("--template must be reply or new")
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "Replace every TODO: SYNTRA placeholder before running syntra prepare --file.")
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(request)
}
