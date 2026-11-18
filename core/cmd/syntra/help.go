package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"syntra.local/mcp/internal/toolclient"
)

type helpEntry struct{ purpose, flags, example, output string }

var helpEntries = map[string]helpEntry{
	"version":    {"Print native binary version.", "", "syntra version", "Native binary version."},
	"login":      {"Connect a human account.", "[--method browser|otp] [--api URL] [--browser-origin URL] [--provider URL] [--publishable-key KEY] [--no-browser] [--reauthorize] [--prepare] [--publish] [--mark-read] [--label TEXT] [--lifetime-seconds N]", "syntra login --prepare", "Connection metadata; browser consent or human OTP authentication."},
	"status":     {"Show local connection status.", "", "syntra status", "Local profile metadata."},
	"identity":   {"Verify the current identity.", "", "syntra identity", "Account and connection metadata."},
	"disconnect": {"Revoke the connection and remove the profile.", "", "syntra disconnect", "Confirmation."},
	"forget":     {"Remove the local profile without revocation.", "", "syntra forget", "Confirmation."},
	"mcp":        {"Serve local MCP over stdio.", "[--call-timeout DURATION]", "syntra mcp", "JSON-RPC on stdout."},
	"workspaces": {"List accessible workspaces.", "", "syntra workspaces", "Workspace JSON."},
	"projects":   {"List workspace projects.", "--workspace UUID", "syntra projects --workspace WORKSPACE_UUID", "Project JSON."},
	"teams":      {"List project teams.", "--workspace UUID --project UUID", "syntra teams --workspace WORKSPACE_UUID --project PROJECT_UUID", "Team JSON."},
	"members":    {"List workspace members.", "--workspace UUID", "syntra members --workspace WORKSPACE_UUID", "Member JSON."},
	"resolve":    {"Resolve workspace, project, team and recipient names.", "--workspace NAME [--project NAME] [--team NAME] [--recipient NAME]", "syntra resolve --workspace Acme --recipient Alex", "Resolved IDs or clarification JSON."},
	"tickets":    {"List tickets.", "--workspace UUID [--view published|own-drafts]", "syntra tickets --workspace WORKSPACE_UUID", "Ticket JSON."},
	"ticket":     {"Read one ticket, its state and its opaque revision. Closed tickets accept no replies.", "--workspace UUID --ticket UUID", "syntra ticket --workspace WORKSPACE_UUID --ticket TICKET_UUID", "Ticket JSON, including state and revision."},
	"messages":   {"Read ticket messages.", "--workspace UUID --ticket UUID [--view published|own-drafts]", "syntra messages --workspace WORKSPACE_UUID --ticket TICKET_UUID", "Message JSON."},
	"reply":      {"Prepare a reply to the creator of a published, nonclosed ticket.", "--workspace UUID --ticket UUID --summary TEXT --context-file PATH|-", "syntra reply --workspace WORKSPACE_UUID --ticket TICKET_UUID --summary Done --context-file context.md", "Draft ID and an exact command for explicit publication."},
	"prepare":    {"Prepare private content without publishing.", "--file PATH|- [--base REQUEST_UUID] | --template new|reply --workspace UUID [--project UUID] [--ticket UUID]", "syntra prepare --template reply --workspace WORKSPACE_UUID --ticket TICKET_UUID", "Template JSON for --template (reply requires a published, nonclosed ticket); private draft snapshot JSON for --file."},
	"link":       {"Link a private draft to a member without publishing.", "--file PATH|- --base REQUEST_UUID", "syntra link --file link.json --base REQUEST_UUID", "Private draft snapshot JSON."},
	"publish":    {"Publish an exact retained private draft.", "--file PATH|- --base REQUEST_UUID", "syntra publish --file publish.json --base REQUEST_UUID", "Published snapshot JSON."},
	"snapshot":   {"Read and verify a content snapshot.", "--file PATH|- [--base REQUEST_UUID]", "syntra snapshot --file snapshot.json", "Snapshot JSON and verification level."},
	"retry":      {"Retry an exact retained request.", "--request UUID", "syntra retry --request REQUEST_UUID", "Original result JSON."},
}
var helpOrder = []string{"version", "login", "status", "identity", "disconnect", "forget", "mcp", "workspaces", "projects", "teams", "members", "resolve", "tickets", "ticket", "messages", "reply", "prepare", "link", "publish", "snapshot", "retry"}

func usage(name string) string {
	e := helpEntries[name]
	if name == "mcp" {
		return "Usage: syntra mcp [--profile-dir PATH] [--api URL] [--call-timeout DURATION]"
	}
	return strings.TrimSpace("Usage: syntra " + name + " " + e.flags + " [--profile-dir PATH] [--api URL] [--timeout DURATION]")
}
func printHelp(name string) {
	if name == "" {
		fmt.Fprintln(os.Stdout, "Usage: syntra <command> [options]\n\nCommands:")
		for _, n := range helpOrder {
			fmt.Fprintf(os.Stdout, "  %-12s %s\n", n, helpEntries[n].purpose)
		}
		fmt.Fprintln(os.Stdout, "\nTypical workflow: login/status -> workspaces/projects/tickets -> ticket -> resolve -> prepare -> publish\nRun syntra help <command> for flags and examples.\nExit codes: 0 success; 1 error.\nOTP and credentials are never accepted as arguments.")
		return
	}
	e := helpEntries[name]
	fmt.Fprintf(os.Stdout, "%s\n\n%s\n\nRequired flags are shown without brackets; bracketed flags are optional.\nCommon options: --profile-dir PATH, --api URL, --timeout DURATION (mcp uses --call-timeout).\n\nExample: %s\nPrints: %s\n", usage(name), e.purpose, e.example, e.output)
}
func unknownCommand(s string) error {
	if !toolclient.IsSafeDiagnosticToken(s) {
		return fmt.Errorf("unknown command: an invalid value. Run \"syntra --help\".")
	}
	best := ""
	d := 4
	for _, n := range helpOrder {
		if x := editDistance(s, n); x < d {
			best = n
			d = x
		}
	}
	if best != "" {
		return fmt.Errorf("unknown command %q. Did you mean %q? Run \"syntra --help\".", s, best)
	}
	return fmt.Errorf("unknown command %q. Run \"syntra --help\".", s)
}
func editDistance(a, b string) int {
	x := make([]int, len(b)+1)
	for j := range x {
		x[j] = j
	}
	for i := 1; i <= len(a); i++ {
		prev := x[0]
		x[0] = i
		for j := 1; j <= len(b); j++ {
			old := x[j]
			c := 0
			if a[i-1] != b[j-1] {
				c = 1
			}
			v := x[j] + 1
			if x[j-1]+1 < v {
				v = x[j-1] + 1
			}
			if prev+c < v {
				v = prev + c
			}
			x[j] = v
			prev = old
		}
	}
	return x[len(b)]
}

var optionName = regexp.MustCompile(`(?:flag provided but not defined:|flag needs an argument:|invalid value .* for flag) -(-?[a-zA-Z][a-zA-Z0-9-]*)`)

func optionError(command string, err error) error {
	name := ""
	if err != nil {
		if m := optionName.FindStringSubmatch(err.Error()); len(m) > 1 {
			name = " --" + toolclient.SafeDiagnosticToken(strings.TrimLeft(m[1], "-"))
		}
	}
	return fmt.Errorf("invalid or missing option%s; OTP and credentials are never accepted as arguments. %s", name, usage(command))
}
