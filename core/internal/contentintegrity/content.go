// Package contentintegrity defines portable authored-content bytes, not RPC authorization.
package contentintegrity

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

type Destination struct {
	Kind     string `json:"kind"`
	MemberID string `json:"memberId,omitempty"`
	Label    string `json:"label,omitempty"`
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
	switch c.Destination.Kind {
	case "linked":
		if c.Destination.Label != "" {
			return nil, errors.New("inactive destination variant")
		}
		out = append(out, 1)
		field(c.Destination.MemberID, 128, false)
	case "unlinked":
		if c.Destination.MemberID != "" {
			return nil, errors.New("inactive destination variant")
		}
		out = append(out, 2)
		field(c.Destination.Label, 1024, false)
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
