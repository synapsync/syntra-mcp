package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

func printTemplate(kind, workspace, project, account string, ticket *v1.Ticket) error {
	if workspace == "" {
		return errors.New("--workspace is required for a template")
	}
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return errors.New("cannot generate requestId")
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
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
			content["originalDestinationIntent"] = ticket.CreatorUserId
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
