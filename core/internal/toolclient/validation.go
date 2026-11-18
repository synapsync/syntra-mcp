package toolclient

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	v1 "syntra.local/mcp/gen/syntra/v1"
	ci "syntra.local/mcp/internal/contentintegrity"
)

func clientField(name, message string) error {
	return fmt.Errorf("%w: %s %s", ci.ErrWire, name, message)
}
func clientID(name, value string) error {
	if value == "" {
		return clientField(name, "is required (canonical lowercase UUID)")
	}
	if !validUUID(value) {
		return clientField(name, "must be a canonical lowercase UUID")
	}
	return nil
}
func clientRevision(name, value string) error {
	b, e := base64.RawURLEncoding.DecodeString(value)
	if e != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != value {
		return clientField(name, "must be the opaque revision; copy `revision` exactly from `syntra ticket` (for draft revision use the retained private draft)")
	}
	return nil
}
func clientExpectation(name string, e *v1.ContentDraftExpectation) error {
	if e == nil {
		return clientField(name, "is required (copy the retained snapshot expectation)")
	}
	for _, f := range []struct{ name, value string }{{"ticketId", e.TicketId}, {"draftId", e.DraftId}} {
		if err := clientID(name+"."+f.name, f.value); err != nil {
			return err
		}
	}
	for _, f := range []struct{ name, value string }{{"expectedTicketRevision", e.ExpectedTicketRevision}, {"expectedDraftRevision", e.ExpectedDraftRevision}} {
		if err := clientRevision(name+"."+f.name, f.value); err != nil {
			return err
		}
	}
	if len(e.ExpectedContentDigest) != 32 {
		return clientField(name+".expectedContentDigest", "must be the retained 32-byte content digest encoded as base64")
	}
	return nil
}
func clientContent(name string, c *v1.AuthoredContent, owner string) error {
	if c == nil {
		return clientField(name, "is required")
	}
	if c.FormatVersion != 1 {
		return clientField(name+".formatVersion", "must be 1")
	}
	for _, f := range []struct{ name, value string }{{"expectedAuthorUserId", c.ExpectedAuthorUserId}, {"workspaceId", c.WorkspaceId}} {
		if err := clientID(name+"."+f.name, f.value); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		name  string
		value *string
	}{{"projectId", c.ProjectId}, {"existingTicketId", c.ExistingTicketId}} {
		if f.value != nil {
			if err := clientID(name+"."+f.name, *f.value); err != nil {
				return err
			}
		}
	}
	if c.ExpectedAuthorUserId != owner {
		return fmt.Errorf("%w: %s.expectedAuthorUserId must equal the account shown by syntra identity", ci.ErrIntegrity, name)
	}
	for _, f := range []struct{ name, value string }{{"title", c.Title}, {"originalDestinationIntent", c.OriginalDestinationIntent}, {"summary", c.Summary}} {
		if f.value == "" {
			return clientField(name+"."+f.name, "is required and must be a non-empty string")
		}
	}
	for _, f := range []struct {
		name, value string
		max         int
	}{{"title", c.Title, 1024}, {"originalDestinationIntent", c.OriginalDestinationIntent, 1024}, {"summary", c.Summary, 8192}, {"contextMarkdown", c.ContextMarkdown, 131072}} {
		if len(f.value) > f.max || !utf8.ValidString(f.value) || strings.ContainsRune(f.value, 0) {
			return clientField(name+"."+f.name, fmt.Sprintf("must be valid UTF-8 without NUL and at most %d bytes", f.max))
		}
	}
	if c.Destination == nil || c.Destination.Target == nil {
		return clientField(name+".destination", "is required (exactly one of linkedMemberId or unlinkedLabel)")
	}
	switch x := c.Destination.Target.(type) {
	case *v1.ContentDestination_LinkedMemberId:
		if err := clientID(name+".destination.linkedMemberId", x.LinkedMemberId); err != nil {
			return err
		}
	case *v1.ContentDestination_UnlinkedLabel:
		if len(x.UnlinkedLabel) > 1024 || !utf8.ValidString(x.UnlinkedLabel) || strings.ContainsRune(x.UnlinkedLabel, 0) {
			return clientField(name+".destination.unlinkedLabel", "must be valid UTF-8 without NUL and at most 1024 bytes")
		}
		if x.UnlinkedLabel == "" {
			return clientField(name+".destination.unlinkedLabel", "must be non-empty")
		}
	}
	return checkPlaceholders(c)
}
