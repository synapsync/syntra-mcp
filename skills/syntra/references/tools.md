# Syntra MCP tools

| Tool | Use |
| --- | --- |
| `identity` | Current verified account and connection, profile directory, named permissions and time until expiry |
| `workspaces` | All currently accessible spaces |
| `projects` | Projects in one explicit space |
| `members` | Current authorized directory in one space |
| `teams` | Assigned teams and their current included members |
| `resolve` | Unique resolution or clarification; never sends |
| `tickets` | Explicit published or own-private-draft view |
| `ticket` | One authorized ticket |
| `messages` | Explicit published or own-private-draft message view |
| `snapshot` | Read content and report the available integrity evidence |
| `prepare` | Retain exact private content, no send |
| `link` | Explicit private destination resolution, no send |
| `publish` | Separate exact-content publication with opted-in capability |
| `retry` | Recover the original retained operation with the original request ID |

The server rejects unknown fields, duplicate JSON keys, unsafe scalar encodings and incompatible destination variants before lossy decoding. Use the current tool schema rather than inventing argument aliases. Technical context is an exact Markdown string, not a file path or executable command.

Example: “Record this question for Pedro in Bimtrazer.” Discover the account's spaces, resolve the named project and Pedro in its space, and clarify duplicate names. Prepare the question privately. If Pedro is absent, retain the intent “Pedro” as unlinked and explain that it was not delivered. A later explicit link to a real member still does not publish. If the user requests sending, publish only the exact authorized retained message.

Example: “List my spaces.” Call `workspaces`; do not require choosing a space first. Directory access is checked by Syntra on every request and does not grant membership.

For a reply, read the ticket first and copy its `revision` exactly to
`expectedTicketRevision`. It is an opaque base64url token encoding 32 bytes,
not a numeric revision. Use the exact ticket title and project ID; omit
`projectId` if absent. Do not invent these values.
