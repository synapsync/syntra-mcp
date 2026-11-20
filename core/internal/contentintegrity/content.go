// Package contentintegrity defines portable authored-content bytes, not RPC authorization.
package contentintegrity

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// MaxRecipients is the structural ceiling of one message's recipient set (also enforced by the database).
// The configured limit may only lower it.
const MaxRecipients = 10

// Destination kinds: "linked" is one person, "linkedSet" two to MaxRecipients distinct people in display
// order, "team" one explicit team expanded to its active members at send time, "replyAll" every other participant
// of the thread expanded at send time, "unlinked" a private label.
type Destination struct {
	Kind      string   `json:"kind"`
	MemberID  string   `json:"memberId,omitempty"`
	MemberIDs []string `json:"memberIds,omitempty"`
	TeamID    string   `json:"teamId,omitempty"`
	Label     string   `json:"label,omitempty"`
	// ReplyAll marks the destination "every other participant of the thread" (existing-thread replies only).
	ReplyAll bool `json:"replyAll,omitempty"`
}
type Content struct {
	FormatVersion             uint32      `json:"formatVersion"`
	ExpectedAuthorUserID      string      `json:"expectedAuthorUserId"`
	WorkspaceID               string      `json:"workspaceId"`
	ProjectID                 *string     `json:"projectId"`
	ExistingThreadID          *string     `json:"existingThreadId,omitempty"`
	Title                     string      `json:"title"`
	OriginalDestinationIntent string      `json:"originalDestinationIntent"`
	Destination               Destination `json:"destination"`
	Summary                   string      `json:"summary"`
	ContextMarkdown           string      `json:"contextMarkdown"`
}

func Encode(c Content) ([]byte, error) {
	if c.FormatVersion != 1 {
		return nil, errors.New("unsupported content version")
	}
	out := append([]byte("syntra.authored-content\x00"), 1)
	var failure error
	field := func(s string, max int, allowEmpty bool) {
		if failure != nil {
			return
		}
		if len(s) > max || (!allowEmpty && len(s) == 0) || !utf8.ValidString(s) {
			failure = errors.New("invalid content field")
			return
		}
		out = binary.BigEndian.AppendUint32(out, uint32(len(s)))
		out = append(out, s...)
	}
	optional := func(s *string) {
		if s == nil {
			out = append(out, 0)
		} else {
			out = append(out, 1)
			field(*s, 128, false)
		}
	}
	field(c.ExpectedAuthorUserID, 128, false)
	field(c.WorkspaceID, 128, false)
	optional(c.ProjectID)
	// Slot 5 (formerly area_id) is frozen as the constant ABSENT marker: the v1 byte layout is positional.
	out = append(out, 0)
	optional(c.ExistingThreadID)
	field(c.Title, 1024, false)
	field(c.OriginalDestinationIntent, 1024, false)
	d := c.Destination
	switch d.Kind {
	case "linked":
		if d.Label != "" || len(d.MemberIDs) != 0 || d.TeamID != "" || d.ReplyAll {
			return nil, errors.New("inactive destination variant")
		}
		out = append(out, 1)
		field(d.MemberID, 128, false)
	case "unlinked":
		if d.MemberID != "" || len(d.MemberIDs) != 0 || d.TeamID != "" || d.ReplyAll {
			return nil, errors.New("inactive destination variant")
		}
		out = append(out, 2)
		field(d.Label, 1024, false)
	case "linkedSet":
		if d.MemberID != "" || d.Label != "" || d.TeamID != "" || d.ReplyAll || len(d.MemberIDs) < 2 || len(d.MemberIDs) > MaxRecipients {
			return nil, errors.New("invalid destination")
		}
		seen := map[string]bool{}
		out = append(out, 3)
		out = binary.BigEndian.AppendUint32(out, uint32(len(d.MemberIDs)))
		for _, id := range d.MemberIDs {
			if seen[id] {
				return nil, errors.New("duplicate recipient")
			}
			seen[id] = true
			field(id, 128, false)
		}
	case "team":
		if d.MemberID != "" || d.Label != "" || len(d.MemberIDs) != 0 || d.ReplyAll {
			return nil, errors.New("inactive destination variant")
		}
		out = append(out, 4)
		field(d.TeamID, 128, false)
	case "replyAll":
		if d.MemberID != "" || d.Label != "" || len(d.MemberIDs) != 0 || d.TeamID != "" || !d.ReplyAll || c.ExistingThreadID == nil {
			return nil, errors.New("invalid destination")
		}
		out = append(out, 5)
	default:
		return nil, errors.New("invalid destination")
	}
	field(c.Summary, 8192, false)
	field(c.ContextMarkdown, 131072, true)
	if failure != nil {
		return nil, failure
	}
	return out, nil
}
func Digest(c Content) ([32]byte, error) {
	bytes, err := Encode(c)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(bytes), nil
}
