# Syntra MCP tools

| Tool | Use |
| --- | --- |
| `identity` | Current verified account and connection, profile directory, named permissions and time until expiry |
| `workspaces` | All currently accessible spaces |
| `projects` | Projects in one explicit space |
| `members` | Current authorized directory in one space |
| `teams` | Assigned teams and their current included members |
| `resolve` | Unique resolution or clarification; never sends |
| `threads` | Published threads or the caller’s private pending threads (`own-prepared`) |
| `thread` | One authorized thread and its current state and revision |
| `messages` | Published messages or the caller’s private prepared messages (`own-prepared`) |
| `snapshot` | Read content and report the available integrity evidence |
| `prepare` | Retain exact private content, no send |
| `link` | Explicit private destination resolution, no send |
| `publish` | Separate exact-content publication with opted-in capability |
| `discard` | Delete the author's own unsent draft (PREPARE capability, exact revisions) |
| `retry` | Recover the original retained operation with the original request ID |

The server rejects unknown fields, duplicate JSON keys, unsafe scalar encodings and incompatible destination variants before lossy decoding. Use the current tool schema rather than inventing argument aliases. Technical context is an exact Markdown string, not a file path or executable command.

Example: “Record this question for Pedro in Bimtrazer.” Discover the account's spaces, resolve the named project and Pedro in its space, and clarify duplicate names. Prepare the question privately. If Pedro is absent, retain the intent “Pedro” as unlinked and explain that it was not delivered. A later explicit link to a real member still does not publish. If the user requests sending, publish only the exact authorized retained message.

Example: “List my spaces.” Call `workspaces`; do not require choosing a space first. Directory access is checked by Syntra on every request and does not grant membership.

For a reply, read the open thread with `thread` first. Set
`content.existingThreadId` to its ID and copy its `revision` to the top-level
`expectedThreadRevision` field. The revision is an opaque base64url token
encoding 32 bytes. Use the thread's exact title and project ID. Omit
`projectId` if the thread has none. Read the current tool schema for the other
required fields.

## Fork and quote a message

Call `fork` with `workspaceId`, `sourceThreadId`, `anchorMessageId`, `title`,
and a fresh `requestId`. The anchor must be published in the source thread.
PUBLISH is required. The result contains the new open thread and its origin.
Omit `sourceMemberIds` to seed active source participants or pass an empty array
to select none. `extraMemberIds` adds active workspace members. The creator is
included in the limit of 11 distinct participants. Creation sends no
notification. Resend the same arguments and request ID to retry.

Only threads have titles. A message has no title: `prepare` takes its recipients
and a nonblank Markdown body in `request.content.contextMarkdown`, with
`request.content.formatVersion` set to 3. `content.title` is the thread title.
To quote a published message in the same thread, set
`request.content.replyToMessageId`. The anchor is part of the content digest.
A private preparation saved in format 1 or 2 must be prepared again in format 3
before it can be linked or sent.
