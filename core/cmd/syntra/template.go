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

func printTemplate(kind, workspace, project, account string, thread *v1.Thread, message string) error {
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
		if thread == nil {
			return errors.New("--thread is required for a reply template")
		}
		if err := toolclient.CheckReplyState(thread); err != nil {
			return err
		}
		content["existingThreadId"] = thread.Id
		if message != "" {
			content["formatVersion"] = 2
			content["replyToMessageId"] = message
		}
		content["title"] = thread.Title
		request["expectedThreadRevision"] = thread.Revision
		if thread.ProjectId != "" {
			content["projectId"] = thread.ProjectId
		} else {
			delete(content, "projectId")
		}
		// A reply goes to every other participant of the thread by default; replying to a subset is not supported.
		content["destination"] = map[string]any{"replyAll": true}
		content["originalDestinationIntent"] = "everyone in the thread"
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
