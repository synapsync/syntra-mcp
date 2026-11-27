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
	"version":         {"Print native binary version.", "", "syntra version", "Native binary version."},
	"login":           {"Connect a human account.", "[--method browser|otp] [--api URL] [--browser-origin URL] [--provider URL] [--publishable-key KEY] [--no-browser] [--reauthorize] [--prepare] [--publish] [--label TEXT] [--lifetime-seconds N]", "syntra login --prepare", "Connection metadata; browser consent or human OTP authentication."},
	"status":          {"Show local connection status.", "", "syntra status", "Local profile metadata."},
	"identity":        {"Verify the current identity.", "", "syntra identity", "Account and connection metadata."},
	"disconnect":      {"Revoke the connection and remove the profile.", "", "syntra disconnect", "Confirmation."},
	"forget":          {"Remove the local profile without revocation.", "", "syntra forget", "Confirmation."},
	"mcp":             {"Serve local MCP over stdio.", "[--call-timeout DURATION]", "syntra mcp", "JSON-RPC on stdout."},
	"workspaces":      {"List accessible workspaces.", "", "syntra workspaces", "Workspace JSON."},
	"projects":        {"List workspace projects.", "--workspace UUID", "syntra projects --workspace WORKSPACE_UUID", "Project JSON."},
	"teams":           {"List project teams.", "--workspace UUID --project UUID", "syntra teams --workspace WORKSPACE_UUID --project PROJECT_UUID", "Team JSON."},
	"members":         {"List workspace members.", "--workspace UUID", "syntra members --workspace WORKSPACE_UUID", "Member JSON."},
	"resolve":         {"Resolve workspace, project, team and recipient names or @usernames.", "--workspace NAME [--project NAME] [--team NAME] [--recipient NAME | --recipients A,B,C | --as-team]", "syntra resolve --workspace Acme --recipients Alex,Sam", "Resolved IDs, the exact content.destination to use (one person, several people or an explicit team), or clarification JSON."},
	"threads":         {"List threads.", "--workspace UUID [--view published|own-prepared]", "syntra threads --workspace WORKSPACE_UUID", "Thread JSON."},
	"thread":          {"Read one thread, its state and its opaque revision. Closed threads accept no replies.", "--workspace UUID --thread UUID", "syntra thread --workspace WORKSPACE_UUID --thread THREAD_UUID", "Thread JSON, including state and revision."},
	"messages":        {"Read thread messages.", "--workspace UUID --thread UUID [--view published|own-prepared]", "syntra messages --workspace WORKSPACE_UUID --thread THREAD_UUID", "Message JSON."},
	"reply":           {"Prepare a reply to every other participant of a published, nonclosed thread (reply to all). Optionally quote a published message in that thread.", "--workspace UUID --thread UUID [--message UUID] --context-file PATH|-", "syntra reply --workspace WORKSPACE_UUID --thread THREAD_UUID --context-file context.md", "Prepared ID and an exact command for explicit publication."},
	"prepare":         {"Prepare private content without publishing.", "--file PATH|- [--base REQUEST_UUID] | --template new|reply --workspace UUID [--project UUID] [--thread UUID [--message UUID]]", "syntra prepare --template reply --workspace WORKSPACE_UUID --thread THREAD_UUID", "Template JSON for --template (reply requires a published, nonclosed thread); private prepared message snapshot JSON for --file."},
	"link":            {"Link a private prepared message to a member without publishing.", "--file PATH|- --base REQUEST_UUID", "syntra link --file link.json --base REQUEST_UUID", "Private prepared snapshot JSON."},
	"publish":         {"Publish an exact retained private prepared message.", "--file PATH|- --base REQUEST_UUID", "syntra publish --file publish.json --base REQUEST_UUID", "Published snapshot JSON."},
	"discard":         {"Delete your own unsent prepared message (the initial one also removes its pending thread). Never publishes.", "--file PATH|-", "syntra discard --file discard.json", "Discard result JSON (preparedMessageId, threadDiscarded). Resend the same requestId to retry."},
	"snapshot":        {"Read and verify a content snapshot.", "--file PATH|- [--base REQUEST_UUID]", "syntra snapshot --file snapshot.json", "Snapshot JSON and verification level."},
	"retry":           {"Retry an exact retained request.", "--request UUID", "syntra retry --request REQUEST_UUID", "Original result JSON."},
	"fork":            {"Create an open thread from a published message. Needs publish capability. Seeds active source participants by default, up to 11 total including you.", "--workspace UUID --thread UUID --message UUID --title TITLE --request-id UUID [--member UUID]... [--source-member UUID | --no-source-members]", "syntra fork --workspace WORKSPACE_UUID --thread THREAD_UUID --message MESSAGE_UUID --title Follow-up --request-id REQUEST_UUID", "New thread JSON with its origin. Title must be a single line of at most 240 characters. Repeat --member for extras or --source-member for an explicit source selection. Resend the same arguments and request ID to retry."},
	"add-participant": {"Add an active member as a full participant of a published thread (open or closed) without sending a message. Needs the publish capability and acts for you; never marks anything read or reviewed.", "--workspace UUID --thread UUID --member UUID --request-id UUID", "syntra add-participant --workspace WORKSPACE_UUID --thread THREAD_UUID --member MEMBER_UUID --request-id REQUEST_UUID", "Thread JSON with participants and added (false when the person already took part). Resend the same --request-id to retry an unconfirmed result."},
}
var helpOrder = []string{"version", "login", "status", "identity", "disconnect", "forget", "mcp", "workspaces", "projects", "teams", "members", "resolve", "threads", "thread", "messages", "reply", "prepare", "link", "publish", "discard", "snapshot", "retry", "add-participant", "fork"}

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
		fmt.Fprintln(os.Stdout, "\nTypical workflow: login/status -> workspaces/projects/threads -> thread -> resolve -> prepare -> publish\nRun syntra help <command> for flags and examples.\nExit codes: 0 success; 1 error.\nOTP and credentials are never accepted as arguments.")
		return
	}
	e := helpEntries[name]
	fmt.Fprintf(os.Stdout, "%s\n\n%s\n\nRequired flags are shown without brackets; bracketed flags are optional.\nCommon options: --profile-dir PATH, --api URL, --timeout DURATION (mcp uses --call-timeout).\n\nExample: %s\nPrints: %s\n", usage(name), e.purpose, e.example, e.output)
	if name == "login" {
		fmt.Fprintln(os.Stdout, `
One login serves every agent session of the same OS user using the default
Syntra profile. Use syntra status to see its location. For a custom profile,
pass the same --profile-dir to login and to the agent's MCP registration.

Browser consent requires read and lets you deselect optional permissions.
A first browser login without permission flags requests read, prepare and publish.
Supply permission flags to request only the listed optional permissions.
OTP login without permission flags grants read only.
  --prepare    Create threads and prepare or link private prepared messages.
  --publish    Publish explicitly. Requires --prepare.
Use --reauthorize to request new permissions for the installed connection.
Include any optional permissions you want to keep and approve in the browser.`)
	}
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
